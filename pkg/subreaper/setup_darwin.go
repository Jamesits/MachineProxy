//go:build darwin

package subreaper

// Setup is a no-op on macOS: there is no PR_SET_CHILD_SUBREAPER nor
// procctl(PROC_REAP_ACQUIRE), and orphaned descendants reparent to
// launchd instead of this process.
func Setup() error {
	return nil
}
