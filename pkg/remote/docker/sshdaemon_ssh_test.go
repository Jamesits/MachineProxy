//go:build backend_docker && backend_ssh

package docker

import (
	"log/slog"
	"testing"
)

func TestBuiltinSSHDialReturnsDialerWithoutConnecting(t *testing.T) {
	cases := []string{
		"ssh://user@docker-host.example.com",
		"ssh://user@docker-host.example.com:2222",
		"ssh://docker-host.example.com/run/user/1000/docker.sock",
	}
	for _, host := range cases {
		t.Run(host, func(t *testing.T) {
			dialFn, dummyHost, err := builtinSSHDial(host, "192.0.2.10", "", slog.Default())
			if err != nil {
				t.Fatalf("builtinSSHDial(%q) error = %v", host, err)
			}
			if dialFn == nil {
				t.Fatal("expected non-nil dial func")
			}
			if dummyHost != dummySSHDaemonHost {
				t.Fatalf("dummyHost = %q, want %q", dummyHost, dummySSHDaemonHost)
			}
		})
	}
}

func TestBuiltinSSHDialRejectsBadPort(t *testing.T) {
	if _, _, err := builtinSSHDial("ssh://user@host:notaport", "", "", slog.Default()); err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestBuiltinSSHDialRejectsBadBind(t *testing.T) {
	// An unresolvable bind interface surfaces from sshconn.NewDialer.
	if _, _, err := builtinSSHDial("ssh://user@host", "definitely-not-an-iface-zzz", "", slog.Default()); err == nil {
		t.Fatal("expected error for unresolvable bind value")
	}
}
