package remoteexec

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jamesits/machineproxy/pkg/sshconn"
)

func testSSHRunner(t *testing.T) (*SSHRunner, func()) {
	t.Helper()

	addr := os.Getenv("MPROXY_TEST_SSH_ADDR")
	keyPath := os.Getenv("MPROXY_TEST_SSH_KEY")
	if addr == "" || keyPath == "" {
		t.Skip("set MPROXY_TEST_SSH_ADDR and MPROXY_TEST_SSH_KEY to run integration tests")
	}
	user := os.Getenv("MPROXY_TEST_SSH_USER")
	if user == "" {
		user = "root"
	}

	dial, err := sshconn.NewDialFunc(sshconn.DialConfig{
		Addr:           addr,
		User:           user,
		PrivateKeyPath: keyPath,
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialFunc: %v", err)
	}

	ctx := context.Background()
	mgr := sshconn.NewManager(sshconn.Options{
		Dial:              dial,
		ReconnectInterval: 500 * time.Millisecond,
	})
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Manager.Start: %v", err)
	}
	if !mgr.IsConnected() {
		t.Fatalf("Manager not connected: %v", mgr.LastErr())
	}

	runner := &SSHRunner{Provider: mgr}
	return runner, func() { mgr.Close() }
}

func TestIntegrationRunEchoCommand(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/echo",
		Argv: []string{"echo", "hello", "world"},
	}, strings.NewReader(""), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "hello world" {
		t.Fatalf("stdout = %q, want %q", got, "hello world")
	}
}

func TestIntegrationRunNonZeroExit(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "exit 42"},
	}, strings.NewReader(""), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 42 {
		t.Fatalf("exit code = %d, want 42", code)
	}
}

func TestIntegrationRunStderr(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "echo errmsg >&2"},
	}, strings.NewReader(""), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stderr.String()); got != "errmsg" {
		t.Fatalf("stderr = %q, want %q", got, "errmsg")
	}
}

func TestIntegrationRunStdin(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "cat"},
	}, strings.NewReader("piped input"), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := stdout.String(); got != "piped input" {
		t.Fatalf("stdout = %q, want %q", got, "piped input")
	}
}

func TestIntegrationRunWithCwd(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "pwd"},
		Cwd:  "/tmp",
	}, strings.NewReader(""), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "/tmp" {
		t.Fatalf("pwd = %q, want %q", got, "/tmp")
	}
}

func TestIntegrationRunCommandNotFound(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/nonexistent/binary",
	}, strings.NewReader(""), &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code == 0 {
		t.Fatal("expected non-zero exit code for missing binary")
	}
}

// TestIntegrationRunReturnsWithOpenStdin verifies that Run returns promptly
// after the remote command exits even when the caller's stdin reader has not
// returned EOF (regression test for deadlock).
func TestIntegrationRunReturnsWithOpenStdin(t *testing.T) {
	runner, cleanup := testSSHRunner(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Use a pipe whose write end is never closed — stdin will block forever.
	stdinR, _ := io.Pipe()
	defer stdinR.Close()

	var stdout, stderr bytes.Buffer
	code, err := runner.Run(ctx, Request{
		Path: "/bin/sh",
		Argv: []string{"sh", "-c", "echo done"},
	}, stdinR, &stdout, &stderr)

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "done" {
		t.Fatalf("stdout = %q, want %q", got, "done")
	}
}
