//go:build unix

package subreaper

import (
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

func startTestReaper(t *testing.T, r *Reaper) {
	t.Helper()
	ctx := t.Context()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("reaper did not stop")
		}
	})
}

// waitForWithTimeout fails the test instead of hanging if WaitFor never
// returns — the symptom of a reaper-vs-WaitFor race where the child's
// status is lost.
func waitForWithTimeout(t *testing.T, r *Reaper, cmd *exec.Cmd, d time.Duration) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- r.WaitFor(cmd) }()
	select {
	case c := <-done:
		return c
	case <-time.After(d):
		t.Fatal("WaitFor timed out (reaper stole the child's status?)")
		return -1
	}
}

// TestReaperWaitForExitCode verifies that a foreground child's exit status
// survives the concurrent wait4(-1) reaper. The loop exercises the timing
// window between Start and WaitFor that the old standalone reaper lost.
func TestReaperWaitForExitCode(t *testing.T) {
	r := NewReaper()
	startTestReaper(t, r)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"sh", "-c", "exit 0"}, 0},
		{"exit7", []string{"sh", "-c", "exit 7"}, 7},
		{"sigterm", []string{"sh", "-c", "kill -TERM $$"}, 128 + 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < 25; i++ {
				cmd := exec.Command(tc.args[0], tc.args[1:]...)
				if err := cmd.Start(); err != nil {
					t.Fatalf("start: %v", err)
				}
				if got := waitForWithTimeout(t, r, cmd, 10*time.Second); got != tc.want {
					t.Fatalf("WaitFor = %d, want %d", got, tc.want)
				}
			}
		})
	}
}

func TestReaperDrainsChildExitedBeforeRun(t *testing.T) {
	r := NewReaper()
	cmd := exec.Command("sh", "-c", "exit 5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Let the child exit before Run installs signal.Notify. Without the
	// startup drain in Run there is no SIGCHLD left to wake the reaper.
	time.Sleep(100 * time.Millisecond)
	startTestReaper(t, r)

	if got := waitForWithTimeout(t, r, cmd, 2*time.Second); got != 5 {
		t.Fatalf("WaitFor = %d, want %d", got, 5)
	}
}

// TestReaperConcurrent runs many foreground children at once, stressing the
// shared bookkeeping and ensuring each waiter gets its own child's status
// while the reaper also drains them.
func TestReaperConcurrent(t *testing.T) {
	r := NewReaper()
	startTestReaper(t, r)

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		want := i % 8
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command("sh", "-c", "exit "+strconv.Itoa(want))
			if err := cmd.Start(); err != nil {
				t.Errorf("start: %v", err)
				return
			}
			if got := r.WaitFor(cmd); got != want {
				t.Errorf("WaitFor = %d, want %d", got, want)
			}
		}()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent WaitFor timed out")
	}
}
