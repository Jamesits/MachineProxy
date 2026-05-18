package initcmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func testLog(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLookPathFindsExecutable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := LookPath(testLog(t), "hello", dir)
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != bin {
		t.Fatalf("LookPath: got %q want %q", got, bin)
	}
}

func TestLookPathSkipsDirectory(t *testing.T) {
	skipDir := t.TempDir()
	keepDir := t.TempDir()
	skipped := filepath.Join(skipDir, "hello")
	kept := filepath.Join(keepDir, "hello")
	for _, p := range []string{skipped, kept} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	got, err := LookPath(testLog(t), "hello", skipDir+":"+keepDir, skipDir)
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != kept {
		t.Fatalf("LookPath: got %q want %q (must skip %q)", got, kept, skipDir)
	}
}

func TestLookPathSkipsNonExec(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello")
	if err := os.WriteFile(bin, []byte("not executable"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LookPath(testLog(t), "hello", dir); err == nil {
		t.Fatalf("LookPath unexpectedly succeeded for non-exec file")
	}
}

func TestLookPathAbsolutePassthrough(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hello")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := LookPath(testLog(t), bin, "/nonexistent")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != bin {
		t.Fatalf("LookPath: got %q want %q", got, bin)
	}
}

func TestLookPathEmptyName(t *testing.T) {
	if _, err := LookPath(testLog(t), "", "/usr/bin"); err == nil {
		t.Fatalf("LookPath(\"\") expected error")
	}
}

func TestDeriveLocalCommandsBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakebin")
	// 0x7fELF magic — definitely not a shebang.
	if err := os.WriteFile(bin, []byte{0x7f, 'E', 'L', 'F', 0x02}, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), bin)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	if len(got) != 1 || got[0] != bin {
		t.Fatalf("DeriveLocalCommands: got %v want [%q]", got, bin)
	}
}

func TestDeriveLocalCommandsDirectShebang(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), script)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	want := []string{script, "/bin/bash"}
	if !stringSliceEqual(got, want) {
		t.Fatalf("DeriveLocalCommands: got %v want %v", got, want)
	}
}

func TestDeriveLocalCommandsEnvShebang(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\nprint('hi')\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), script)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	want := []string{script, "/usr/bin/env", "python3"}
	if !stringSliceEqual(got, want) {
		t.Fatalf("DeriveLocalCommands: got %v want %v", got, want)
	}
}

func TestDeriveLocalCommandsEnvSplitShebang(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env -S python3 -u\nprint('hi')\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), script)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	want := []string{script, "/usr/bin/env", "python3"}
	if !stringSliceEqual(got, want) {
		t.Fatalf("DeriveLocalCommands: got %v want %v", got, want)
	}
}

func TestDeriveLocalCommandsEnvUnsetSkipsFlagArg(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env -u TZ bash\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), script)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	want := []string{script, "/usr/bin/env", "bash"}
	if !stringSliceEqual(got, want) {
		t.Fatalf("DeriveLocalCommands: got %v want %v", got, want)
	}
}

func TestDeriveLocalCommandsExtraWhitespace(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(script, []byte("#!   /bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), script)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	want := []string{script, "/bin/bash"}
	if !stringSliceEqual(got, want) {
		t.Fatalf("DeriveLocalCommands: got %v want %v", got, want)
	}
}

func TestDeriveLocalCommandsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "empty")
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := DeriveLocalCommands(testLog(t), bin)
	if err != nil {
		t.Fatalf("DeriveLocalCommands: %v", err)
	}
	if len(got) != 1 || got[0] != bin {
		t.Fatalf("DeriveLocalCommands: got %v want [%q]", got, bin)
	}
}

func TestDeriveLocalCommandsUnreadable(t *testing.T) {
	got, err := DeriveLocalCommands(testLog(t), "/this/path/does/not/exist")
	if err == nil {
		t.Fatalf("DeriveLocalCommands: expected error")
	}
	if len(got) != 1 || got[0] != "/this/path/does/not/exist" {
		t.Fatalf("DeriveLocalCommands: got %v; first entry must still be the input path", got)
	}
}

func stringSliceEqual(a, b []string) bool {
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

// Sanity check that envTarget handles unfamiliar inputs without panicking.
func TestEnvTargetEdgeCases(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"   ":            "",
		"-i":             "",
		"-u":             "",
		"-u FOO":         "",
		"-u FOO bash":    "bash",
		"-S python3 -u":  "python3",
		"--ignore-foo a": "a",
	}
	for in, want := range cases {
		if got := envTarget(in); got != want {
			t.Errorf("envTarget(%q) = %q, want %q", in, got, want)
		}
	}
}
