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

func TestCommandIncludesAllBinds(t *testing.T) {
	n := &Namespace{bwrapBin: "/usr/bin/bwrap"}
	_, args := n.Command("/workspace", []string{"echo", "hi"}, []Bind{
		{Src: "/tmp/fuse-ws", Dst: "/workspace"},
		{Src: "/tmp/fuse-stubs", Dst: "/home/user/.cache/machineproxy/pathstub"},
	})

	// Walk pairs of "--bind src dst" entries and confirm both binds appear.
	have := map[string]string{}
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--bind" {
			have[args[i+1]] = args[i+2]
		}
	}
	if have["/tmp/fuse-ws"] != "/workspace" {
		t.Errorf("workspace bind missing: %v", have)
	}
	if have["/tmp/fuse-stubs"] != "/home/user/.cache/machineproxy/pathstub" {
		t.Errorf("stub bind missing: %v", have)
	}

	// Must keep --dev-bind / / as the first mount.
	if args[0] != "--dev-bind" || args[1] != "/" || args[2] != "/" {
		t.Fatalf("--dev-bind / / not first: %v", args[:3])
	}

	// --chdir and cmdline must come after the binds.
	chdirIdx := -1
	for i, a := range args {
		if a == "--chdir" {
			chdirIdx = i
			break
		}
	}
	if chdirIdx < 0 || args[chdirIdx+1] != "/workspace" {
		t.Fatalf("--chdir /workspace missing")
	}
	if args[len(args)-2] != "echo" || args[len(args)-1] != "hi" {
		t.Fatalf("cmdline not at end: %v", args[len(args)-3:])
	}
}
