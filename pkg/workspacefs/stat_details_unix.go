//go:build linux || darwin || freebsd

package workspacefs

import (
	"syscall"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"
)

func applyStatDetailsOS(out *fuse.Attr, sys any) {
	switch st := sys.(type) {
	case *syscall.Stat_t:
		if st != nil {
			applySyscallStat(out, st)
		}
	case *sftp.FileStat:
		if st != nil {
			applySFTPStat(out, st)
		}
	}
}

func applySFTPStat(out *fuse.Attr, st *sftp.FileStat) {
	out.Uid = st.UID
	out.Gid = st.GID
	if st.Atime != 0 {
		out.Atime = uint64(st.Atime)
		out.Atimensec = 0
	}
	if st.Mtime != 0 {
		out.Mtime = uint64(st.Mtime)
		out.Mtimensec = 0
	}
}
