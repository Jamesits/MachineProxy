//go:build darwin

package main

import (
	"net"
	"os"
	"strconv"
	"syscall"
)

// detectExtraFDs is the darwin counterpart of the Linux /proc/self/fd
// scan. macOS exposes /dev/fd, which directory-lists the actually-open
// FDs of the current process; reading it gives us the same upper bound
// as procfs without a hard-coded scan range.
//
// Filtering rules mirror the Linux version: drop FDs 0–2 (stdio), the
// directory FD we're reading, the broker socket, and anything marked
// FD_CLOEXEC. The Linux version additionally filters anon_inode targets
// (epoll/eventfd/signalfd) — Go's darwin runtime uses kqueue and a
// self-pipe instead, both of which are opened CLOEXEC, so the FD_CLOEXEC
// check is sufficient.
func detectExtraFDs(brokerConn net.Conn) []uint32 {
	var brokerFD uintptr
	if uc, ok := brokerConn.(*net.UnixConn); ok {
		if raw, err := uc.SyscallConn(); err == nil {
			_ = raw.Control(func(fd uintptr) { brokerFD = fd })
		}
	}

	dir, err := os.Open("/dev/fd")
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
		fds = append(fds, fd)
	}
	return fds
}
