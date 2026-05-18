//go:build unix

package subreaper

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Reap collects zombie child processes for the lifetime of ctx. It
// listens for SIGCHLD and drains all finished children on each tick.
// When run after Setup it also reaps descendants reparented to us via
// subreaper / procctl, keeping the process table clean.
func Reap(ctx context.Context) {
	sigch := make(chan os.Signal, 8)
	signal.Notify(sigch, syscall.SIGCHLD)
	defer signal.Stop(sigch)

	for {
		select {
		case <-ctx.Done():
			reapAll() // final drain
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
