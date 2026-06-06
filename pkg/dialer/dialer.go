// Package dialer builds net.Dialers that bind the local side of an
// outbound connection to a specific source address or network
// interface/VRF. It is shared by the remote backends (SSH dials the
// target host; Docker dials a TCP docker daemon) so both honour the same
// remote.bind / remote.bind_interface semantics.
package dialer

import (
	"fmt"
	"net"
)

// New returns a *net.Dialer configured with the requested local binding.
// bind is an IP literal or interface name and sets LocalAddr; bindInterface
// is a network interface or VRF bound via SO_BINDTODEVICE (Linux only).
// Either may be empty. The dialer is otherwise zero-valued; callers may set
// Timeout/KeepAlive afterwards.
func New(bind, bindInterface string) (*net.Dialer, error) {
	d := &net.Dialer{}
	if bind != "" {
		localAddr, err := ResolveBindAddr(bind)
		if err != nil {
			return nil, fmt.Errorf("resolve bind %q: %w", bind, err)
		}
		d.LocalAddr = localAddr
	}
	control, err := newBindControl(bindInterface)
	if err != nil {
		return nil, fmt.Errorf("bind_interface %q: %w", bindInterface, err)
	}
	d.Control = control
	return d, nil
}

// ResolveBindAddr turns a bind value into a local TCP address. The value is
// either an IP literal (used verbatim) or a network interface name, in which
// case the interface's first global-unicast address is chosen, preferring
// IPv4 over IPv6.
func ResolveBindAddr(bind string) (net.Addr, error) {
	if ip := net.ParseIP(bind); ip != nil {
		return &net.TCPAddr{IP: ip}, nil
	}
	iface, err := net.InterfaceByName(bind)
	if err != nil {
		return nil, fmt.Errorf("not an IP literal and interface lookup failed: %w", err)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("list addresses of interface %q: %w", bind, err)
	}
	var v6 net.IP
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP == nil || !ipNet.IP.IsGlobalUnicast() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return &net.TCPAddr{IP: ip4}, nil
		}
		if v6 == nil {
			v6 = ipNet.IP
		}
	}
	if v6 != nil {
		return &net.TCPAddr{IP: v6}, nil
	}
	return nil, fmt.Errorf("interface %q has no global-unicast address to bind", bind)
}
