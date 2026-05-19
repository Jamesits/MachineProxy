package workspacefs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/remote"
)

// FileSystem backs a FUSE mount with a remote.FileClient. Each op is
// translated into the corresponding FileClient method call.
type FileSystem struct {
	root string
	sftp remote.FileClient
	log  *slog.Logger
}

func New(sftp remote.FileClient, root string, log *slog.Logger) *FileSystem {
	cleanRoot := path.Clean(root)
	if cleanRoot == "." {
		cleanRoot = "/"
	}
	if log == nil {
		log = slog.Default()
	}
	return &FileSystem{root: cleanRoot, sftp: sftp, log: log}
}

func (f *FileSystem) ReadFile(ctx context.Context, rel string, off int64, size int) ([]byte, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse read", "path", rel, "offset", off, "size", size)

	abs := f.absPath(rel)
	fh, err := f.sftp.Open(abs)
	if err != nil {
		f.log.Warn("fuse read open failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	defer func() {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse read close failed", "path", rel, "error", cerr)
		}
	}()

	if size <= 0 {
		return []byte{}, 0
	}

	buf := make([]byte, size)
	n, err := fh.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		f.log.Warn("fuse read failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return buf[:n], 0
}

func (f *FileSystem) Stat(ctx context.Context, rel string) (os.FileInfo, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse stat", "path", rel)
	st, err := f.sftp.Lstat(f.absPath(rel))
	if err != nil {
		// ENOENT is the normal answer to "does this exist?" probes
		// during Lookup; keep it visible without flooding warning logs.
		if errors.Is(err, os.ErrNotExist) {
			f.log.Debug("fuse stat missing", "path", rel, "error", err)
		} else {
			f.log.Warn("fuse stat failed", "path", rel, "error", err)
		}
		return nil, toErrno(err)
	}
	return st, 0
}

