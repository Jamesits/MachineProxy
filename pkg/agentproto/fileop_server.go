package agentproto

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

// FileOpServer is the agent-side implementation of file ops. It is
// stateful: Open/Create return uint32 handles that subsequent ReadAt/
// WriteAt/Close requests reference. Handles are local to the server.
type FileOpServer struct {
	mu      sync.Mutex
	handles map[uint32]*os.File
	next    uint32
}

// NewFileOpServer constructs an empty server.
func NewFileOpServer() *FileOpServer {
	return &FileOpServer{handles: make(map[uint32]*os.File)}
}

// Handle dispatches a request to the matching os.* primitive and
// returns a response. Errors are surfaced through Resp.Errno + ErrMsg
// so the client can map them back to os.Err* sentinels.
func (s *FileOpServer) Handle(req *FileOpReq) *FileOpResp {
	resp := &FileOpResp{}
	switch req.Op {
	case FileOpOpen:
		f, err := os.Open(req.Path)
		if err != nil {
			return errResp(err)
		}
		resp.Handle = s.put(f)
	case FileOpCreate:
		f, err := os.Create(req.Path)
		if err != nil {
			return errResp(err)
		}
		resp.Handle = s.put(f)
	case FileOpOpenFile:
		f, err := os.OpenFile(req.Path, int(req.Flags), fileModeFromPOSIX(req.Mode))
		if err != nil {
			return errResp(err)
		}
		resp.Handle = s.put(f)
	case FileOpClose:
		f, ok := s.take(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		if err := f.Close(); err != nil {
			return errResp(err)
		}
	case FileOpFsync:
		f, ok := s.get(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		if err := f.Sync(); err != nil {
			return errResp(err)
		}
	case FileOpFstat:
		f, ok := s.get(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		st, err := f.Stat()
		if err != nil {
			return errResp(err)
		}
		resp.Stat = statFromInfo(st)
	case FileOpFtruncate:
		f, ok := s.get(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		if err := f.Truncate(req.Size); err != nil {
			return errResp(err)
		}
	case FileOpReadAt:
		f, ok := s.get(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		size := req.Size
		if size <= 0 {
			return resp
		}
		buf := make([]byte, size)
		n, err := f.ReadAt(buf, req.Offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return errResp(err)
		}
		resp.Data = buf[:n]
		resp.N = int64(n)
		resp.EOF = errors.Is(err, io.EOF)
	case FileOpWriteAt:
		f, ok := s.get(req.Handle)
		if !ok {
			return errResp(syscall.EBADF)
		}
		var n int
		var err error
		if req.Offset < 0 {
			n, err = f.Write(req.Data)
		} else {
			n, err = f.WriteAt(req.Data, req.Offset)
		}
		resp.N = int64(n)
		if err != nil {
			errOut := errResp(err)
			errOut.N = resp.N
			return errOut
		}
	case FileOpStat:
		st, err := os.Stat(req.Path)
		if err != nil {
			return errResp(err)
		}
		resp.Stat = statFromInfo(st)
	case FileOpLstat:
		st, err := os.Lstat(req.Path)
		if err != nil {
			return errResp(err)
		}
		resp.Stat = statFromInfo(st)
	case FileOpReadDir:
		entries, err := os.ReadDir(req.Path)
		if err != nil {
			return errResp(err)
		}
		out := make([]FileDirEntry, 0, len(entries))
		for _, e := range entries {
			info, ierr := e.Info()
			if ierr != nil {
				return errResp(ierr)
			}
			out = append(out, FileDirEntry{Stat: *statFromInfo(info)})
		}
		resp.Entries = out
	case FileOpMkdir:
		if err := os.Mkdir(req.Path, fileModeFromPOSIX(req.Mode)); err != nil {
			return errResp(err)
		}
	case FileOpMkdirAll:
		if err := os.MkdirAll(req.Path, fileModeFromPOSIX(req.Mode)); err != nil {
			return errResp(err)
		}
	case FileOpRemove:
		if err := os.Remove(req.Path); err != nil {
			return errResp(err)
		}
	case FileOpRename:
		if err := os.Rename(req.Path, req.NewPath); err != nil {
			return errResp(err)
		}
	case FileOpReadlink:
		target, err := os.Readlink(req.Path)
		if err != nil {
			return errResp(err)
		}
		resp.Path = target
	case FileOpSymlink:
		if err := os.Symlink(req.Path, req.NewPath); err != nil {
			return errResp(err)
		}
	case FileOpLink:
		if err := os.Link(req.Path, req.NewPath); err != nil {
			return errResp(err)
		}
	case FileOpChmod:
		if err := os.Chmod(req.Path, fileModeFromPOSIX(req.Mode)); err != nil {
			return errResp(err)
		}
	case FileOpChown:
		if err := os.Chown(req.Path, int(req.UID), int(req.GID)); err != nil {
			return errResp(err)
		}
	case FileOpChtimes:
		if err := os.Chtimes(req.Path, time.Unix(0, req.AtimeNanos), time.Unix(0, req.MTimeNanos)); err != nil {
			return errResp(err)
		}
	case FileOpTruncate:
		if err := os.Truncate(req.Path, req.Size); err != nil {
			return errResp(err)
		}
	case FileOpStatfs:
		return handleStatfs(req)
	case FileOpGetwd:
		wd, err := os.Getwd()
		if err != nil {
			return errResp(err)
		}
		resp.Path = wd
	default:
		resp.Errno = uint32(syscall.ENOSYS)
		resp.ErrMsg = "unknown file op"
	}
	return resp
}

// CloseAll releases all open handles. Call this when the file-op
// service loop exits so the agent doesn't leak fds.
func (s *FileOpServer) CloseAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.handles {
		_ = f.Close()
	}
	s.handles = make(map[uint32]*os.File)
}

func (s *FileOpServer) put(f *os.File) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	if s.next == 0 {
		s.next = 1
	}
	id := s.next
	s.handles[id] = f
	return id
}

func (s *FileOpServer) get(h uint32) (*os.File, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.handles[h]
	return f, ok
}

func (s *FileOpServer) take(h uint32) (*os.File, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.handles[h]
	if ok {
		delete(s.handles, h)
	}
	return f, ok
}

// statFromInfo converts an os.FileInfo into the wire FileStat.
func statFromInfo(info os.FileInfo) *FileStat {
	mode := info.Mode()
	if mode.Type() == os.ModeIrregular {
		if sysMode, ok := unixModeFromSys(info.Sys()); ok {
			mode = modeFromUnix(sysMode) | mode.Perm()
		}
	}
	st := &FileStat{
		Name:       info.Name(),
		Size:       info.Size(),
		Mode:       uint32(mode),
		MTimeNanos: info.ModTime().UnixNano(),
		IsDir:      info.IsDir(),
	}
	augmentStatFromSys(st, info.Sys())
	return st
}

// errResp produces a FileOpResp whose Errno and ErrMsg describe err.
func errResp(err error) *FileOpResp {
	resp := &FileOpResp{ErrMsg: err.Error()}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		resp.Errno = uint32(errno)
		return resp
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		resp.Errno = uint32(syscall.ENOENT)
	case errors.Is(err, os.ErrPermission):
		resp.Errno = uint32(syscall.EACCES)
	case errors.Is(err, os.ErrExist):
		resp.Errno = uint32(syscall.EEXIST)
	case errors.Is(err, os.ErrInvalid):
		resp.Errno = uint32(syscall.EINVAL)
	default:
		resp.Errno = uint32(syscall.EIO)
	}
	return resp
}

// Context not used directly here; provided so the dispatcher could grow
// a cancellable Handle in the future.
var _ = context.Background
