//go:build linux

package workspacefs

import (
	"context"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func (n *dirNode) Statx(ctx context.Context, f fs.FileHandle, flags uint32, mask uint32, out *fuse.StatxOut) syscall.Errno {
	var attr fuse.AttrOut
	if errno := n.Getattr(ctx, f, &attr); errno != 0 {
		return errno
	}
	out.Mask = mask
	out.Blksize = attr.Blksize
	out.Nlink = attr.Nlink
	out.Uid = attr.Uid
	out.Gid = attr.Gid
	out.Mode = uint16(attr.Mode)
	out.Ino = attr.Ino
	out.Size = attr.Size
	out.Blocks = attr.Blocks
	out.Atime.Sec = attr.Atime
	out.Atime.Nsec = attr.Atimensec
	out.Mtime.Sec = attr.Mtime
	out.Mtime.Nsec = attr.Mtimensec
	out.Ctime.Sec = attr.Ctime
	out.Ctime.Nsec = attr.Ctimensec
	n.backend.log.Log(ctx, traceLevel(), "fuse statx", "path", n.relPath, "flags", flags, "mask", mask)
	return 0
}
