package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// reapZombies collects zombie child processes. It must be started after
// prctl(PR_SET_CHILD_SUBREAPER) so that orphaned grandchildren are
// reparented to this process.
func reapZombies(ctx context.Context) {
	sigch := make(chan os.Signal, 8)
	signal.Notify(sigch, syscall.SIGCHLD)
	defer signal.Stop(sigch)

	for {
		select {
		case <-ctx.Done():
			// Drain any remaining zombies before returning.
			reapAll()
			return
		case <-sigch:
			reapAll()
		}
	}
}

func reapAll() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
