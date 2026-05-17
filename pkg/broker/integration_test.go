package broker

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesits/machineproxy/pkg/remoteexec"
	"github.com/jamesits/machineproxy/pkg/sshconn"
)

func testBrokerServer(t *testing.T) *Server {
	t.Helper()

	host := os.Getenv("MPROXY_TEST_SSH_HOST")
	if host == "" {
		t.Skip("set MPROXY_TEST_SSH_HOST to run integration tests; identity is resolved via ~/.ssh/config")
	}
	user := os.Getenv("MPROXY_TEST_SSH_USER")

	dialer, err := sshconn.NewDialer(sshconn.DialConfig{
		Host:    host,
		User:    user,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}

	mgr := sshconn.NewManager(sshconn.Options{
		Dial:              dialer.Dial,
		ReconnectInterval: 500 * time.Millisecond,
	})
	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Manager.Start: %v", err)
	}
	if !mgr.IsConnected() {
		t.Fatalf("Manager not connected: %v", mgr.LastErr())
	}
	t.Cleanup(func() { _ = mgr.Close() })

	runner := &remoteexec.SSHRunner{Provider: mgr}
	return NewServer(Deps{Remote: runner})
}

// dialBroker connects to the broker socket and returns the connection
// cast to *net.UnixConn so callers can CloseWrite to signal stdin EOF.
func dialBroker(t *testing.T, socketPath string) *net.UnixConn {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	uc := conn.(*net.UnixConn)
	t.Cleanup(func() { _ = uc.Close() })
	return uc
}

func TestIntegrationBrokerEcho(t *testing.T) {
	srv := testBrokerServer(t)

	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx, socketPath); err != nil && ctx.Err() == nil {
			t.Logf("srv.Start: %v", err)
		}
	}()
	waitForSocket(t, socketPath)

	conn := dialBroker(t, socketPath)
	enc := NewEncoder(conn)
	dec := NewDecoder(conn)

	if err := enc.Encode(ExecRequest{
		Path: "/bin/echo",
		Argv: []string{"echo", "broker-test"},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Close write side so broker's readStdinFrames gets EOF,
	// allowing SSHRunner's stdin copy goroutine to finish.
	_ = conn.CloseWrite()

	var stdout string
	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch frame.Stream {
		case StreamStdout:
			stdout += string(frame.Data)
		case StreamExit:
			if frame.Code != 0 {
				t.Fatalf("exit code = %d, error = %q", frame.Code, frame.Error)
			}
			if want := "broker-test\n"; stdout != want {
				t.Fatalf("stdout = %q, want %q", stdout, want)
			}
			return
		}
	}
}

func TestIntegrationBrokerStdinForward(t *testing.T) {
	srv := testBrokerServer(t)

	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx, socketPath); err != nil && ctx.Err() == nil {
			t.Logf("srv.Start: %v", err)
		}
	}()
	waitForSocket(t, socketPath)

	conn := dialBroker(t, socketPath)
	enc := NewEncoder(conn)
	dec := NewDecoder(conn)

	if err := enc.Encode(ExecRequest{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "cat"},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Send stdin data then close write side to signal EOF to cat
	if err := enc.Encode(Frame{Stream: StreamStdin, Data: []byte("from broker")}); err != nil {
		t.Fatalf("send stdin: %v", err)
	}
	_ = conn.CloseWrite()

	var stdout string
	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch frame.Stream {
		case StreamStdout:
			stdout += string(frame.Data)
		case StreamExit:
			if frame.Code != 0 {
				t.Fatalf("exit code = %d, error = %q", frame.Code, frame.Error)
			}
			if stdout != "from broker" {
				t.Fatalf("stdout = %q, want %q", stdout, "from broker")
			}
			return
		}
	}
}

func TestIntegrationBrokerNonZeroExit(t *testing.T) {
	srv := testBrokerServer(t)

	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx, socketPath); err != nil && ctx.Err() == nil {
			t.Logf("srv.Start: %v", err)
		}
	}()
	waitForSocket(t, socketPath)

	conn := dialBroker(t, socketPath)
	enc := NewEncoder(conn)
	dec := NewDecoder(conn)

	if err := enc.Encode(ExecRequest{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "exit 99"},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_ = conn.CloseWrite()

	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if frame.Stream == StreamExit {
			if frame.Code != 99 {
				t.Fatalf("exit code = %d, want 99", frame.Code)
			}
			return
		}
	}
}

func TestIntegrationBrokerMultipleClients(t *testing.T) {
	srv := testBrokerServer(t)

	socketPath := filepath.Join(t.TempDir(), "broker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Start(ctx, socketPath); err != nil && ctx.Err() == nil {
			t.Logf("srv.Start: %v", err)
		}
	}()
	waitForSocket(t, socketPath)

	type result struct {
		stdout string
		code   int
		err    error
	}
	results := make(chan result, 2)

	for i := range 2 {
		go func(idx int) {
			conn, err := net.Dial("unix", socketPath)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer conn.Close()

			uc := conn.(*net.UnixConn)
			enc := NewEncoder(uc)
			dec := NewDecoder(uc)

			if err := enc.Encode(ExecRequest{
				Path: "/bin/echo",
				Argv: []string{"echo", string(rune('A' + idx))},
			}); err != nil {
				results <- result{err: err}
				return
			}
			_ = uc.CloseWrite()

			var stdout string
			for {
				var frame Frame
				if err := dec.Decode(&frame); err != nil {
					results <- result{err: err}
					return
				}
				if frame.Stream == StreamStdout {
					stdout += string(frame.Data)
				}
				if frame.Stream == StreamExit {
					results <- result{stdout: stdout, code: frame.Code}
					return
				}
			}
		}(i)
	}

	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("client error: %v", r.err)
		}
		if r.code != 0 {
			t.Fatalf("exit code = %d", r.code)
		}
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear", path)
}
