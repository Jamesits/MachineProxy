package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/jamesits/machineproxy/pkg/config"
)

func newCwdDeps(mode, remap, workingDir string) *runtimeDeps {
	cfg := &config.Config{}
	cfg.Container.CwdMode = mode
	cfg.Container.CwdRemap = remap
	cfg.Container.WorkingDir = workingDir
	return &runtimeDeps{
		cfg:            cfg,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaceMount: config.Mount{ContainerPath: "/ws", RemotePath: "/remote/ws"},
	}
}

func TestResolveWorkingDir(t *testing.T) {
	realDir := t.TempDir() // exists, is a directory, absolute

	cases := []struct {
		name       string
		mode       string
		workingDir string
		want       string
		wantErr    bool
	}{
		{"inherit -> container path", config.CwdModeInherit, "", "/ws", false},
		{"local -> container path", config.CwdModeLocal, "", "/ws", false},
		{"remote -> remote path", config.CwdModeRemote, "", "/remote/ws", false},
		{"working_dir wins over inherit", config.CwdModeInherit, "/custom", "/custom", false},
		{"working_dir wins over remote", config.CwdModeRemote, "/custom", "/custom", false},
		{"explicit valid dir", config.CwdModeExplicit, realDir, realDir, false},
		{"explicit missing dir", config.CwdModeExplicit, "/no/such/dir", "", true},
		{"explicit empty working_dir", config.CwdModeExplicit, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newCwdDeps(c.mode, config.CwdRemapLocal, c.workingDir)
			got, err := d.resolveWorkingDir()
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveCwdRemap(t *testing.T) {
	cases := []struct {
		name     string
		remap    string
		mount    config.Mount
		wantFrom string
		wantTo   string
	}{
		{"local disables hook", config.CwdRemapLocal, config.Mount{ContainerPath: "/ws", RemotePath: "/remote/ws"}, "", ""},
		{"remote enables hook", config.CwdRemapRemote, config.Mount{ContainerPath: "/ws", RemotePath: "/remote/ws"}, "/ws", "/remote/ws"},
		{"remote no-op when paths coincide", config.CwdRemapRemote, config.Mount{ContainerPath: "/ws", RemotePath: "/ws"}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := newCwdDeps(config.CwdModeInherit, c.remap, "")
			d.workspaceMount = c.mount
			d.resolveCwdRemap()
			if d.cwdFrom != c.wantFrom || d.cwdTo != c.wantTo {
				t.Fatalf("got from=%q to=%q, want from=%q to=%q", d.cwdFrom, d.cwdTo, c.wantFrom, c.wantTo)
			}
		})
	}
}
