//go:build freebsd

package workspacefs

import (
	"syscall"
	"time"
)

func atimeFromStat(st *syscall.Stat_t) time.Time {
	return time.Unix(0, st.Atimespec.Nano())
}
