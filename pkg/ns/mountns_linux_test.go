package ns

import (
	"context"
	"errors"
	"testing"
)

func TestPrepareLocatesBwrap(t *testing.T) {
	n := New(Deps{
		LookPath: func(file string) (string, error) {
			if file != "bwrap" {
				t.Fatalf("expected lookup for bwrap, got %q", file)
			}
			return "/usr/bin/bwrap", nil
		},
	})

	if err := n.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if n.bwrapBin != "/usr/bin/bwrap" {
		t.Fatalf("expected bwrapBin=/usr/bin/bwrap, got %q", n.bwrapBin)
	}
}

func TestPrepareReturnsErrorWhenBwrapMissing(t *testing.T) {
	n := New(Deps{
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	})

	if err := n.Prepare(context.Background()); err == nil {
		t.Fatal("expected error when bwrap is missing")
	}
}

func TestCommandBuildsCorrectArgs(t *testing.T) {
	n := New(Deps{
		LookPath: func(string) (string, error) { return "/usr/bin/bwrap", nil },
	})
	if err := n.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	bin, args := n.Command("/tmp/fuse-123", "/workspace", []string{"/bin/sh", "-c", "echo hi"})

	if bin != "/usr/bin/bwrap" {
		t.Fatalf("expected bin=/usr/bin/bwrap, got %q", bin)
	}

	expected := []string{
		"--dev-bind", "/", "/",
		"--bind", "/tmp/fuse-123", "/workspace",
		"--die-with-parent",
		"/bin/sh", "-c", "echo hi",
	}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i := range expected {
		if args[i] != expected[i] {
			t.Fatalf("arg[%d]: expected %q, got %q", i, expected[i], args[i])
		}
	}
}

func TestFormatEnvInjectsVariables(t *testing.T) {
	base := []string{"HOME=/home/dev", "PATH=/usr/bin"}
	env := FormatEnv(base, "/tmp/broker.sock", "/opt/shim", []string{"/usr/bin/env"}, "/opt/hook.so")

	find := func(prefix string) string {
		for _, e := range env {
			if len(e) > len(prefix) && e[:len(prefix)] == prefix {
				return e[len(prefix):]
			}
		}
		return ""
	}

	if v := find("MPROXY_BROKER_SOCK="); v != "/tmp/broker.sock" {
		t.Fatalf("MPROXY_BROKER_SOCK=%q", v)
	}
	if v := find("MPROXY_SHIM_PATH="); v != "/opt/shim" {
		t.Fatalf("MPROXY_SHIM_PATH=%q", v)
	}
	if v := find("MPROXY_WHITELIST="); v != "/usr/bin/env" {
		t.Fatalf("MPROXY_WHITELIST=%q", v)
	}
	if v := find("LD_PRELOAD="); v != "/opt/hook.so" {
		t.Fatalf("LD_PRELOAD=%q", v)
	}
}

func TestFormatEnvAppendsExistingLdPreload(t *testing.T) {
	base := []string{"LD_PRELOAD=/existing/lib.so"}
	env := FormatEnv(base, "", "", nil, "/opt/hook.so")

	for _, e := range env {
		if e == "LD_PRELOAD=/opt/hook.so:/existing/lib.so" {
			return
		}
	}
	t.Fatalf("LD_PRELOAD not correctly merged: %v", env)
}
