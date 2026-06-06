package workspacefs

import (
	"context"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/remote"
)

type fileNode struct {
	fs.Inode
	backend *FileSystem
	relPath string
}

type fileHandle struct {
	read  remote.RemoteFile
	write remote.RemoteWriteFile
	flags uint32
}

func (n *fileNode) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*fileHandle); ok && h.write != nil {
		st, err := h.write.Stat()
		if err != nil {
			return toErrno(err)
		}
		n.backend.applyAttr(&out.Attr, st)
		return 0
	}
	st, errno := n.backend.Stat(ctx, n.relPath)
	if errno != 0 {
		return errno
	}
	n.backend.applyAttr(&out.Attr, st)
	if out.Mode&uint32(syscall.S_IFMT) == 0 {
		out.Mode = (out.Mode &^ uint32(syscall.S_IFMT)) | fuse.S_IFREG
	}
	return 0
}

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	accmode := int(flags) & syscall.O_ACCMODE
	h := &fileHandle{flags: flags}
	if accmode == os.O_RDONLY {
		fh, err := n.backend.files.Open(n.backend.absPath(n.relPath))
		if err != nil {
			return nil, 0, toErrno(err)
		}
		h.read = fh
	} else {
		fh, err := n.backend.files.OpenFile(n.backend.absPath(n.relPath), int(flags), 0)
		if err != nil {
			return nil, 0, toErrno(err)
		}
		h.write = fh
	}
	return h, fuse.FOPEN_DIRECT_IO, 0
}

func (n *fileNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	st, errno := n.backend.Stat(ctx, n.relPath)
	if errno != 0 {
		return nil, errno
	}
	if st.Mode()&os.ModeSymlink == 0 {
		n.backend.log.Warn("fuse readlink on non-symlink", "path", n.relPath, "mode", st.Mode())
		return nil, syscall.EINVAL
	}
	return n.backend.Readlink(ctx, n.relPath)
}

func (n *fileNode) Read(ctx context.Context, fh fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h, _ := fh.(*fileHandle)
	if h != nil {
		var reader interface {
			ReadAt([]byte, int64) (int, error)
		}
		if h.write != nil {
			reader = h.write
		} else if h.read != nil {
			reader = h.read
		}
		if reader != nil {
			if len(dest) == 0 {
				return fuse.ReadResultData([]byte{}), 0
			}
			buf := make([]byte, len(dest))
			n, err := reader.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, toErrno(err)
			}
			return fuse.ReadResultData(buf[:n]), 0
		}
	}
	data, errno := n.backend.ReadFile(ctx, n.relPath, off, len(dest))
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(data), 0
}

func (n *fileNode) Write(ctx context.Context, fh fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if h, ok := fh.(*fileHandle); ok && h.write != nil {
		var n int
		var err error
		if h.flags&uint32(os.O_APPEND) != 0 {
			n, err = h.write.Write(data)
		} else {
			n, err = h.write.WriteAt(data, off)
		}
		if err != nil {
			return uint32(n), toErrno(err)
		}
		return uint32(n), 0
	}
	return n.backend.WriteFile(ctx, n.relPath, data, off)
}

// Fsync satisfies fs.NodeFsyncer so the FUSE bridge stops returning
// ENOTSUP for fsync calls on workspace files. Without this method,
// go-fuse's rawBridge.Fsync falls through to a hardcoded fuse.ENOTSUP,
// which surfaces in userspace as "operation not supported" — breaking
// Node.js' fs.writeFile when it calls fsync as a durability hint after
// a successful write.
func (n *fileNode) Fsync(ctx context.Context, fh fs.FileHandle, _ uint32) syscall.Errno {
	if h, ok := fh.(*fileHandle); ok {
		if h.write != nil {
			if err := h.write.Sync(); err != nil {
				return toErrno(err)
			}
			return 0
		}
		if h.read != nil {
			if err := h.read.Sync(); err != nil {
				return toErrno(err)
			}
			return 0
		}
	}
	return n.backend.Fsync(ctx, n.relPath)
}

func (h *fileHandle) Release(context.Context) syscall.Errno {
	var errno syscall.Errno
	if h.read != nil {
		if err := h.read.Close(); err != nil {
			errno = toErrno(err)
		}
	}
	if h.write != nil {
		if err := h.write.Close(); err != nil && errno == 0 {
			errno = toErrno(err)
		}
	}
	return errno
}

