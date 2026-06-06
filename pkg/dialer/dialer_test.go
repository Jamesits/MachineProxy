package dialer

import (
	"net"
	"testing"
)

func TestResolveBindAddrIPLiteral(t *testing.T) {
	cases := []string{"192.0.2.10", "203.0.113.5", "2001:db8::1", "::1"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			addr, err := ResolveBindAddr(in)
			if err != nil {
				t.Fatalf("ResolveBindAddr(%q) error = %v", in, err)
			}
			tcp, ok := addr.(*net.TCPAddr)
			if !ok {
				t.Fatalf("addr type = %T, want *net.TCPAddr", addr)
			}
			if !tcp.IP.Equal(net.ParseIP(in)) {
				t.Fatalf("IP = %v, want %v", tcp.IP, in)
			}
			if tcp.Port != 0 {
				t.Fatalf("Port = %d, want 0 (ephemeral)", tcp.Port)
			}
		})
	}
}

func TestResolveBindAddrUnknownInterface(t *testing.T) {
	// A value that is neither a valid IP nor an existing interface must
	// surface an error rather than silently binding nothing.
	if _, err := ResolveBindAddr("definitely-not-an-iface-zzz"); err == nil {
		t.Fatal("expected error for unknown interface name")
	}
}

func TestResolveBindAddrInterfaceWithGlobalUnicast(t *testing.T) {
	// Exercise the interface branch against whichever local interface
	// exposes a global-unicast address; skip when none exist (e.g. a
	// loopback-only CI sandbox).
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("no interfaces available")
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		hasGlobal := false
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.IP != nil && ipNet.IP.IsGlobalUnicast() {
				hasGlobal = true
				break
			}
		}
		if !hasGlobal {
			continue
		}
		got, err := ResolveBindAddr(iface.Name)
		if err != nil {
			t.Fatalf("ResolveBindAddr(%q) error = %v", iface.Name, err)
		}
		tcp := got.(*net.TCPAddr)
		if !tcp.IP.IsGlobalUnicast() {
			t.Fatalf("resolved IP %v for %q is not global-unicast", tcp.IP, iface.Name)
		}
		return
	}
	t.Skip("no interface with a global-unicast address available")
}

func TestNewEmptyIsZeroValue(t *testing.T) {
	d, err := New("", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if d.LocalAddr != nil || d.Control != nil {
		t.Fatalf("expected zero-value dialer, got LocalAddr=%v Control!=nil:%v", d.LocalAddr, d.Control != nil)
	}
}

func TestNewSetsLocalAddr(t *testing.T) {
	d, err := New("192.0.2.10", "")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tcp, ok := d.LocalAddr.(*net.TCPAddr)
	if !ok || !tcp.IP.Equal(net.ParseIP("192.0.2.10")) {
		t.Fatalf("LocalAddr = %v, want 192.0.2.10", d.LocalAddr)
	}
}
