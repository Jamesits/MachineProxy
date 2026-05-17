package agentproto

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

func TestMuxDataDispatch(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	var received bytes.Buffer
	var mu sync.Mutex

	mux := NewMux(pr, io.Discard)
	mux.RegisterStream(1, &testHandler{
		writeFn: func(data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			received.Write(data)
			return nil
		},
	})

	go func() {
		enc := NewEncoder(pw)
		_ = enc.Encode(&Frame{Type: FrameData, Stream: 1, Data: []byte("hello ")})
		_ = enc.Encode(&Frame{Type: FrameData, Stream: 1, Data: []byte("world")})
		_ = enc.Encode(&Frame{Type: FrameExit, Code: 0})
	}()

	code, err := mux.ReadLoop(context.Background())
	if err != nil {
		t.Fatalf("ReadLoop: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code: got %d, want 0", code)
	}

	mu.Lock()
	got := received.String()
	mu.Unlock()
	if got != "hello world" {
		t.Errorf("received: got %q, want %q", got, "hello world")
	}
}

func TestMuxSignalCallback(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	var gotSig int
	mux := NewMux(pr, io.Discard)
	mux.OnSignal(func(sig int) { gotSig = sig })

	go func() {
		enc := NewEncoder(pw)
		_ = enc.Encode(&Frame{Type: FrameSignal, Signal: 15})
		_ = enc.Encode(&Frame{Type: FrameExit, Code: 143})
	}()

	code, err := mux.ReadLoop(context.Background())
	if err != nil {
		t.Fatalf("ReadLoop: %v", err)
	}
	if code != 143 {
		t.Errorf("exit code: got %d, want 143", code)
	}
	if gotSig != 15 {
		t.Errorf("signal: got %d, want 15", gotSig)
	}
}

func TestMuxEOFClosesHandler(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()

	eofCalled := false
	mux := NewMux(pr, io.Discard)
	mux.RegisterStream(0, &testHandler{
		eofFn: func() { eofCalled = true },
	})

	go func() {
		enc := NewEncoder(pw)
		_ = enc.Encode(&Frame{Type: FrameEOF, Stream: 0})
		_ = enc.Encode(&Frame{Type: FrameExit, Code: 0})
	}()

	_, _ = mux.ReadLoop(context.Background())
	if !eofCalled {
		t.Error("EOF handler not called")
	}
}

func TestMuxBidirectional(t *testing.T) {
	// Wire two muxes together over a pair of pipes.
	ar, bw := io.Pipe()
	br, aw := io.Pipe()

	muxA := NewMux(ar, aw)
	muxB := NewMux(br, bw)

	var gotByA bytes.Buffer
	var muA sync.Mutex

	muxA.RegisterStream(1, &testHandler{
		writeFn: func(data []byte) error {
			muA.Lock()
			defer muA.Unlock()
			gotByA.Write(data)
			return nil
		},
	})

	// B: on receiving stream 0 EOF, echo back on stream 1 and send exit.
	var gotByB bytes.Buffer
	muxB.RegisterStream(0, &testHandler{
		writeFn: func(data []byte) error {
			gotByB.Write(data)
			return nil
		},
		eofFn: func() {
			// B received all stdin; respond with output and exit.
			_ = muxB.SendData(1, []byte("output"))
			_ = muxB.Send(&Frame{Type: FrameExit, Code: 0})
		},
	})

	// A sends stdin data to B.
	go func() {
		_ = muxA.SendData(0, []byte("input"))
		_ = muxA.SendEOF(0)
	}()

	// B runs its read loop in a goroutine.
	go func() { _, _ = muxB.ReadLoop(context.Background()) }()

	code, err := muxA.ReadLoop(context.Background())
	if err != nil {
		t.Fatalf("muxA ReadLoop: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code: got %d, want 0", code)
	}

	muA.Lock()
	a := gotByA.String()
	muA.Unlock()
	if a != "output" {
		t.Errorf("A received: got %q, want %q", a, "output")
	}

	b := gotByB.String()
	if b != "input" {
		t.Errorf("B received: got %q, want %q", b, "input")
	}
}

func TestMuxContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	mux := NewMux(pr, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Send one frame then cancel context (no exit frame).
		enc := NewEncoder(pw)
		_ = enc.Encode(&Frame{Type: FrameData, Stream: 1, Data: []byte("hi")})
		cancel()
		// Close the pipe so the decoder unblocks.
		_ = pw.Close()
	}()

	code, _ := mux.ReadLoop(ctx)
	if code != -1 {
		t.Errorf("exit code: got %d, want -1 (no exit frame)", code)
	}
}

// testHandler is a test helper implementing StreamHandler.
type testHandler struct {
	writeFn func([]byte) error
	eofFn   func()
}

func (h *testHandler) Write(data []byte) error {
	if h.writeFn != nil {
		return h.writeFn(data)
	}
	return nil
}

func (h *testHandler) EOF() {
	if h.eofFn != nil {
		h.eofFn()
	}
}