func (n *fileNode) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	const supported = fuse.FATTR_MODE | fuse.FATTR_SIZE | fuse.FATTR_UID | fuse.FATTR_GID |
		fuse.FATTR_ATIME | fuse.FATTR_MTIME | fuse.FATTR_ATIME_NOW | fuse.FATTR_MTIME_NOW |
		fuse.FATTR_FH | fuse.FATTR_LOCKOWNER | fuse.FATTR_KILL_SUIDGID
	if unsupported := in.Valid &^ supported; unsupported != 0 {
		n.backend.log.Warn("fuse setattr with unsupported attributes", "path", n.relPath, "valid", in.Valid, "unsupported", unsupported)
		return syscall.ENOTSUP
	}
	if uid, uok := in.GetUID(); uok {
		gid, gok := in.GetGID()
		if !gok {
			st, errno := n.backend.Stat(ctx, n.relPath)
			if errno != 0 {
				return errno
			}
			gid = currentGID(st.Sys())
		}
		if errno := n.backend.Chown(ctx, n.relPath, uid, gid); errno != 0 {
			return errno
		}
	} else if gid, ok := in.GetGID(); ok {
		st, errno := n.backend.Stat(ctx, n.relPath)
		if errno != 0 {
			return errno
		}
		if errno := n.backend.Chown(ctx, n.relPath, currentUID(st.Sys()), gid); errno != 0 {
			return errno
		}
	}
	if mode, ok := in.GetMode(); ok {
		if errno := n.backend.Chmod(ctx, n.relPath, posixFileMode(mode)); errno != 0 {
			return errno
		}
	}
	if atime, aok := in.GetATime(); aok {
		mtime, mok := in.GetMTime()
		if !mok {
			st, errno := n.backend.Stat(ctx, n.relPath)
			if errno != 0 {
				return errno
			}
			mtime = st.ModTime()
		}
		if errno := n.backend.Chtimes(ctx, n.relPath, atime, mtime); errno != 0 {
			return errno
		}
	} else if mtime, ok := in.GetMTime(); ok {
		st, errno := n.backend.Stat(ctx, n.relPath)
		if errno != 0 {
			return errno
		}
		if errno := n.backend.Chtimes(ctx, n.relPath, currentATime(st), mtime); errno != 0 {
			return errno
		}
	}
	if sz, ok := in.GetSize(); ok {
		if h, ok := fh.(*fileHandle); ok && h.write != nil {
			if err := h.write.Truncate(int64(sz)); err != nil {
				return toErrno(err)
			}
		} else {
			if errno := n.backend.Truncate(ctx, n.relPath, int64(sz)); errno != 0 {
				return errno
			}
		}
	}
	return n.Getattr(ctx, fh, out)
}

func (n *fileNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	st, errno := n.backend.Stat(ctx, n.relPath)
	if errno != 0 {
		return errno
	}
	if errno := access(ctx, n.backend, st, mask); errno != 0 {
		n.backend.log.Debug("fuse access denied", "path", n.relPath, "mask", mask, "mode", st.Mode(), "error", errno)
		return errno
	}
	return 0
}

func (n *fileNode) Getxattr(_ context.Context, attr string, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("fuse getxattr unsupported", "path", n.relPath, "attr", attr)
	return 0, fs.ENOATTR
}

func (n *fileNode) Listxattr(_ context.Context, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("fuse listxattr unsupported", "path", n.relPath)
	return 0, 0
}

func (n *fileNode) Setxattr(_ context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	n.backend.log.Warn("fuse setxattr unsupported", "path", n.relPath, "attr", attr, "size", len(data), "flags", flags)
	return fs.ENOATTR
}

func (n *fileNode) Removexattr(_ context.Context, attr string) syscall.Errno {
	n.backend.log.Warn("fuse removexattr unsupported", "path", n.relPath, "attr", attr)
	return fs.ENOATTR
}

func (n *fileNode) Allocate(_ context.Context, _ fs.FileHandle, off uint64, size uint64, mode uint32) syscall.Errno {
	n.backend.log.Warn("fuse fallocate unsupported", "path", n.relPath, "offset", off, "size", size, "mode", mode)
	return syscall.ENOTSUP
}

func (n *fileNode) CopyFileRange(_ context.Context, _ fs.FileHandle, offIn uint64, out *fs.Inode, _ fs.FileHandle, offOut uint64, length uint64, flags uint64) (uint32, syscall.Errno) {
	n.backend.log.Warn("fuse copy_file_range unsupported", "path", n.relPath, "target", out, "off_in", offIn, "off_out", offOut, "length", length, "flags", flags)
	return 0, syscall.ENOTSUP
}

func (n *fileNode) Getlk(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) syscall.Errno {
	n.backend.log.Warn("fuse getlk unsupported", "path", n.relPath, "owner", owner, "lock", lk, "flags", flags, "out", out)
	return syscall.ENOTSUP
}

func (n *fileNode) Setlk(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno {
	n.backend.log.Warn("fuse setlk unsupported", "path", n.relPath, "owner", owner, "lock", lk, "flags", flags)
	return syscall.ENOTSUP
}

func (n *fileNode) Setlkw(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno {
	n.backend.log.Warn("fuse setlkw unsupported", "path", n.relPath, "owner", owner, "lock", lk, "flags", flags)
	return syscall.ENOTSUP
}

func (n *fileNode) Ioctl(_ context.Context, _ fs.FileHandle, cmd uint32, arg uint64, input []byte, output []byte) (int32, syscall.Errno) {
	n.backend.log.Warn("fuse ioctl unsupported", "path", n.relPath, "cmd", cmd, "arg", arg, "input_size", len(input), "output_size", len(output))
	return 0, syscall.ENOTTY
}
