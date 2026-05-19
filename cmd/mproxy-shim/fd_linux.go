//go:build linux

package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

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
