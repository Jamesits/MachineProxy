//go:build backend_ssh

package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestParseIPv6LinkLocalURL documents the addressing contract: an IPv6
// link-local target carries a zone identifier ("%eth0"), and the standard
// library's URL parser only accepts that zone when the '%' is percent-encoded
// as "%25". Parsing "http://[fe80::4%25eth0]:80" must therefore decode back to
// the literal zone separator and produce a host:port that net.Dialer accepts.
// This case is deterministic and always runs.
func TestParseIPv6LinkLocalURL(t *testing.T) {
	const raw = "http://[fe80::4%25eth0]:80"

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	if got, want := u.Hostname(), "fe80::4%eth0"; got != want {
		t.Fatalf("Hostname() = %q, want %q", got, want)
	}
	if got, want := u.Port(), "80"; got != want {
		t.Fatalf("Port() = %q, want %q", got, want)
	}
	// net.JoinHostPort must re-bracket the literal so the result is the exact
	// string net.Dialer expects for a zoned link-local address.
	if got, want := net.JoinHostPort(u.Hostname(), u.Port()), "[fe80::4%eth0]:80"; got != want {
		t.Fatalf("JoinHostPort = %q, want %q", got, want)
	}
}

// TestDialSSHIPv6LinkLocal proves the SSH connect path (dialSSH) completes a
// full handshake against a server bound to an IPv6 link-local address. The
// zone identifier must survive the URL round-trip, otherwise the dial fails
// with "connect: invalid argument" because the kernel cannot pick the scope.
//
// Skips when the host has no IPv6 link-local interface (e.g. a loopback-only
// CI sandbox).
func TestDialSSHIPv6LinkLocal(t *testing.T) {
	ip, zone := linkLocalEndpoint(t)

	// The listener also needs the zone to select the link's scope.
	listenHost := ip.String() + "%" + zone
	port := startTestSSHServer(t, listenHost)

	// Reconstruct the dial target the way an operator would pass it: a URL
	// with the zone's '%' percent-encoded as "%25". url.Hostname() decodes it
	// back to the literal '%' separator that net.Dialer requires.
	raw := fmt.Sprintf("http://[%s%%25%s]:%d", ip.String(), zone, port)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	if want := ip.String() + "%" + zone; u.Hostname() != want {
		t.Fatalf("Hostname() = %q, want %q", u.Hostname(), want)
	}
	dialAddr := net.JoinHostPort(u.Hostname(), u.Port())

	clientCfg := &ssh.ClientConfig{
		// The server uses NoClientAuth; the client offers the "none" method.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialSSH(ctx, &net.Dialer{}, dialAddr, clientCfg)
	if err != nil {
		t.Fatalf("dialSSH(%q): %v", dialAddr, err)
	}
	defer conn.Close()

	if conn.ServerVersion() == "" {
		t.Fatal("ServerVersion() is empty; SSH handshake over link-local address did not complete")
	}
	// A keepalive global request proves a round-trip over the link-local link,
	// not just that the TCP connection opened.
	if err := conn.SendKeepAlive(ctx); err != nil {
		t.Fatalf("SendKeepAlive over link-local connection: %v", err)
	}
}

// linkLocalEndpoint returns the first usable IPv6 link-local unicast address
// and its interface name (zone), or skips the test when none exist. These are
// the only addresses that require the zone to be carried through dialing.
func linkLocalEndpoint(t *testing.T) (net.IP, string) {
	t.Helper()

	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() != nil || !ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			return ipn.IP, ifi.Name
		}
	}
	t.Skip("no IPv6 link-local address available on this host")
	return nil, ""
}

// startTestSSHServer brings up an in-process SSH server on listenHost:0 and
// returns the chosen port. The server accepts any client (NoClientAuth) and
// is torn down via t.Cleanup.
func startTestSSHServer(t *testing.T, listenHost string) int {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", net.JoinHostPort(listenHost, "0"))
	if err != nil {
		t.Skipf("cannot listen on link-local address %q: %v", listenHost, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr %q: %v", ln.Addr(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse listener port %q: %v", portStr, err)
	}

	go func() {
		for {
			nConn, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			go serveTestSSHConn(nConn, cfg)
		}
	}()

	return port
}

// serveTestSSHConn completes the SSH handshake and then drains requests and
// rejects channels until the peer disconnects.
func serveTestSSHConn(nConn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nConn, cfg)
	if err != nil {
		_ = nConn.Close()
		return
	}
	defer sc.Close()

	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		_ = ch.Reject(ssh.Prohibited, "test server opens no channels")
	}
}
