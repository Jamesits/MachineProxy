//go:build freebsd

package subreaper

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// FreeBSD sys/procctl.h constants — not exposed by golang.org/x/sys/unix.
const (
	procP_PID       = 0
	procReapAcquire = 2
)

// Setup makes the current process the reaper for its descendants via
// procctl(PROC_REAP_ACQUIRE) — the FreeBSD analogue of Linux's
// PR_SET_CHILD_SUBREAPER prctl call.
func Setup() error {
	_, _, errno := syscall.Syscall6(
		unix.SYS_PROCCTL,
		uintptr(procP_PID),
		0,
		uintptr(procReapAcquire),
		0, 0, 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
