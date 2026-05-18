// Package childproc launches and manages a single child process with
// portable plumbing for stdin/stdout/stderr and platform-specific
// behavior for process-group control, signal delivery, and extra file
// descriptors.
package childproc

import (
	"os"
	"os/exec"
)

// Spec describes the command to launch. It is intentionally decoupled
// from any wire protocol so this package can be reused.
type Spec struct {
	Path     string
	Argv     []string
	Env      []string
	Cwd      string
	ExtraFDs []uint32

	// DropSudoCredentials, when true, instructs Build to switch the
	// child's uid/gid to the values in $SUDO_UID / $SUDO_GID if those
	// variables are set. Unix-only; ignored on Windows.
	DropSudoCredentials bool
}

// Pipes holds the parent-side ends of the standard pipes and any
// extra-fd channels connected to the child process. The child-side
// ends live on the *exec.Cmd until Start, after which CloseChildSide
// releases them in the parent.
type Pipes struct {
	Stdin  *os.File            // parent writes → child stdin
	Stdout *os.File            // parent reads ← child stdout
	Stderr *os.File            // parent reads ← child stderr
	Extra  map[uint32]*os.File // parent end of each extra-fd channel
}

// Close releases every file descriptor in p. Safe to call multiple times.
func (p *Pipes) Close() {
	if p == nil {
		return
	}
	if p.Stdin != nil {
		_ = p.Stdin.Close()
		p.Stdin = nil
	}
	if p.Stdout != nil {
		_ = p.Stdout.Close()
		p.Stdout = nil
	}
	if p.Stderr != nil {
		_ = p.Stderr.Close()
		p.Stderr = nil
	}
	for k, f := range p.Extra {
		if f != nil {
			_ = f.Close()
		}
		delete(p.Extra, k)
	}
}

// CloseChildSide releases the child-side ends of the std pipes and
// extra files attached to cmd. Call this once Start has succeeded so
// the child becomes the only holder of its end of each pipe.
func CloseChildSide(cmd *exec.Cmd) {
	if f, ok := cmd.Stdin.(*os.File); ok && f != nil {
		_ = f.Close()
	}
	if f, ok := cmd.Stdout.(*os.File); ok && f != nil {
		_ = f.Close()
	}
	if f, ok := cmd.Stderr.(*os.File); ok && f != nil {
		_ = f.Close()
	}
	for _, f := range cmd.ExtraFiles {
		if f != nil {
			_ = f.Close()
		}
	}
}

// newCmd builds an *exec.Cmd from a Spec without touching SysProcAttr
// or pipes; platform-specific Build wraps this to finish wiring.
func newCmd(spec Spec) *exec.Cmd {
	var cmd *exec.Cmd
	if len(spec.Argv) <= 1 {
		cmd = exec.Command(spec.Path)
	} else {
		cmd = exec.Command(spec.Path, spec.Argv[1:]...)
	}
	cmd.Dir = spec.Cwd
	cmd.Env = spec.Env
	return cmd
}

// setupStdPipes wires std pipes between cmd and the returned Pipes.
func setupStdPipes(cmd *exec.Cmd) (*Pipes, error) {
	pipes := &Pipes{Extra: make(map[uint32]*os.File)}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdin = stdinR
	pipes.Stdin = stdinW

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	cmd.Stdout = stdoutW
	pipes.Stdout = stdoutR

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	cmd.Stderr = stderrW
	pipes.Stderr = stderrR

	return pipes, nil
}
