//go:build linux

package subreaper

import "golang.org/x/sys/unix"

// Setup makes the current process the "child subreaper" so that
// double-forked grandchildren are reparented to us instead of pid 1.
// Combined with Reap this keeps the process table clean.
func Setup() error {
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}
