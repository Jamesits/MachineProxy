//go:build !linux

package dialer

import (
	"errors"
	"syscall"
)

// newBindControl rejects a non-empty interface bind on platforms without
// SO_BINDTODEVICE. An empty iface yields a nil hook (no-op).
func newBindControl(iface string) (func(network, address string, c syscall.RawConn) error, error) {
	if iface == "" {
		return nil, nil
	}
	return nil, errors.New("bind_interface (SO_BINDTODEVICE) is only supported on Linux")
}
