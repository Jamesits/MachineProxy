package ns

import (
	"testing"
)

func TestFormatEnvInjectsVariables(t *testing.T) {
	base := []string{"HOME=/home/dev", "PATH=/usr/bin"}
	env := FormatEnv(base, "/tmp/broker.sock", "/opt/shim")

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
}

func TestFormatEnvDoesNotInjectLdPreload(t *testing.T) {
	base := []string{"HOME=/home/dev"}
	env := FormatEnv(base, "", "")

	for _, e := range env {
		if len(e) >= 11 && e[:11] == "LD_PRELOAD=" {
			t.Fatalf("unexpected LD_PRELOAD in env: %v", env)
		}
	}
}
