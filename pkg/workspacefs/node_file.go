package workspacefs

import (
	"context"
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type fileNode struct {
	fs.Inode
	backend *FileSystem
	relPath string
}

func (n *fileNode) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	st, errno := n.backend.Stat(n.relPath)
	if errno != 0 {
		return errno
	}
	applyFileInfo(&out.Attr, st)
	if out.Mode&uint32(syscall.S_IFMT) == 0 {
		out.Mode = (out.Mode &^ uint32(syscall.S_IFMT)) | fuse.S_IFREG
	}
	return 0
}

func (n *fileNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	_ = ctx
	_ = flags
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *fileNode) Read(ctx context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, errno := n.backend.ReadFile(ctx, n.relPath, off, len(dest))
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(data), 0
}

func (n *fileNode) Write(ctx context.Context, _ fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	return n.backend.WriteFile(ctx, n.relPath, data, off)
}

func (n *fileNode) Setattr(ctx context.Context, _ fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if mode, ok := in.GetMode(); ok {
		if errno := n.backend.Chmod(ctx, n.relPath, os.FileMode(mode)); errno != 0 {
			return errno
		}
	}
	if sz, ok := in.GetSize(); ok {
		if errno := n.backend.Truncate(ctx, n.relPath, int64(sz)); errno != 0 {
			return errno
		}
	}
	return n.Getattr(ctx, nil, out)
}