func (f *FileSystem) ReadDir(ctx context.Context, rel string) ([]os.FileInfo, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse readdir", "path", rel)
	entries, err := f.sftp.ReadDir(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse readdir failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return entries, 0
}

func (f *FileSystem) Readlink(ctx context.Context, rel string) ([]byte, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse readlink", "path", rel)
	target, err := f.sftp.Readlink(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse readlink failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return []byte(target), 0
}

func (f *FileSystem) WriteFile(ctx context.Context, rel string, data []byte, off int64) (uint32, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse write", "path", rel, "offset", off, "size", len(data))
	abs := f.absPath(rel)
	fh, err := f.sftp.OpenFile(abs, os.O_WRONLY, 0)
	if err != nil {
		f.log.Warn("fuse write open failed", "path", rel, "error", err)
		return 0, toErrno(err)
	}
	n, err := fh.WriteAt(data, off)
	if err != nil {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse write close failed after write error", "path", rel, "error", cerr)
		}
		f.log.Warn("fuse write failed", "path", rel, "error", err)
		return uint32(n), toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse write close failed", "path", rel, "error", cerr)
		return uint32(n), toErrno(cerr)
	}
	return uint32(n), 0
}

func (f *FileSystem) CreateFile(ctx context.Context, rel string, flags uint32, mode uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse create", "path", rel, "flags", flags, "mode", mode)
	abs := f.absPath(rel)
	openFlags := int(flags) | os.O_CREATE
	fh, err := f.sftp.OpenFile(abs, openFlags, posixFileMode(mode))
	if err != nil {
		f.log.Warn("fuse create failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse create close failed", "path", rel, "error", cerr)
		return toErrno(cerr)
	}
	return 0
}

func (f *FileSystem) MkDir(ctx context.Context, rel string, mode uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse mkdir", "path", rel, "mode", mode)
	if err := f.sftp.Mkdir(f.absPath(rel), posixFileMode(mode)); err != nil {
		f.log.Warn("fuse mkdir failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Unlink(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse unlink", "path", rel)
	st, errno := f.Stat(ctx, rel)
	if errno != 0 {
		return errno
	}
	if st.IsDir() {
		f.log.Warn("fuse unlink rejected directory", "path", rel)
		return syscall.EISDIR
	}
	if err := f.sftp.Remove(f.absPath(rel)); err != nil {
		f.log.Warn("fuse unlink failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Rmdir(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse rmdir", "path", rel)
	st, errno := f.Stat(ctx, rel)
	if errno != 0 {
		return errno
	}
	if !st.IsDir() {
		f.log.Warn("fuse rmdir rejected non-directory", "path", rel)
		return syscall.ENOTDIR
	}
	if err := f.sftp.Remove(f.absPath(rel)); err != nil {
		f.log.Warn("fuse rmdir failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Rename(ctx context.Context, oldRel, newRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse rename", "from", oldRel, "to", newRel)
	if err := f.sftp.Rename(f.absPath(oldRel), f.absPath(newRel)); err != nil {
		f.log.Warn("fuse rename failed", "from", oldRel, "to", newRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Symlink(ctx context.Context, target, linkRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse symlink", "target", target, "link", linkRel)
	if err := f.sftp.Symlink(target, f.absPath(linkRel)); err != nil {
		f.log.Warn("fuse symlink failed", "target", target, "link", linkRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Link(ctx context.Context, oldRel, newRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse link", "from", oldRel, "to", newRel)
	if err := f.sftp.Link(f.absPath(oldRel), f.absPath(newRel)); err != nil {
		f.log.Warn("fuse link failed", "from", oldRel, "to", newRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Chmod(ctx context.Context, rel string, mode os.FileMode) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chmod", "path", rel, "mode", mode)
	if err := f.sftp.Chmod(f.absPath(rel), mode); err != nil {
		f.log.Warn("fuse chmod failed", "path", rel, "mode", mode, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Chown(ctx context.Context, rel string, uid, gid uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chown", "path", rel, "uid", uid, "gid", gid)
	if err := f.sftp.Chown(f.absPath(rel), int(uid), int(gid)); err != nil {
		f.log.Warn("fuse chown failed", "path", rel, "uid", uid, "gid", gid, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Chtimes(ctx context.Context, rel string, atime, mtime time.Time) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chtimes", "path", rel, "atime", atime, "mtime", mtime)
	if err := f.sftp.Chtimes(f.absPath(rel), atime, mtime); err != nil {
		f.log.Warn("fuse chtimes failed", "path", rel, "atime", atime, "mtime", mtime, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Truncate(ctx context.Context, rel string, size int64) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse truncate", "path", rel, "size", size)
	if err := f.sftp.Truncate(f.absPath(rel), size); err != nil {
		f.log.Warn("fuse truncate failed", "path", rel, "size", size, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Statfs(ctx context.Context, rel string, out *remote.Statfs) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse statfs", "path", rel)
	st, err := f.sftp.Statfs(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse statfs failed", "path", rel, "error", err)
		return toErrno(err)
	}
	*out = *st
	return 0
}

// Fsync opens the remote file fresh, asks the backend to flush it to
// stable storage, then closes the handle. Each FUSE write already opens
// and closes its own handle, so a per-request handle here is enough to
// reach the underlying inode; the kernel/server flushes pending writes
// regardless of which fd issued them.
//
// Backends whose protocol cannot express fsync are expected to make
// Sync a successful no-op; real open/sync failures are returned to the
// kernel so callers see the same durability errors they would on a local
// filesystem.
func (f *FileSystem) Fsync(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse fsync", "path", rel)
	abs := f.absPath(rel)
	fh, err := f.sftp.OpenFile(abs, os.O_RDONLY, 0)
	if err != nil {
		f.log.Warn("fuse fsync open failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if err := fh.Sync(); err != nil {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse fsync close failed after sync error", "path", rel, "error", cerr)
		}
		f.log.Warn("fuse fsync failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse fsync close failed", "path", rel, "error", cerr)
		return toErrno(cerr)
	}
	return 0
}

func (f *FileSystem) absPath(rel string) string {
	clean := path.Clean("/" + rel)
	if clean == "/" {
		return f.root
	}
	return path.Join(f.root, strings.TrimPrefix(clean, "/"))
}

func posixFileMode(mode uint32) os.FileMode {
	out := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		out |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		out |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		out |= os.ModeSticky
	}
	return out
}

func toErrno(err error) syscall.Errno {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if errors.As(pathErr.Err, &errno) {
			return errno
		}
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrInvalid):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
