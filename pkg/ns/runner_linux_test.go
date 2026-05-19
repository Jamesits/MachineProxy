package ns

import (
	"testing"
)

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
