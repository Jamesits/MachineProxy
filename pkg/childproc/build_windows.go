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
//
// When spec.DropSudoCredentials is true and the current process token
// is elevated under UAC, the child is launched with the linked
// filtered (standard-user) token via CreateProcessAsUser. If the
// current token is not elevated there is nothing to drop and the
// child inherits the parent's token as usual.
func Build(spec Spec) (*exec.Cmd, *Pipes, error) {
	if len(spec.ExtraFDs) > 0 {
		return nil, nil, fmt.Errorf("extra file descriptors are not supported on windows")
	}

	cmd := newCmd(spec)
	sysProcAttr := &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}

	if spec.DropSudoCredentials {
		token, err := unprivilegedPrimaryToken()
		if err != nil {
			return nil, nil, fmt.Errorf("drop credentials: %w", err)
		}
		if token != 0 {
			// The handle is consumed by CreateProcessAsUser when cmd
			// starts; Go's syscall layer does not close it for us, so
			// it is intentionally leaked for the lifetime of this
			// process. Build is called once per agent run.
			sysProcAttr.Token = syscall.Token(token)
		}
	}

	cmd.SysProcAttr = sysProcAttr

	pipes, err := setupStdPipes(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("std pipes: %w", err)
	}

	return cmd, pipes, nil
}

// unprivilegedPrimaryToken returns a primary token representing the
// current user with elevation removed, or 0 if the current process is
// not elevated (in which case the caller should leave the child to
// inherit the parent's token).
func unprivilegedPrimaryToken() (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE,
		&current,
	); err != nil {
		return 0, fmt.Errorf("open current process token: %w", err)
	}
	defer func() { _ = current.Close() }()

	if !current.IsElevated() {
		return 0, nil
	}

	// On a UAC split-token admin logon, TokenLinkedToken yields the
	// filtered (standard-user) token. It is an impersonation token;
	// CreateProcessAsUser requires a primary token, so duplicate it.
	linked, err := current.GetLinkedToken()
	if err != nil {
		return 0, fmt.Errorf("get linked token: %w", err)
	}
	defer func() { _ = linked.Close() }()

	var primary windows.Token
	if err := windows.DuplicateTokenEx(
		linked,
		0, // request maximum allowed access
		nil,
		windows.SecurityImpersonation,
		windows.TokenPrimary,
		&primary,
	); err != nil {
		return 0, fmt.Errorf("duplicate primary token: %w", err)
	}
	return primary, nil
}
