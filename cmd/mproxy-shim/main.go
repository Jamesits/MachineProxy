package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
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
	extraFiles := newFDFileSet()
	defer extraFiles.Close()

	req := broker.ExecRequest{
		Path:     args[1],
		Argv:     targetArgv(args),
		Env:      os.Environ(),
		ExtraFDs: extraFDs,
	}
	if cwd, err := os.Getwd(); err == nil {
		req.Cwd = cwd
	}

	enc := broker.NewEncoder(conn)
	dec := broker.NewDecoder(conn)

	if err := enc.Encode(req); err != nil {
		return 127
	}

	safeSend := func(frame broker.Frame) {
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
		f := extraFiles.Get(fdNum)
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
				f := extraFiles.Get(uint32(fd))
				if f != nil && len(frame.Data) > 0 {
					_, _ = f.Write(frame.Data)
				}
			}
		}
	}
}

type fdFileSet struct {
	mu    sync.Mutex
	files map[uint32]*os.File
}

func newFDFileSet() *fdFileSet {
	return &fdFileSet{files: make(map[uint32]*os.File)}
}

func (s *fdFileSet) Get(fd uint32) *os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.files[fd]; f != nil {
		return f
	}
	// Keep one wrapper alive per descriptor for the whole shim lifetime. We do
	// not own these inherited descriptors, so clear the finalizer to prevent the
	// wrapper from closing streams that the target process still owns.
	f := os.NewFile(uintptr(fd), fmt.Sprintf("fd/%d", fd))
	if f != nil {
		runtime.SetFinalizer(f, nil)
		s.files[fd] = f
	}
	return f
}

func (s *fdFileSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for fd := range s.files {
		delete(s.files, fd)
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

// detectExtraFDs finds open file descriptors beyond stdin/stdout/stderr that
// were inherited from the target process, excluding the broker socket. FDs
// marked FD_CLOEXEC are skipped: by convention they are not for inheritance,
// and in production execve() would have already closed them before the shim
// ran. The explicit check also keeps the shim isolated from the surrounding
// process's own descriptors when Run is exercised in-process (e.g. unit tests),
// where Go-opened sockets share the FD table.
//
// /proc/self/fd is the source of truth: it enumerates only the actually-open
// fds (so we avoid a bounded scan that would silently drop anything above
// RLIMIT_NOFILE-ish defaults) and lets us readlink the target to skip Go
// runtime internals (epoll, eventfd, signalfd, etc., which all surface as
// anon_inode:[…]). The anon_inode filter is belt-and-suspenders alongside
// FD_CLOEXEC, since the runtime could in principle hold a non-CLOEXEC fd.
//
// Caveats:
//   - Linux-only. /proc/self/fd doesn't exist on other kernels; the shim is
//     already Linux-only via Bubblewrap so we don't bother gating with a
//     build tag, but anyone porting this should provide an alternative
//     (e.g. /dev/fd on FreeBSD, proc_pidinfo on macOS).
//   - The fd opened to read /proc/self/fd appears in its own listing; we
//     exclude it explicitly via dirFD so it doesn't leak into the result.
//   - The anon_inode filter drops any legitimately-inherited anonymous-inode
//     fd (a memfd, epollfd, or similar that the tracee opened and passed
//     through without O_CLOEXEC). This is a deliberate trade-off: those
//     handles aren't meaningful to recreate on a remote machine anyway, and
//     the common case of forwarding regular files/sockets/pipes is unaffected.
func detectExtraFDs(brokerConn net.Conn) []uint32 {
	var brokerFD uintptr
	if uc, ok := brokerConn.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) { brokerFD = fd })
		}
	}

	dir, err := os.Open("/proc/self/fd")
	if err != nil {
		return nil
	}
	defer dir.Close()
	dirFD := uint32(dir.Fd())

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil
	}

	fds := make([]uint32, 0, len(names))
	for _, name := range names {
		n, err := strconv.ParseUint(name, 10, 32)
		if err != nil {
			continue
		}
		fd := uint32(n)
		if fd < 3 || fd == dirFD || uintptr(fd) == brokerFD {
			continue
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno != 0 || flags&syscall.FD_CLOEXEC != 0 {
			continue
		}
		// Skip Go runtime internals (epoll, eventfd, signalfd, …); they all
		// readlink to anon_inode:[…] and were never part of the tracee.
		target, err := os.Readlink("/proc/self/fd/" + name)
		if err != nil || strings.HasPrefix(target, "anon_inode:") {
			continue
		}
		fds = append(fds, fd)
	}
	return fds
}

func targetArgv(args []string) []string {
	if len(args) > 2 {
		return args[2:]
	}
	return nil
}
