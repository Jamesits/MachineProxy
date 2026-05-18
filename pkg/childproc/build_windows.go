//go:build windows

package childproc

import (
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Build creates an *exec.Cmd from spec.
//
// Windows limitations relative to the unix build:
//   - Extra file descriptors are not supported; Build returns an error
//     if spec.ExtraFDs is non-empty.
//   - There is no process group / pgid concept; the child is started
//     in a new console process group so CTRL_BREAK_EVENT can be
//     delivered without affecting the parent. Tree cleanup is the
//     responsibility of a Job Object set up elsewhere.
//   - spec.DropSudoCredentials is ignored.
func Build(spec Spec) (*exec.Cmd, *Pipes, error) {
	if len(spec.ExtraFDs) > 0 {
		return nil, nil, fmt.Errorf("extra file descriptors are not supported on windows")
	}

	cmd := newCmd(spec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}

	pipes, err := setupStdPipes(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("std pipes: %w", err)
	}

	return cmd, pipes, nil
}
