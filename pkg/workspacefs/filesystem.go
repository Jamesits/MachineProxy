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

	"github.com/jamesits/machineproxy/pkg/logging"
)

type RemoteFile interface {
	ReadAt(p []byte, off int64) (int, error)
	Close() error
}

type RemoteWriteFile interface {
	WriteAt(p []byte, off int64) (int, error)
	Close() error
}

// SFTPClient is the subset of pkg/sftp client methods required by the filesystem.
type SFTPClient interface {
	Open(path string) (RemoteFile, error)
	Create(path string) (RemoteWriteFile, error)
	OpenFile(path string, flags int) (RemoteWriteFile, error)
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.FileInfo, error)
	Mkdir(path string) error
	Remove(path string) error
	Rename(oldpath, newpath string) error
	Chmod(path string, mode os.FileMode) error
	Truncate(path string, size int64) error
}

type FileSystem struct {
	root string
	sftp SFTPClient
	log  *slog.Logger
}

func New(sftp SFTPClient, root string, log *slog.Logger) *FileSystem {
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
	defer fh.Close()

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

func (f *FileSystem) Stat(rel string) (os.FileInfo, syscall.Errno) {
	st, err := f.sftp.Stat(f.absPath(rel))
	if err != nil {
		return nil, toErrno(err)
	}
	return st, 0
}

func (f *FileSystem) ReadDir(rel string) ([]os.FileInfo, syscall.Errno) {
	entries, err := f.sftp.ReadDir(f.absPath(rel))
	if err != nil {
		return nil, toErrno(err)
	}
	return entries, 0
}

func (f *FileSystem) WriteFile(ctx context.Context, rel string, data []byte, off int64) (uint32, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse write", "path", rel, "offset", off, "size", len(data))
	abs := f.absPath(rel)
	fh, err := f.sftp.OpenFile(abs, os.O_WRONLY)
	if err != nil {
		f.log.Warn("fuse write open failed", "path", rel, "error", err)
		return 0, toErrno(err)
	}
	defer fh.Close()
	n, err := fh.WriteAt(data, off)
	if err != nil {
		f.log.Warn("fuse write failed", "path", rel, "error", err)
		return uint32(n), toErrno(err)
	}
	return uint32(n), 0
}

func (f *FileSystem) CreateFile(ctx context.Context, rel string) syscall.Errno {
	_ = ctx
	abs := f.absPath(rel)
	fh, err := f.sftp.Create(abs)
	if err != nil {
		return toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("close newly created remote file", "path", abs, "error", cerr)
		return toErrno(cerr)
	}
	return 0
}

func (f *FileSystem) MkDir(ctx context.Context, rel string) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Mkdir(f.absPath(rel)))
}

func (f *FileSystem) Unlink(ctx context.Context, rel string) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Remove(f.absPath(rel)))
}

func (f *FileSystem) Rmdir(ctx context.Context, rel string) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Remove(f.absPath(rel)))
}

func (f *FileSystem) Rename(ctx context.Context, oldRel, newRel string) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Rename(f.absPath(oldRel), f.absPath(newRel)))
}

func (f *FileSystem) Chmod(ctx context.Context, rel string, mode os.FileMode) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Chmod(f.absPath(rel), mode))
}

func (f *FileSystem) Truncate(ctx context.Context, rel string, size int64) syscall.Errno {
	_ = ctx
	return toErrnoE(f.sftp.Truncate(f.absPath(rel), size))
}

func (f *FileSystem) absPath(rel string) string {
	clean := path.Clean("/" + rel)
	if clean == "/" {
		return f.root
	}
	return path.Join(f.root, strings.TrimPrefix(clean, "/"))
}

func toErrno(err error) syscall.Errno {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	default:
		return syscall.EIO
	}
}

func toErrnoE(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	return toErrno(err)
}
