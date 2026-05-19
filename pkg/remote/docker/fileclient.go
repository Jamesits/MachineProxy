//go:build backend_docker

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/remote"
)

// agentFileClient implements remote.FileClient by tunnelling each
// operation through a long-lived mproxy-agent running inside the
// container, via the agent CBOR mux.
type agentFileClient struct {
	mux  *agentproto.Mux
	sess *dockerSession

	// ctx is the lifetime context for file ops issued through this
	// client. It is the same context that drives the mux read-loop, so
	// closing the client (which calls cancel) also cancels any
	// in-flight ops.
	ctx    context.Context
	cancel context.CancelFunc
	doneCh chan struct{}
}

// newAgentFileClient launches a long-lived mproxy-agent inside the
// container via docker exec and constructs a FileClient that pipes
// every op through that agent.
func newAgentFileClient(parent context.Context, b *Backend, agentRemotePath string) (*agentFileClient, error) {
	ctx, cancel := context.WithCancel(parent)
	sess := newSession(ctx, b.cli, b.containerID)
	stdin, err := sess.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := sess.Start(shellQuote(agentRemotePath)); err != nil {
		cancel()
		return nil, fmt.Errorf("start file-op agent: %w", err)
	}
	// Drain stderr so the agent doesn't block on a full pipe.
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	mux := agentproto.NewMux(stdout, stdin)
	c := &agentFileClient{mux: mux, sess: sess, ctx: ctx, cancel: cancel, doneCh: make(chan struct{})}

	go func() {
		defer close(c.doneCh)
		_, _ = mux.ReadLoop(ctx)
		_ = sess.Close()
	}()

	// Issue an initial Getwd to prime the agent into file-op service mode.
	// The agent dispatches on the first non-config frame type.
	probeCtx, probeCancel := context.WithTimeout(parent, 10*time.Second)
	defer probeCancel()
	if _, err := c.do(probeCtx, &agentproto.FileOpReq{Op: agentproto.FileOpGetwd}); err != nil {
		cancel()
		_ = sess.Close() // unblock the read loop goroutine
		<-c.doneCh
		return nil, fmt.Errorf("probe file-op agent: %w", err)
	}
	return c, nil
}

func (c *agentFileClient) Close() error {
	// Cancel the read-loop context first, then close the docker session.
	// Closing the session shuts the hijacked pipe, which unblocks any
	// in-progress mux.Decode() call so the goroutine can exit promptly.
	// Without the session close, the goroutine blocks on the pipe read
	// indefinitely even after context cancellation.
	if c.cancel != nil {
		c.cancel()
	}
	if c.sess != nil {
		_ = c.sess.Close()
	}
	<-c.doneCh
	return nil
}

func (c *agentFileClient) do(ctx context.Context, req *agentproto.FileOpReq) (*agentproto.FileOpResp, error) {
	resp, err := c.mux.FileOp(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("nil file-op response")
	}
	if resp.Errno != 0 || resp.ErrMsg != "" {
		return resp, errnoToErr(resp)
	}
	return resp, nil
}

// errnoToErr maps a non-zero errno into the closest os.Err* sentinel
// so callers (workspacefs, pathstub) can use errors.Is on the result.
func errnoToErr(resp *agentproto.FileOpResp) error {
	switch syscall.Errno(resp.Errno) {
	case syscall.ENOENT:
		return os.ErrNotExist
	case syscall.EACCES:
		return os.ErrPermission
	case syscall.EEXIST:
		return os.ErrExist
	case syscall.EINVAL:
		return os.ErrInvalid
	}
	if resp.Errno != 0 {
		return syscall.Errno(resp.Errno)
	}
	if resp.ErrMsg != "" {
		return errors.New(resp.ErrMsg)
	}
	return fmt.Errorf("errno %d", resp.Errno)
}

// Open implements remote.FileClient.
func (c *agentFileClient) Open(p string) (remote.RemoteFile, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpOpen, Path: p})
	if err != nil {
		return nil, err
	}
	return &remoteFile{c: c, handle: resp.Handle}, nil
}

// Create implements remote.FileClient.
func (c *agentFileClient) Create(p string) (remote.RemoteWriteFile, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpCreate, Path: p})
	if err != nil {
		return nil, err
	}
	return &remoteWriteFile{c: c, handle: resp.Handle}, nil
}

