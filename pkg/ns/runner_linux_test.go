package ns

import (
	"testing"
)

func TestCommandIncludesAllBinds(t *testing.T) {
	n := &Namespace{bwrapBin: "/usr/bin/bwrap"}
	_, args := n.Command("/workspace", []string{"echo", "hi"}, []Bind{
		{Src: "/tmp/fuse-ws", Dst: "/workspace"},
		// Identity alias of the same FUSE dir at the remote path
		// (auto_identity_mounts): same Src, distinct Dst.
		{Src: "/tmp/fuse-ws", Dst: "/remote"},
		{Src: "/tmp/fuse-stubs", Dst: "/home/user/.cache/machineproxy/pathstub"},
	})

	// Walk pairs of "--bind src dst" entries and collect every (src,dst).
	type pair struct{ src, dst string }
	var have []pair
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--bind" {
			have = append(have, pair{args[i+1], args[i+2]})
		}
	}
	hasBind := func(src, dst string) bool {
		for _, p := range have {
			if p.src == src && p.dst == dst {
				return true
			}
		}
		return false
	}
	if !hasBind("/tmp/fuse-ws", "/workspace") {
		t.Errorf("workspace bind missing: %v", have)
	}
	if !hasBind("/tmp/fuse-ws", "/remote") {
		t.Errorf("identity alias bind missing: %v", have)
	}
	if !hasBind("/tmp/fuse-stubs", "/home/user/.cache/machineproxy/pathstub") {
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
