package pathstub

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func TestEnumerateLocally(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	mustWrite := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("data"), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(dir1, "exec1"), 0o755)
	mustWrite(filepath.Join(dir1, "not_exec"), 0o644)
	mustWrite(filepath.Join(dir1, "shared"), 0o755) // shadowed by dir1 entry
	if err := os.Mkdir(filepath.Join(dir1, "a_subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	mustWrite(filepath.Join(dir2, "exec2"), 0o755)
	mustWrite(filepath.Join(dir2, "shared"), 0o755) // shadowed: dir1 wins

	entries, err := EnumerateLocally([]string{dir1, "", "/does/not/exist", dir2})
	if err != nil {
		t.Fatalf("EnumerateLocally: %v", err)
	}

	got := make(map[string]string, len(entries))
	for _, e := range entries {
		got[e.Name] = e.RemotePath
	}

	want := map[string]string{
		"exec1":  filepath.Join(dir1, "exec1"),
		"shared": filepath.Join(dir1, "shared"), // first wins
		"exec2":  filepath.Join(dir2, "exec2"),
	}

	if len(got) != len(want) {
		// Build a stable list for the error message.
		names := make([]string, 0, len(got))
		for n := range got {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Fatalf("entry count: got %d %v, want %d", len(got), names, len(want))
	}
	for name, wantPath := range want {
		if got[name] != wantPath {
			t.Errorf("entry %q: got path %q, want %q", name, got[name], wantPath)
		}
	}

	if strings.Contains(got["exec1"], "not_exec") {
		t.Fatal("non-executable file leaked into enumeration")
	}
	if _, ok := got["a_subdir"]; ok {
		t.Fatal("subdirectory leaked into enumeration")
	}

	// Spot-check that mode/size/mtime are populated.
	for _, e := range entries {
		if e.Mode&0o100 == 0 {
			t.Errorf("entry %q has no owner-exec bit: mode=%#o", e.Name, e.Mode)
		}
		if e.Size <= 0 {
			t.Errorf("entry %q has size %d, want >0", e.Name, e.Size)
		}
		if e.MTimeNanos == 0 {
			t.Errorf("entry %q has zero mtime", e.Name)
		}
	}
}

func TestEnumerateLocallyEmptyPathFromEnv(t *testing.T) {
	t.Setenv("PATH", "")
	entries, err := EnumerateLocally(nil)
	if err != nil {
		t.Fatalf("EnumerateLocally: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries from empty PATH, got %d", len(entries))
	}
}

func TestFilterLocalCommands(t *testing.T) {
	entries := []agentproto.PathInfoEntry{
		{Name: "env", RemotePath: "/usr/bin/env"},
		{Name: "python3", RemotePath: "/usr/bin/python3"},
		{Name: "bash", RemotePath: "/usr/bin/bash"},
		{Name: "node", RemotePath: "/usr/bin/node"},
	}

	cases := []struct {
		name          string
		localCommands []string
		wantNames     []string
	}{
		{
			name:          "empty rules keep all",
			localCommands: nil,
			wantNames:     []string{"env", "python3", "bash", "node"},
		},
		{
			name:          "basename rule filters by name",
			localCommands: []string{"env"},
			wantNames:     []string{"python3", "bash", "node"},
		},
		{
			name:          "multiple basename rules",
			localCommands: []string{"env", "bash"},
			wantNames:     []string{"python3", "node"},
		},
		{
			name:          "regex rule matching substring of synthetic path",
			localCommands: []string{"/python/"},
			wantNames:     []string{"env", "bash", "node"},
		},
		{
			name: "absolute-path rule does NOT shadow stubs",
			// /usr/bin/env is precise: only the literal local /usr/bin/env
			// runs locally. The stub at /var/lib/machineproxy/path-stub/env
			// is a different path and stays.
			localCommands: []string{"/usr/bin/env"},
			wantNames:     []string{"env", "python3", "bash", "node"},
		},
		{
			name:          "mixed rules",
			localCommands: []string{"/usr/bin/env", "bash", "/^.*node$/"},
			wantNames:     []string{"env", "python3"},
		},
		{
			name: "invalid rule is skipped",
			// Trailing /invalid is a malformed regex; the entry is dropped
			// silently and the basename rule still applies.
			localCommands: []string{"/[invalid/", "bash"},
			wantNames:     []string{"env", "python3", "node"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Copy entries so the in-place filter doesn't affect later cases.
			in := make([]agentproto.PathInfoEntry, len(entries))
			copy(in, entries)

			out := FilterLocalCommands(in, tc.localCommands)
			got := make([]string, len(out))
			for i, e := range out {
				got[i] = e.Name
			}
			if !equalStringSlice(got, tc.wantNames) {
				t.Fatalf("FilterLocalCommands names = %v, want %v", got, tc.wantNames)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
