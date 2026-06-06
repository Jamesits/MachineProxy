//go:build unix

package subreaper

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/childproc"
)

// Reaper collects finished child processes for the lifetime of a context.
//
// A process that uses os/exec cannot also run a naive wait4(-1) loop: the
// loop races (*exec.Cmd).Wait over the same zombie. Whichever wins reaps
// the child; the loser gets ECHILD and reports a bogus result (we observed
// a successfully-started command surfacing as exit code 127). Reaper avoids
// the race by being the single waiter for the whole process: foreground
// children registered with WaitFor get their real status delivered, while
// every other (reparented orphan) child is silently drained.
type Reaper struct {
	mu      sync.Mutex
	watched map[int]chan syscall.WaitStatus
	pending map[int]syscall.WaitStatus // reaped before WaitFor registered
}

// NewReaper returns a ready-to-run Reaper. Pair it with Setup (to acquire
// subreaper status) and a Run goroutine.
func NewReaper() *Reaper {
	return &Reaper{
		watched: make(map[int]chan syscall.WaitStatus),
		pending: make(map[int]syscall.WaitStatus),
	}
}

// Run reaps children until ctx is cancelled. It listens for SIGCHLD and
// drains all finished children on each tick, plus once more on shutdown.
// When run after Setup it also reaps descendants reparented to us via
// subreaper / procctl, keeping the process table clean.
func (r *Reaper) Run(ctx context.Context) {
	sigch := make(chan os.Signal, 8)
	signal.Notify(sigch, syscall.SIGCHLD)
	defer signal.Stop(sigch)

	for {
		select {
		case <-ctx.Done():
			r.reapAll() // final drain
			return
		case <-sigch:
			r.reapAll()
		}
	}
}

func (r *Reaper) reapAll() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			// Async preemption (SIGURG) and other signals can interrupt the
			// syscall. Returning here would abandon still-undrained zombies;
			// if their SIGCHLD was the edge that woke us, nothing reaps them
			// and the matching WaitFor blocks forever. Retry instead.
			continue
		}
		if pid <= 0 || err != nil {
			return
		}
		r.deliver(pid, ws)
	}
}

// deliver hands a reaped child's status to a waiting WaitFor. If WaitFor
// has not registered yet — its caller races the child's exit — the status
// is buffered so it isn't lost. Buffered statuses for orphans nobody waits
// for stay until the process exits; the agent is short-lived per command,
// so this does not grow without bound.
func (r *Reaper) deliver(pid int, ws syscall.WaitStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.watched[pid]; ok {
		delete(r.watched, pid)
		ch <- ws
		return
	}
	r.pending[pid] = ws
}

// WaitFor waits for cmd — an already-started child — to exit and returns
// its conventional exit code. It cooperates with the Run goroutine instead
// of calling (*exec.Cmd).Wait, which would race the reaper over the same
// zombie.
func (r *Reaper) WaitFor(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 127
	}
	pid := cmd.Process.Pid

	r.mu.Lock()
	if ws, ok := r.pending[pid]; ok {
		// The child exited and was reaped before we registered.
		delete(r.pending, pid)
		r.mu.Unlock()
		_ = cmd.Process.Release()
		return childproc.ExitCodeFromWaitStatus(ws)
	}
	ch := make(chan syscall.WaitStatus, 1)
	r.watched[pid] = ch
	r.mu.Unlock()

	ws := <-ch
	// The reaper already collected the zombie; release Go's process handle
	// (e.g. the pidfd) without attempting another wait.
	_ = cmd.Process.Release()
	return childproc.ExitCodeFromWaitStatus(ws)
}