// OpenFile implements remote.FileClient.
func (c *agentFileClient) OpenFile(p string, flags int, mode os.FileMode) (remote.RemoteWriteFile, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op:    agentproto.FileOpOpenFile,
		Path:  p,
		Flags: int32(flags),
		Mode:  fileModeToPOSIX(mode),
	})
	if err != nil {
		return nil, err
	}
	return &remoteWriteFile{c: c, handle: resp.Handle, flags: flags}, nil
}

// Stat implements remote.FileClient.
func (c *agentFileClient) Stat(p string) (os.FileInfo, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpStat, Path: p})
	if err != nil {
		return nil, err
	}
	if resp.Stat == nil {
		return nil, errors.New("stat: empty result")
	}
	return statInfo{s: *resp.Stat}, nil
}

// Lstat implements remote.FileClient.
func (c *agentFileClient) Lstat(p string) (os.FileInfo, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpLstat, Path: p})
	if err != nil {
		return nil, err
	}
	if resp.Stat == nil {
		return nil, errors.New("lstat: empty result")
	}
	return statInfo{s: *resp.Stat}, nil
}

// ReadDir implements remote.FileClient.
func (c *agentFileClient) ReadDir(p string) ([]os.FileInfo, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpReadDir, Path: p})
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, statInfo{s: e.Stat})
	}
	return out, nil
}

// Readlink implements remote.FileClient.
func (c *agentFileClient) Readlink(p string) (string, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpReadlink, Path: p})
	if err != nil {
		return "", err
	}
	return resp.Path, nil
}

// Mkdir implements remote.FileClient.
func (c *agentFileClient) Mkdir(p string, mode os.FileMode) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpMkdir, Path: p, Mode: fileModeToPOSIX(mode)})
	return err
}

// MkdirAll implements remote.FileClient.
func (c *agentFileClient) MkdirAll(p string, mode os.FileMode) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpMkdirAll, Path: p, Mode: fileModeToPOSIX(mode)})
	return err
}

// Remove implements remote.FileClient.
func (c *agentFileClient) Remove(p string) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpRemove, Path: p})
	return err
}

// Rename implements remote.FileClient.
func (c *agentFileClient) Rename(oldPath, newPath string) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpRename, Path: oldPath, NewPath: newPath,
	})
	return err
}

// Symlink implements remote.FileClient.
func (c *agentFileClient) Symlink(target, linkpath string) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpSymlink, Path: target, NewPath: linkpath,
	})
	return err
}

// Link implements remote.FileClient.
func (c *agentFileClient) Link(oldPath, newPath string) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpLink, Path: oldPath, NewPath: newPath,
	})
	return err
}

// Chmod implements remote.FileClient.
func (c *agentFileClient) Chmod(p string, mode os.FileMode) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpChmod, Path: p, Mode: fileModeToPOSIX(mode),
	})
	return err
}

// Chown implements remote.FileClient.
func (c *agentFileClient) Chown(p string, uid, gid int) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpChown, Path: p, UID: uint32(uid), GID: uint32(gid),
	})
	return err
}

// Chtimes implements remote.FileClient.
func (c *agentFileClient) Chtimes(p string, atime, mtime time.Time) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpChtimes, Path: p, AtimeNanos: atime.UnixNano(), MTimeNanos: mtime.UnixNano(),
	})
	return err
}

// Truncate implements remote.FileClient.
func (c *agentFileClient) Truncate(p string, size int64) error {
	_, err := c.do(c.ctx, &agentproto.FileOpReq{
		Op: agentproto.FileOpTruncate, Path: p, Size: size,
	})
	return err
}

// Statfs implements remote.FileClient.
func (c *agentFileClient) Statfs(p string) (*remote.Statfs, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpStatfs, Path: p})
	if err != nil {
		return nil, err
	}
	if resp.Statfs == nil {
		return nil, errors.New("statfs: empty result")
	}
	return &remote.Statfs{
		Blocks:  resp.Statfs.Blocks,
		Bfree:   resp.Statfs.Bfree,
		Bavail:  resp.Statfs.Bavail,
		Files:   resp.Statfs.Files,
		Ffree:   resp.Statfs.Ffree,
		Bsize:   resp.Statfs.Bsize,
		Frsize:  resp.Statfs.Frsize,
		NameLen: resp.Statfs.NameLen,
	}, nil
}

