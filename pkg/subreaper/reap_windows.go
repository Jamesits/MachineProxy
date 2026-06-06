//go:build windows

package subreaper

import (
	"context"
	"os/exec"

	"github.com/jamesits/machineproxy/pkg/childproc"
)

// Reaper is a no-op collector on Windows: there is no SIGCHLD or wait4
// analogue, exec.Cmd reaps direct children on its own, and the
// kill-on-close Job Object from Setup terminates any remaining descendants
// when this process exits.
type Reaper struct{}

// NewReaper returns a ready-to-run Reaper.
func NewReaper() *Reaper { return &Reaper{} }

// Run blocks until ctx is cancelled; there is nothing to reap.
func (r *Reaper) Run(ctx context.Context) { <-ctx.Done() }

// WaitFor waits for cmd via (*exec.Cmd).Wait — safe on Windows because no
// competing reaper exists — and returns its conventional exit code.
func (r *Reaper) WaitFor(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 127
	}
	_ = cmd.Wait()
	if cmd.ProcessState == nil {
		return 127
	}
	return childproc.ExitCode(cmd.ProcessState)
}
