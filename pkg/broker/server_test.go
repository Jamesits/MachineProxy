package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBrokerStartCreatesPrivateSocketWithPermissiveUmask(t *testing.T) {
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	srv := NewServer(Deps{})
	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx, socketPath)
	}()

	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("broker socket was not created")
		case <-ticker.C:
			info, err := os.Stat(socketPath)
			if err != nil {
				continue
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("broker start returned error: %v", err)
			}
			if got := info.Mode().Perm(); got&0o077 != 0 {
				t.Fatalf("socket mode = %o, want no group/other permissions", got)
			}
			return
		}
	}
}

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
