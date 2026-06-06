package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRewritePathPrefix(t *testing.T) {
	cases := []struct {
		name        string
		p, from, to string
		want        string
		wantMatch   bool
	}{
		{"exact root", "/ws", "/ws", "/remote", "/remote", true},
		{"subpath", "/ws/foo/bar", "/ws", "/remote", "/remote/foo/bar", true},
		{"no match sibling", "/wsX", "/ws", "/remote", "/wsX", false},
		{"no match outside", "/other", "/ws", "/remote", "/other", false},
		{"empty from", "/ws/foo", "", "/remote", "/ws/foo", false},
		{"to with trailing component preserved", "/ws/a/b", "/ws", "/home/u/proj", "/home/u/proj/a/b", true},
		{"identity from==to root", "/ws", "/ws", "/ws", "/ws", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, match := RewritePathPrefix(c.p, c.from, c.to)
			if got != c.want || match != c.wantMatch {
				t.Fatalf("RewritePathPrefix(%q,%q,%q) = (%q,%v), want (%q,%v)",
					c.p, c.from, c.to, got, match, c.want, c.wantMatch)
			}
		})
	}
}

// loadCwd builds a minimal valid config with the given cwd_mode / working_dir
// and returns the Validate (via Finalize) outcome.
func loadCwd(t *testing.T, cwdMode, workingDir string) (*Config, error) {
	t.Helper()
	var wd string
	if workingDir != "" {
		wd = fmt.Sprintf("\n  working_dir: %s", workingDir)
	}
	raw := fmt.Sprintf(`
remote:
  ssh:
    host: 127.0.0.1
container:
  cwd_mode: %s%s
  mounts:
    - /ws:/remote/ws
`, cwdMode, wd)
	return Load(strings.NewReader(raw))
}

func TestValidateCwdMode(t *testing.T) {
	cases := []struct {
		name       string
		cwdMode    string
		workingDir string
		wantErr    string // substring; "" means no error
	}{
		{"inherit ok", "inherit", "", ""},
		{"local ok", "local", "", ""},
		{"remote ok", "remote", "", ""},
		{"explicit with workdir ok", "explicit", "/ws", ""},
		{"explicit without workdir", "explicit", "", "working_dir is required"},
		{"unknown mode", "bogus", "", "cwd_mode must be one of"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadCwd(t, c.cwdMode, c.workingDir)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("error = %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestValidateCwdRemap(t *testing.T) {
	load := func(remap string) (*Config, error) {
		raw := fmt.Sprintf(`
remote:
  ssh:
    host: 127.0.0.1
container:
  cwd_remap: %s
  mounts:
    - /ws:/remote/ws
`, remap)
		return Load(strings.NewReader(raw))
	}
	if _, err := load("local"); err != nil {
		t.Fatalf("local: unexpected error: %v", err)
	}
	if _, err := load("remote"); err != nil {
		t.Fatalf("remote: unexpected error: %v", err)
	}
	if _, err := load("bogus"); err == nil || !strings.Contains(err.Error(), "cwd_remap must be one of") {
		t.Fatalf("bogus: error = %v, want substring %q", err, "cwd_remap must be one of")
	}
}

func TestCwdDefaults(t *testing.T) {
	raw := `
remote:
  ssh:
    host: 127.0.0.1
container:
  mounts:
    - /ws:/remote/ws
`
	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Container.CwdMode != CwdModeInherit {
		t.Fatalf("CwdMode = %q, want %q", cfg.Container.CwdMode, CwdModeInherit)
	}
	if cfg.Container.CwdRemap != CwdRemapLocal {
		t.Fatalf("CwdRemap = %q, want %q", cfg.Container.CwdRemap, CwdRemapLocal)
	}
}
