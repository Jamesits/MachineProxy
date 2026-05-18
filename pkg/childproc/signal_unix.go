//go:build unix

package childproc

import (
	"os"
	"os/exec"
	"syscall"
)

// ExitCode extracts the exit code from a finished process state.
// Returns 128+signal when the process was killed by a signal.
func ExitCode(state *os.ProcessState) int {
	ws, ok := state.Sys().(syscall.WaitStatus)
	if !ok {
		return state.ExitCode()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

// SignalGroup sends sig (a unix signal number) to the child's process
// group. Falls back to signaling the lead process if the pgid lookup
// fails, e.g. because the child has already exited.
func SignalGroup(cmd *exec.Cmd, sig int) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Signal(syscall.Signal(sig))
	}
	return syscall.Kill(-pgid, syscall.Signal(sig))
}