// Getwd implements remote.FileClient.
func (c *agentFileClient) Getwd() (string, error) {
	resp, err := c.do(c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpGetwd})
	if err != nil {
		return "", err
	}
	return resp.Path, nil
}

// remoteFile is the read-side handle returned by Open.
type remoteFile struct {
	c      *agentFileClient
	handle uint32
	mu     sync.Mutex
	pos    int64
}

func (f *remoteFile) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	off := f.pos
	n, err := f.ReadAt(p, off)
	f.pos += int64(n)
	return n, err
}

func (f *remoteFile) ReadAt(p []byte, off int64) (int, error) {
	resp, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{
		Op:     agentproto.FileOpReadAt,
		Handle: f.handle,
		Offset: off,
		Size:   int64(len(p)),
	})
	if err != nil {
		return 0, err
	}
	n := copy(p, resp.Data)
	if resp.EOF {
		return n, io.EOF
	}
	return n, nil
}

func (f *remoteFile) Close() error {
	_, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpClose, Handle: f.handle})
	return err
}

// remoteWriteFile is the write-side handle returned by Create / OpenFile.
type remoteWriteFile struct {
	c      *agentFileClient
	handle uint32
	flags  int
	mu     sync.Mutex
	pos    int64
}

func (f *remoteWriteFile) Write(p []byte) (int, error) {
	if f.flags&os.O_APPEND != 0 {
		return f.writeAt(p, -1)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	off := f.pos
	n, err := f.writeAt(p, off)
	f.pos += int64(n)
	return n, err
}

func (f *remoteWriteFile) ReadAt(p []byte, off int64) (int, error) {
	resp, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{
		Op:     agentproto.FileOpReadAt,
		Handle: f.handle,
		Offset: off,
		Size:   int64(len(p)),
	})
	if err != nil {
		return int(respN(resp)), err
	}
	n := copy(p, resp.Data)
	if resp.EOF {
		return n, io.EOF
	}
	return n, nil
}

func (f *remoteWriteFile) WriteAt(p []byte, off int64) (int, error) {
	if f.flags&os.O_APPEND != 0 {
		return f.writeAt(p, -1)
	}
	return f.writeAt(p, off)
}

func (f *remoteWriteFile) Stat() (os.FileInfo, error) {
	resp, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpFstat, Handle: f.handle})
	if err != nil {
		return nil, err
	}
	if resp.Stat == nil {
		return nil, errors.New("fstat: empty result")
	}
	return statInfo{s: *resp.Stat}, nil
}

func (f *remoteWriteFile) Truncate(size int64) error {
	_, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpFtruncate, Handle: f.handle, Size: size})
	return err
}

func (f *remoteWriteFile) writeAt(p []byte, off int64) (int, error) {
	resp, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{
		Op:     agentproto.FileOpWriteAt,
		Handle: f.handle,
		Offset: off,
		Data:   p,
	})
	if err != nil {
		return int(respN(resp)), err
	}
	return int(resp.N), nil
}

func respN(resp *agentproto.FileOpResp) int64 {
	if resp == nil {
		return 0
	}
	return resp.N
}

func fileModeToPOSIX(mode os.FileMode) uint32 {
	out := uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		out |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		out |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		out |= 0o1000
	}
	return out
}

func (f *remoteWriteFile) Sync() error {
	_, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpFsync, Handle: f.handle})
	return err
}

func (f *remoteWriteFile) Close() error {
	_, err := f.c.do(f.c.ctx, &agentproto.FileOpReq{Op: agentproto.FileOpClose, Handle: f.handle})
	return err
}

// statInfo adapts agentproto.FileStat to os.FileInfo.
type statInfo struct{ s agentproto.FileStat }

func (i statInfo) Name() string       { return i.s.Name }
func (i statInfo) Size() int64        { return i.s.Size }
func (i statInfo) Mode() os.FileMode  { return os.FileMode(i.s.Mode) }
func (i statInfo) ModTime() time.Time { return time.Unix(0, i.s.MTimeNanos) }
func (i statInfo) IsDir() bool        { return i.s.IsDir }
func (i statInfo) Sys() any           { return i.s }
