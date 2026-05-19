package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/jamesits/machineproxy/pkg/broker"
)

func TestRunBridgesStreamsAndExitCode(t *testing.T) {
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "broker.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		dec := broker.NewDecoder(conn)
		enc := broker.NewEncoder(conn)

		var req broker.ExecRequest
		_ = dec.Decode(&req)

		_ = enc.Encode(broker.Frame{Stream: broker.StreamStdout, Data: []byte("out")})
		_ = enc.Encode(broker.Frame{Stream: broker.StreamStderr, Data: []byte("err")})
		_ = enc.Encode(broker.Frame{Stream: broker.StreamExit, Code: 42})
	}()

	t.Setenv("MPROXY_BROKER_SOCK", sock)

	stdin := bytes.NewBufferString("ignored")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"mproxy-shim", "/usr/bin/python3", "python3", "-V"}, stdin, &stdout, &stderr)

	if code != 42 {
		t.Fatalf("exit code = %d, want 42", code)
	}
	if stdout.String() != "out" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.String() != "err" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunNoOriginalArgvSendsNilArgv(t *testing.T) {
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "broker.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	var (
		gotReq    broker.ExecRequest
		decodeErr error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			decodeErr = err
			return
		}
		defer conn.Close()

		dec := broker.NewDecoder(conn)
		enc := broker.NewEncoder(conn)
		decodeErr = dec.Decode(&gotReq)
		_ = enc.Encode(broker.Frame{Stream: broker.StreamExit, Code: 0})
	}()

	t.Setenv("MPROXY_BROKER_SOCK", sock)

	code := Run([]string{"mproxy-shim", "/usr/bin/python3"}, bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// Wait for the server goroutine to finish so its writes to gotReq are
	// synchronized with this goroutine's reads below. Without this, the Go
	// memory model does not guarantee visibility through the socket I/O.
	<-done
	if decodeErr != nil {
		t.Fatalf("decode request: %v", decodeErr)
	}
	if gotReq.Path != "/usr/bin/python3" {
		t.Fatalf("req.Path = %q, want /usr/bin/python3", gotReq.Path)
	}
	if len(gotReq.Argv) != 0 {
		t.Fatalf("req.Argv = %v, want empty", gotReq.Argv)
	}
}

// Regression: Run must return promptly after receiving the exit frame even
// when stdin has not reached EOF (e.g. terminal input still open).
func TestRunReturnsWithOpenStdin(t *testing.T) {
	tempDir := t.TempDir()
	sock := filepath.Join(tempDir, "broker.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		dec := broker.NewDecoder(conn)
		enc := broker.NewEncoder(conn)
		var req broker.ExecRequest
		_ = dec.Decode(&req)
		_ = enc.Encode(broker.Frame{Stream: broker.StreamStdout, Data: []byte("ok")})
		_ = enc.Encode(broker.Frame{Stream: broker.StreamExit, Code: 0})
	}()

	t.Setenv("MPROXY_BROKER_SOCK", sock)

	// Pipe whose write end stays open — stdin never returns EOF.
	stdinR, _ := net.Pipe()
	defer stdinR.Close()

	var stdout, stderr bytes.Buffer
	code := Run([]string{"mproxy-shim", "/bin/true", "true"}, stdinR, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.String() != "ok" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "ok")
	}
}

func TestRunFailsWithoutBrokerSocket(t *testing.T) {
	if err := os.Unsetenv("MPROXY_BROKER_SOCK"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	code := Run([]string{"mproxy-shim", "/usr/bin/env", "env"}, bytes.NewBuffer(nil), &bytes.Buffer{}, &bytes.Buffer{})
	if code != 127 {
		t.Fatalf("exit code = %d, want 127", code)
	}
}
