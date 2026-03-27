package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsEmptyLocalCommands(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands: []
  mounts:
    - /workspace
`

	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "container.local_commands") {
		t.Fatalf("expected local_commands validation error, got: %v", err)
	}
}

func TestLoadParsesConfig(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
    keep_alive: 15s
  os: linux
  arch: arm64
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Remote.SSH.KeepAlive.String() != "15s" {
		t.Fatalf("expected keepalive 15s, got %s", cfg.Remote.SSH.KeepAlive)
	}
	if len(cfg.Container.LocalCommands) != 1 || cfg.Container.LocalCommands[0] != "/usr/bin/env" {
		t.Fatalf("unexpected local_commands: %#v", cfg.Container.LocalCommands)
	}
	if cfg.Remote.OS != "linux" || cfg.Remote.Arch != "arm64" {
		t.Fatalf("unexpected remote os/arch: %s/%s", cfg.Remote.OS, cfg.Remote.Arch)
	}
}

func TestParseMountRemoteOnly(t *testing.T) {
	m, err := ParseMount("/workspace")
	if err != nil {
		t.Fatalf("ParseMount() error = %v", err)
	}
	if m.RemotePath != "/workspace" || m.ContainerPath != "/workspace" {
		t.Fatalf("unexpected mount: %+v", m)
	}
}

func TestParseMountLocalRemote(t *testing.T) {
	m, err := ParseMount("/local/path:/remote/path")
	if err != nil {
		t.Fatalf("ParseMount() error = %v", err)
	}
	if m.RemotePath != "/remote/path" || m.ContainerPath != "/local/path" {
		t.Fatalf("unexpected mount: %+v", m)
	}
}

func TestParseMountRejectsRelative(t *testing.T) {
	_, err := ParseMount("relative/path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestParseMountRejectsEmpty(t *testing.T) {
	_, err := ParseMount("")
	if err == nil {
		t.Fatal("expected error for empty mount")
	}
}

func TestLoadRejectsEmptyMounts(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands:
    - /usr/bin/env
  mounts: []
`

	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "container.mounts") {
		t.Fatalf("expected mounts validation error, got: %v", err)
	}
}
