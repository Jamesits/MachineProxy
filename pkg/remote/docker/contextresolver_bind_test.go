//go:build backend_docker

package docker

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestAppendBindDialNoBindIsNoop(t *testing.T) {
	in := []client.Opt{client.FromEnv}
	out, err := appendBindDial(in, "tcp://10.0.0.1:2376", "", "", slog.Default())
	if err != nil {
		t.Fatalf("appendBindDial() error = %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("expected no added opt, got %d (was %d)", len(out), len(in))
	}
}

func TestAppendBindDialTCPAddsOpt(t *testing.T) {
	in := []client.Opt{client.FromEnv}
	out, err := appendBindDial(in, "tcp://10.0.0.1:2376", "192.0.2.10", "", slog.Default())
	if err != nil {
		t.Fatalf("appendBindDial() error = %v", err)
	}
	if len(out) != len(in)+1 {
		t.Fatalf("expected one added dial opt, got %d (was %d)", len(out), len(in))
	}
}

func TestAppendBindDialLocalSocketWarnsAndSkips(t *testing.T) {
	for _, host := range []string{"", "unix:///var/run/docker.sock", "npipe:////./pipe/docker_engine"} {
		t.Run(host, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
			in := []client.Opt{client.FromEnv}
			out, err := appendBindDial(in, host, "192.0.2.10", "", log)
			if err != nil {
				t.Fatalf("appendBindDial() error = %v", err)
			}
			if len(out) != len(in) {
				t.Fatalf("expected bind skipped for local socket, got %d opts", len(out))
			}
			if !strings.Contains(buf.String(), "local socket") {
				t.Errorf("expected local-socket warning, got:\n%s", buf.String())
			}
		})
	}
}

func TestAppendSSHDaemonDialDefaultUsesConnhelper(t *testing.T) {
	// No bind set: the system-ssh connection helper builds without dialing,
	// adding a WithHost + WithDialContext pair.
	in := []client.Opt{client.FromEnv}
	out, err := appendSSHDaemonDial(in, "ssh://user@docker-host.example.com", "", "", slog.Default())
	if err != nil {
		t.Fatalf("appendSSHDaemonDial() error = %v", err)
	}
	if len(out) != len(in)+2 {
		t.Fatalf("expected host+dial opts appended, got %d (was %d)", len(out), len(in))
	}
}

func TestAppendSSHDaemonDialBindLogsInfo(t *testing.T) {
	// Bind set: an info line announces the built-in ssh transport. The
	// builtin path is only compiled with the ssh backend; without it the
	// call errors (still after logging), so accept either outcome but always
	// require the info log.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	in := []client.Opt{client.FromEnv}
	out, err := appendSSHDaemonDial(in, "ssh://user@docker-host.example.com", "192.0.2.10", "", log)
	if !strings.Contains(buf.String(), "built-in ssh transport") {
		t.Errorf("expected built-in-ssh info log, got:\n%s", buf.String())
	}
	if err == nil && len(out) != len(in)+2 {
		t.Fatalf("expected host+dial opts appended, got %d (was %d)", len(out), len(in))
	}
}

func TestAppendBindDialInvalidBindErrors(t *testing.T) {
	in := []client.Opt{client.FromEnv}
	_, err := appendBindDial(in, "tcp://10.0.0.1:2376", "definitely-not-an-iface-zzz", "", slog.Default())
	if err == nil {
		t.Fatal("expected error for unresolvable bind value")
	}
}
