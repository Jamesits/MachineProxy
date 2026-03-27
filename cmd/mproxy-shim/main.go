package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/broker"
)

func main() {
	os.Exit(Run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

func Run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	// Shim is performance-sensitive; only log errors to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	if len(args) < 2 {
		log.Error("missing arguments")
		return 127
	}

	socketPath := os.Getenv("MPROXY_BROKER_SOCK")
	if socketPath == "" {
		log.Error("MPROXY_BROKER_SOCK not set")
		return 127
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			log.Error("broker socket permission denied (stale socket?)", "path", socketPath)
		} else {
			log.Error("failed to connect to broker", "path", socketPath, "error", err)
		}
		return 127
	}
	defer conn.Close()

	extraFDs := detectExtraFDs(conn)

	req := broker.ExecRequest{
		Path:     args[1],
		Argv:     targetArgv(args),
		Env:      os.Environ(),
		ExtraFDs: extraFDs,
	}
	if cwd, err := os.Getwd(); err == nil {
		req.Cwd = cwd
	}

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(req); err != nil {
		return 127
	}

	var encMu sync.Mutex
	safeSend := func(frame broker.Frame) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = enc.Encode(frame)
	}

	// Forward signals to the broker.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for sig := range sigCh {
			if s, ok := sig.(syscall.Signal); ok {
				safeSend(broker.Frame{Stream: broker.StreamSignal, Signal: int(s)})
			}
		}
	}()

	// Bridge stdin to broker.
	go func() {
		streamStdin(safeSend, stdin)
		if c, ok := conn.(*net.UnixConn); ok {
			_ = c.CloseWrite()
		}
	}()

	// Bridge extra fds (local → broker).
	for _, fdNum := range extraFDs {
		fdNum := fdNum
		f := os.NewFile(uintptr(fdNum), fmt.Sprintf("fd/%d", fdNum))
		if f == nil {
			continue
		}
		go streamFD(safeSend, fdNum, f)
	}

	// Read frames from broker and dispatch.
	for {
		var frame broker.Frame
		if err := dec.Decode(&frame); err != nil {
			return 127
		}

		switch {
		case frame.Stream == broker.StreamStdout:
			_, _ = stdout.Write(frame.Data)
		case frame.Stream == broker.StreamStderr:
			_, _ = stderr.Write(frame.Data)
		case frame.Stream == broker.StreamExit:
			signal.Stop(sigCh)
			return frame.Code
		case strings.HasPrefix(frame.Stream, broker.StreamFDPrefix):
			// Data from remote extra fd → write to local fd.
			fdNumStr := frame.Stream[len(broker.StreamFDPrefix):]
			if fd, err := strconv.ParseUint(fdNumStr, 10, 32); err == nil {
				f := os.NewFile(uintptr(fd), fmt.Sprintf("fd/%d", fd))
				if f != nil && len(frame.Data) > 0 {
					_, _ = f.Write(frame.Data)
				}
			}
		}
	}
}

func streamStdin(send func(broker.Frame), stdin io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			send(broker.Frame{Stream: broker.StreamStdin, Data: chunk})
		}
		if err != nil {
			return
		}
	}
}

func streamFD(send func(broker.Frame), fdNum uint32, f *os.File) {
	stream := fmt.Sprintf("%s%d", broker.StreamFDPrefix, fdNum)
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			send(broker.Frame{Stream: stream, Data: chunk})
		}
		if err != nil {
			return
		}
	}
}

// detectExtraFDs finds open file descriptors beyond stdin/stdout/stderr,
// excluding the broker socket itself.
func detectExtraFDs(brokerConn net.Conn) []uint32 {
	// Get the broker socket fd to exclude it.
	var brokerFD uintptr
	if uc, ok := brokerConn.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) { brokerFD = fd })
		}
	}

	var fds []uint32
	for fd := 3; fd < 256; fd++ {
		if uintptr(fd) == brokerFD {
			continue
		}
		// Check if fd is valid by calling fstat.
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			continue
		}
		fds = append(fds, uint32(fd))
	}
	return fds
}

func targetArgv(args []string) []string {
	if len(args) > 2 {
		return args[2:]
	}
	return nil
}
