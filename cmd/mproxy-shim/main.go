package main

import (
	"encoding/json"
	"io"
	"net"
	"os"

	"github.com/jamesits/machineproxy/pkg/broker"
)

func main() {
	os.Exit(Run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(args) < 2 {
		return 127
	}

	socketPath := os.Getenv("MPROXY_BROKER_SOCK")
	if socketPath == "" {
		return 127
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return 127
	}
	defer conn.Close()

	req := broker.ExecRequest{
		Path: args[1],
		Argv: targetArgv(args),
		Env:  os.Environ(),
	}
	if cwd, err := os.Getwd(); err == nil {
		req.Cwd = cwd
	}

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(req); err != nil {
		return 127
	}

	go func() {
		streamStdin(enc, stdin)
		if c, ok := conn.(*net.UnixConn); ok {
			_ = c.CloseWrite()
		}
	}()

	for {
		var frame broker.Frame
		if err := dec.Decode(&frame); err != nil {
			return 127
		}

		switch frame.Stream {
		case broker.StreamStdout:
			_, _ = stdout.Write(frame.Data)
		case broker.StreamStderr:
			_, _ = stderr.Write(frame.Data)
		case broker.StreamExit:
			return frame.Code
		}
	}
}

func streamStdin(enc *json.Encoder, stdin io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			_ = enc.Encode(broker.Frame{Stream: broker.StreamStdin, Data: chunk})
		}
		if err != nil {
			return
		}
	}
}

func targetArgv(args []string) []string {
	if len(args) > 2 {
		return args[2:]
	}
	return nil
}
