package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBrokerForwardsStdioAndExitCode(t *testing.T) {
	srv := NewServer(Deps{Remote: fakeRemoteRunner{
		stdout:   "stdout:ok",
		stderr:   "stderr:warn",
		exitCode: 7,
	}})

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.serveConn(ctx, serverConn)

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)

	req := ExecRequest{Path: "/usr/bin/python3", Argv: []string{"python3", "-V"}}
	if err := enc.Encode(req); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	var stdout, stderr string
	var code int

	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		switch frame.Stream {
		case StreamStdout:
			stdout += string(frame.Data)
		case StreamStderr:
			stderr += string(frame.Data)
		case StreamExit:
			code = frame.Code
			if stdout != "stdout:ok" {
				t.Fatalf("stdout = %q", stdout)
			}
			if stderr != "stderr:warn" {
				t.Fatalf("stderr = %q", stderr)
			}
			if code != 7 {
				t.Fatalf("code = %d", code)
			}
			return
		}
	}
}

func TestBrokerReturns127WhenRemoteCannotStart(t *testing.T) {
	srv := NewServer(Deps{Remote: fakeRemoteRunner{err: errors.New("not found")}})

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go srv.serveConn(ctx, serverConn)

	enc := json.NewEncoder(clientConn)
	dec := json.NewDecoder(clientConn)
	if err := enc.Encode(ExecRequest{Path: "/usr/bin/missing", Argv: []string{"missing"}}); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if frame.Stream != StreamExit {
			continue
		}
		if frame.Code != 127 {
			t.Fatalf("exit code = %d, want 127", frame.Code)
		}
		if frame.Error == "" {
			t.Fatalf("expected broker error message")
		}
		return
	}
}

type fakeRemoteRunner struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (f fakeRemoteRunner) Run(_ context.Context, _ ExecRequest, _ io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	_, _ = io.Copy(stdout, strings.NewReader(f.stdout))
	_, _ = io.Copy(stderr, strings.NewReader(f.stderr))
	return f.exitCode, f.err
}
