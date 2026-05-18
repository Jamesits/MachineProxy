package agentproto

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// TestFileOpRoundTrip drives a mock agent that echoes the Path back as
// the response, then verifies the client receives the matching response.
func TestFileOpRoundTrip(t *testing.T) {
	cr, aw := io.Pipe()
	ar, cw := io.Pipe()
	defer cr.Close()
	defer ar.Close()
	defer aw.Close()
	defer cw.Close()

	client := NewMux(cr, cw)
	agent := NewMux(ar, aw)

	agent.OnFileOp(func(req *FileOpReq) *FileOpResp {
		return &FileOpResp{Path: req.Path + "!"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Run both read loops.
	go func() { _, _ = agent.ReadLoop(ctx) }()
	go func() { _, _ = client.ReadLoop(ctx) }()

	resp, err := client.FileOp(ctx, &FileOpReq{Op: FileOpGetwd, Path: "/tmp"})
	if err != nil {
		t.Fatalf("FileOp returned error: %v", err)
	}
	if resp.Path != "/tmp!" {
		t.Fatalf("Path = %q, want %q", resp.Path, "/tmp!")
	}
}

// TestFileOpConcurrent verifies that many concurrent callers each get
// their own response back, with no cross-talk.
func TestFileOpConcurrent(t *testing.T) {
	cr, aw := io.Pipe()
	ar, cw := io.Pipe()
	defer cr.Close()
	defer ar.Close()
	defer aw.Close()
	defer cw.Close()

	client := NewMux(cr, cw)
	agent := NewMux(ar, aw)

	agent.OnFileOp(func(req *FileOpReq) *FileOpResp {
		return &FileOpResp{Path: req.Path}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _, _ = agent.ReadLoop(ctx) }()
	go func() { _, _ = client.ReadLoop(ctx) }()

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			want := pathFor(i)
			resp, err := client.FileOp(ctx, &FileOpReq{Op: FileOpStat, Path: want})
			if err != nil {
				errs <- err
				return
			}
			if resp.Path != want {
				errs <- &mismatchErr{want: want, got: resp.Path}
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent FileOp: %v", err)
	}
}

func pathFor(i int) string {
	return "/dir/" + string(rune('a'+(i%26))) + "/" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

type mismatchErr struct{ want, got string }

func (e *mismatchErr) Error() string { return "want " + e.want + " got " + e.got }
