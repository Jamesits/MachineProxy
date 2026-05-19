//go:build linux || darwin || freebsd

package workspacefs

import (
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

func currentATimeOS(sys any) (time.Time, bool) {
	switch st := sys.(type) {
	case *syscall.Stat_t:
		if st != nil {
			return atimeFromStat(st), true
		}
	case *sftp.FileStat:
		if st != nil && st.Atime != 0 {
			return time.Unix(int64(st.Atime), 0), true
		}
	}
	return time.Time{}, false
}

func currentUIDOS(sys any) (uint32, bool) {
	switch st := sys.(type) {
	case *syscall.Stat_t:
		if st != nil {
			return st.Uid, true
		}
	case *sftp.FileStat:
		if st != nil {
			return st.UID, true
		}
	}
	return 0, false
}

func currentGIDOS(sys any) (uint32, bool) {
	switch st := sys.(type) {
	case *syscall.Stat_t:
		if st != nil {
			return st.Gid, true
		}
	case *sftp.FileStat:
		if st != nil {
			return st.GID, true
		}
	}
	return 0, false
}
