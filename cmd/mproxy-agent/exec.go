package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

// childPipes holds the agent-side ends of pipes/socketpairs connected
// to the child process.
type childPipes struct {
	stdin  *os.File            // write end → child stdin
	stdout *os.File            // read end ← child stdout
	stderr *os.File            // read end ← child stderr
	extra  map[uint32]*os.File // read/write end for each extra fd
}

// buildCmd creates an *exec.Cmd from the ExecMsg. It sets up:
//   - New process group (Setpgid)
//   - Pipes for stdin/stdout/stderr
//   - Socketpairs for each requested extra fd
//
// The caller must close the returned childPipes when done.
func buildCmd(msg *agentproto.ExecMsg) (*exec.Cmd, *childPipes, error) {
	cmd := exec.Command(msg.Path, msg.Argv[1:]...)
	if len(msg.Argv) <= 1 {
		cmd = exec.Command(msg.Path)
	}
	cmd.Dir = msg.Cwd
	cmd.Env = msg.Env
	sysProcAttr := &syscall.SysProcAttr{
		Setpgid: true,
	}

	// If running under sudo, drop privileges to the original user.
	if sudoUID := os.Getenv("SUDO_UID"); sudoUID != "" {
		uid, err := strconv.ParseUint(sudoUID, 10, 32)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid SUDO_UID %q: %w", sudoUID, err)
		}
		gid := uid // fallback: use UID as GID
		if sudoGID := os.Getenv("SUDO_GID"); sudoGID != "" {
			g, err := strconv.ParseUint(sudoGID, 10, 32)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid SUDO_GID %q: %w", sudoGID, err)
			}
			gid = g
		}
		sysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(uid),
			Gid: uint32(gid),
		}
	}

	cmd.SysProcAttr = sysProcAttr

	pipes := &childPipes{
		extra: make(map[uint32]*os.File),
	}

	// stdin: pipe, agent writes to w, child reads from r.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}
	cmd.Stdin = stdinR
	pipes.stdin = stdinW

	// stdout: pipe, child writes to w, agent reads from r.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stdout = stdoutW
	pipes.stdout = stdoutR

	// stderr: pipe, child writes to w, agent reads from r.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	cmd.Stderr = stderrW
	pipes.stderr = stderrR

	// Extra fds: create a Unix socketpair for each. One end goes to the
	// child as the requested fd number (via ExtraFiles), the other stays
	// with the agent for bidirectional bridging.
	for _, fdNum := range msg.ExtraFDs {
		agentEnd, childEnd, err := socketpair()
		if err != nil {
			closePipes(pipes)
			return nil, nil, fmt.Errorf("socketpair for fd %d: %w", fdNum, err)
		}
		// ExtraFiles[i] becomes fd 3+i in the child. We need to arrange
		// the slice so that fdNum maps correctly. For simplicity, pad
		// the slice up to the needed index.
		idx := int(fdNum) - 3
		for len(cmd.ExtraFiles) <= idx {
			cmd.ExtraFiles = append(cmd.ExtraFiles, nil)
		}
		cmd.ExtraFiles[idx] = childEnd
		pipes.extra[fdNum] = agentEnd
	}

	return cmd, pipes, nil
}

// closeChildFDs closes the child-side file descriptors after Start().
// These must be closed in the agent so the child is the only holder.
func closeChildFDs(cmd *exec.Cmd) {
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

func closePipes(p *childPipes) {
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.stdout != nil {
		_ = p.stdout.Close()
	}
	if p.stderr != nil {
		_ = p.stderr.Close()
	}
	for _, f := range p.extra {
		_ = f.Close()
	}
}

func socketpair() (agent *os.File, child *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	agent = os.NewFile(uintptr(fds[0]), "socketpair-agent")
	child = os.NewFile(uintptr(fds[1]), "socketpair-child")

	// Clear CLOEXEC on the child end so it survives exec.
	if err := clearCloexec(fds[1]); err != nil {
		_ = agent.Close()
		_ = child.Close()
		return nil, nil, err
	}
	return agent, child, nil
}

func clearCloexec(fd int) error {
	flags, err := fcntlGetFD(fd)
	if err != nil {
		return err
	}
	_, err = fcntlSetFD(fd, flags&^syscall.FD_CLOEXEC)
	return err
}

func fcntlGetFD(fd int) (int, error) {
	val, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(val), nil
}

func fcntlSetFD(fd int, flags int) (int, error) {
	val, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, uintptr(flags))
	if errno != 0 {
		return 0, errno
	}
	return int(val), nil
}

// exitCode extracts the exit code from a process state.
// Returns 128+signal if the process was killed by a signal.
func exitCode(state *os.ProcessState) int {
	ws := state.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

// sendSignalToGroup sends a signal to the child's process group.
func sendSignalToGroup(cmd *exec.Cmd, sig int) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Fallback: send to process directly.
		return cmd.Process.Signal(signalFromInt(sig))
	}
	return syscall.Kill(-pgid, syscall.Signal(sig))
}

func signalFromInt(sig int) os.Signal {
	return syscall.Signal(sig)
}
