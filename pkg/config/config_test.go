package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsEmptyLocalCommands(t *testing.T) {
	raw := `
ssh:
  addr: 127.0.0.1:22
  user: dev
  private_key_path: /tmp/id_ed25519
workspace:
  remote_path: /workspace
exec:
  local_commands: []
  shim_path: /opt/mproxy-shim
`

	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "exec.local_commands") {
		t.Fatalf("expected local_commands validation error, got: %v", err)
	}
}

func TestLoadParsesConfig(t *testing.T) {
	raw := `
ssh:
  addr: 127.0.0.1:22
  user: dev
  private_key_path: /tmp/id_ed25519
  keep_alive: 15s
workspace:
  remote_path: /workspace
exec:
  local_commands:
    - /usr/bin/env
  shim_path: /opt/mproxy-shim
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.SSH.KeepAlive.String() != "15s" {
		t.Fatalf("expected keepalive 15s, got %s", cfg.SSH.KeepAlive)
	}
	if len(cfg.Exec.LocalCommands) != 1 || cfg.Exec.LocalCommands[0] != "/usr/bin/env" {
		t.Fatalf("unexpected local_commands: %#v", cfg.Exec.LocalCommands)
	}
}
