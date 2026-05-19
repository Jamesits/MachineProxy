package agentproto

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
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

func TestErrRespPreservesPathErrno(t *testing.T) {
	resp := errResp(&os.PathError{Op: "rmdir", Path: "/tmp/nonempty", Err: syscall.ENOTEMPTY})

	if resp.Errno != uint32(syscall.ENOTEMPTY) {
		t.Fatalf("errResp errno = %d, want ENOTEMPTY", resp.Errno)
	}
	if !errors.Is(syscall.Errno(resp.Errno), syscall.ENOTEMPTY) {
		t.Fatalf("response errno %d does not map back to ENOTEMPTY", resp.Errno)
	}
}

func TestFileOpOpenFileHonorsModeZero(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/private.txt"
	server := NewFileOpServer()
	defer server.CloseAll()

	resp := server.Handle(&FileOpReq{Op: FileOpOpenFile, Path: path, Flags: int32(os.O_CREATE | os.O_WRONLY), Mode: 0})
	if resp.Errno != 0 {
		t.Fatalf("OpenFile errno = %d (%s), want 0", resp.Errno, resp.ErrMsg)
	}
	if closeResp := server.Handle(&FileOpReq{Op: FileOpClose, Handle: resp.Handle}); closeResp.Errno != 0 {
		t.Fatalf("Close errno = %d (%s), want 0", closeResp.Errno, closeResp.ErrMsg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("mode = %o, want 000", got)
	}
}

func TestFileOpBadHandleReturnsEBADF(t *testing.T) {
	server := NewFileOpServer()

	resp := server.Handle(&FileOpReq{Op: FileOpFsync, Handle: 1234})

	if resp.Errno != uint32(syscall.EBADF) {
		t.Fatalf("Fsync bad handle errno = %d, want EBADF", resp.Errno)
	}
}

func TestFileOpAppendWriteUsesOpenFileDescription(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/log.txt"
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	server := NewFileOpServer()
	defer server.CloseAll()

	openResp := server.Handle(&FileOpReq{Op: FileOpOpenFile, Path: path, Flags: int32(os.O_WRONLY | os.O_APPEND)})
	if openResp.Errno != 0 {
		t.Fatalf("OpenFile errno = %d (%s), want 0", openResp.Errno, openResp.ErrMsg)
	}
	writeResp := server.Handle(&FileOpReq{Op: FileOpWriteAt, Handle: openResp.Handle, Offset: -1, Data: []byte("new")})
	if writeResp.Errno != 0 {
		t.Fatalf("append Write errno = %d (%s), want 0", writeResp.Errno, writeResp.ErrMsg)
	}
	if writeResp.N != 3 {
		t.Fatalf("append Write N = %d, want 3", writeResp.N)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "oldnew" {
		t.Fatalf("contents = %q, want oldnew", string(data))
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
