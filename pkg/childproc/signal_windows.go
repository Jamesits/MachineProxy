//go:build windows

package childproc

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

// Unix signal numbers honored when received from a wire protocol.
const (
	unixSIGHUP  = 1
	unixSIGINT  = 2
	unixSIGQUIT = 3
	unixSIGKILL = 9
	unixSIGTERM = 15
)

// ExitCode extracts the exit code from a finished process state.
// Windows reports no signal information for completed processes.
func ExitCode(state *os.ProcessState) int {
	return state.ExitCode()
}

// SignalGroup translates a unix signal number into the closest
// Windows equivalent and delivers it to the child:
//
//   - SIGINT/SIGQUIT/SIGHUP → CTRL_BREAK_EVENT to the process group
//     (the child must have been started with CREATE_NEW_PROCESS_GROUP).
//   - Anything else → TerminateProcess with code 128+sig.
func SignalGroup(cmd *exec.Cmd, sig int) error {
	if cmd.Process == nil {
		return nil
	}

	switch sig {
	case unixSIGINT, unixSIGQUIT, unixSIGHUP:
		return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
	default:
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err != nil {
			return err
		}
		defer func() { _ = windows.CloseHandle(h) }()
		return windows.TerminateProcess(h, uint32(128+sig))
	}
}
