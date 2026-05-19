//go:build darwin

package workspacefs

import (
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func applySyscallStat(out *fuse.Attr, st *syscall.Stat_t) {
	out.Uid = st.Uid
	out.Gid = st.Gid
	out.Nlink = uint32(st.Nlink)
	out.Rdev = uint32(st.Rdev)
	out.Blocks = uint64(st.Blocks)
	out.Blksize = uint32(st.Blksize)
	out.Ino = st.Ino
	setFuseTime(&out.Atime, &out.Atimensec, time.Unix(0, st.Atimespec.Nano()))
	setFuseTime(&out.Mtime, &out.Mtimensec, time.Unix(0, st.Mtimespec.Nano()))
	setFuseTime(&out.Ctime, &out.Ctimensec, time.Unix(0, st.Ctimespec.Nano()))
}
