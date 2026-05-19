package ns

import (
	"strings"
	"testing"
)

func TestFormatEnvInjectsMproxyVars(t *testing.T) {
	base := []string{"HOME=/home/dev", "PATH=/usr/bin"}
	env := FormatEnv(base, "/tmp/broker.sock", "/opt/shim", PathInjection{})

	find := func(prefix string) string {
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
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
	if v := find("PATH="); v != "/usr/bin" {
		t.Fatalf("PATH should be unchanged when no injection: %q", v)
	}
}

func TestFormatEnvDoesNotInjectLdPreload(t *testing.T) {
	base := []string{"HOME=/home/dev"}
	env := FormatEnv(base, "", "", PathInjection{})

	for _, e := range env {
		if strings.HasPrefix(e, "LD_PRELOAD=") {
			t.Fatalf("unexpected LD_PRELOAD in env: %v", env)
		}
	}
}

func TestFormatEnvPrependsPath(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin"}
	env := FormatEnv(base, "", "", PathInjection{Dir: "/stub", Position: "prepend"})
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			want := "PATH=/stub:/usr/bin:/bin"
			if e != want {
				t.Fatalf("PATH = %q, want %q", e, want)
			}
			return
		}
	}
	t.Fatal("PATH not found")
}

func TestFormatEnvAppendsPath(t *testing.T) {
	base := []string{"PATH=/usr/bin:/bin"}
	env := FormatEnv(base, "", "", PathInjection{Dir: "/stub", Position: "append"})
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			want := "PATH=/usr/bin:/bin:/stub"
			if e != want {
				t.Fatalf("PATH = %q, want %q", e, want)
			}
			return
		}
	}
	t.Fatal("PATH not found")
}

func TestFormatEnvCreatesPathWhenMissing(t *testing.T) {
	env := FormatEnv(nil, "", "", PathInjection{Dir: "/stub", Position: "prepend"})
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			if !strings.HasPrefix(e, "PATH=/stub:") {
				t.Fatalf("PATH should start with /stub: %q", e)
			}
			if !strings.Contains(e, "/usr/bin") {
				t.Fatalf("PATH should include a sane default: %q", e)
			}
			return
		}
	}
	t.Fatal("PATH not created")
}

func TestFormatEnvSkipsPathIfAlreadyPresent(t *testing.T) {
	base := []string{"PATH=/stub:/usr/bin"}
	env := FormatEnv(base, "", "", PathInjection{Dir: "/stub", Position: "prepend"})
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			if e != "PATH=/stub:/usr/bin" {
				t.Fatalf("PATH = %q, want unchanged", e)
			}
			return
		}
	}
	t.Fatal("PATH not found")
}
