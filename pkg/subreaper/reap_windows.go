//go:build windows

package subreaper

import "context"

// Reap is a no-op on Windows: there is no SIGCHLD or wait4 analogue,
// exec.Cmd reaps direct children on its own, and the kill-on-close
// Job Object from Setup terminates any remaining descendants when
// this process exits.
func Reap(ctx context.Context) {
	<-ctx.Done()
}
