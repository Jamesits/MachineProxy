//go:build linux

package dialer

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// newBindControl returns a net.Dialer.Control hook that binds the socket to
// the named network interface (or VRF) via SO_BINDTODEVICE before connect.
// An empty iface yields a nil hook so the dialer behaves as default.
func newBindControl(iface string) (func(network, address string, c syscall.RawConn) error, error) {
	if iface == "" {
		return nil, nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) {
			setErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		}); err != nil {
			return fmt.Errorf("raw control: %w", err)
		}
		if setErr != nil {
			return fmt.Errorf("SO_BINDTODEVICE %q: %w", iface, setErr)
		}
		return nil
	}, nil
}
