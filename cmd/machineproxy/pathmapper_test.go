package main

import (
	"testing"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/pathstub"
)

func TestBuildPathMapperNilWhenNothingToTranslate(t *testing.T) {
	// Only identity mounts and no stubs: nothing to rewrite.
	m := buildPathMapper([]config.Mount{
		{ContainerPath: "/remote", RemotePath: "/remote"},
	}, "/stub", nil)
	if m != nil {
		t.Fatalf("expected nil mapper, got non-nil")
	}
}

func TestBuildPathMapperMultipleMounts(t *testing.T) {
	mounts := []config.Mount{
		{ContainerPath: "/localA", RemotePath: "/remote"},          // workspace
		{ContainerPath: "/remote", RemotePath: "/remote"},          // auto-identity alias
		{ContainerPath: "/other/local", RemotePath: "/other/rem"},  // distinct remote
		{ContainerPath: "/localA/nested", RemotePath: "/deep/nst"}, // nested, more specific
	}
	m := buildPathMapper(mounts, "/stub", nil)
	if m == nil {
		t.Fatal("expected non-nil mapper")
	}

	cases := []struct{ in, want string }{
		// Workspace prefix rewrite, container -> remote.
		{"/localA", "/remote"},
		{"/localA/src/main.go", "/remote/src/main.go"},
		// Identity alias maps to itself (no rewrite, already remote form).
		{"/remote", "/remote"},
		{"/remote/src/main.go", "/remote/src/main.go"},
		// Second distinct mount.
		{"/other/local/x", "/other/rem/x"},
		// Most-specific prefix wins: /localA/nested must beat /localA.
		{"/localA/nested", "/deep/nst"},
		{"/localA/nested/a.txt", "/deep/nst/a.txt"},
		// Unrelated path passes through.
		{"/usr/bin/env", "/usr/bin/env"},
		// Prefix must match whole components, not substrings.
		{"/localABC", "/localABC"},
	}
	for _, c := range cases {
		if got := m(c.in); got != c.want {
			t.Errorf("mapper(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildPathMapperStubTakesPrecedence(t *testing.T) {
	stubMap := map[string]pathstub.Entry{
		"git": {Name: "git", RemotePath: "/usr/bin/git"},
	}
	m := buildPathMapper([]config.Mount{
		{ContainerPath: "/localA", RemotePath: "/remote"},
	}, "/stub", stubMap)
	if m == nil {
		t.Fatal("expected non-nil mapper")
	}
	if got := m("/stub/git"); got != "/usr/bin/git" {
		t.Errorf("stub lookup: got %q, want /usr/bin/git", got)
	}
	// A stub-dir path with no matching entry falls through to mount rewrite
	// (here it matches nothing, so it passes through unchanged).
	if got := m("/stub/unknown"); got != "/stub/unknown" {
		t.Errorf("unknown stub: got %q, want /stub/unknown", got)
	}
	// Mount rewrite still works alongside stubs.
	if got := m("/localA/x"); got != "/remote/x" {
		t.Errorf("mount rewrite: got %q, want /remote/x", got)
	}
}
