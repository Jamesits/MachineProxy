package workspacefs

import (
	"context"
	"os"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func access(ctx context.Context, st os.FileInfo, mask uint32) syscall.Errno {
	if mask == 0 {
		return 0
	}
	fuseCtx, _ := ctx.(*fuse.Context)
	if fuseCtx != nil && fuseCtx.Uid == 0 {
		if mask&fuse.X_OK != 0 && st.Mode().Perm()&0o111 == 0 {
			return syscall.EACCES
		}
		return 0
	}
	perm := uint32(st.Mode().Perm())
	shift := uint(0)
	uid, gid := currentUID(st.Sys()), currentGID(st.Sys())
	if fuseCtx != nil {
		switch {
		case fuseCtx.Uid == uid:
			shift = 6
		case fuseCtx.Gid == gid:
			shift = 3
		}
	}
	allowed := (perm >> shift) & 0o7
	const (
		rOK = 4
		wOK = 2
		xOK = 1
	)
	if mask&rOK != 0 && allowed&0o4 == 0 {
		return syscall.EACCES
	}
	if mask&wOK != 0 && allowed&0o2 == 0 {
		return syscall.EACCES
	}
	if mask&xOK != 0 && allowed&0o1 == 0 {
		return syscall.EACCES
	}
	return 0
}
