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

func TestLoadParsesAgentEnvConfig(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
agent:
  env_keep:
    - HOME
    - PATH
    - /^AWS_/
  env_remove:
    - MPROXY_*
    - /SECRET/
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Agent.EnvKeep) != 3 {
		t.Fatalf("expected 3 env_keep entries, got %d: %v", len(cfg.Agent.EnvKeep), cfg.Agent.EnvKeep)
	}
	if cfg.Agent.EnvKeep[2] != "/^AWS_/" {
		t.Fatalf("expected regex pattern /^AWS_/, got %s", cfg.Agent.EnvKeep[2])
	}
	if len(cfg.Agent.EnvRemove) != 2 {
		t.Fatalf("expected 2 env_remove entries, got %d: %v", len(cfg.Agent.EnvRemove), cfg.Agent.EnvRemove)
	}
}

func TestLoadDefaultsAgentEnv(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
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

	if len(cfg.Agent.EnvKeep) == 0 {
		t.Fatal("expected non-empty default agent.env_keep")
	}
	if len(cfg.Agent.EnvRemove) == 0 {
		t.Fatal("expected non-empty default agent.env_remove")
	}
	if len(cfg.Container.EnvRemove) == 0 {
		t.Fatal("expected non-empty default container.env_remove")
	}
}

func TestLoadParsesContainerEnvRemove(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
  env_remove:
    - MPROXY_*
    - /^UNSAFE_/
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Container.EnvRemove) != 2 {
		t.Fatalf("expected 2 container.env_remove entries, got %d", len(cfg.Container.EnvRemove))
	}
}

func TestCompileLocalCommandFullPath(t *testing.T) {
	if _, err := CompileLocalCommand("/usr/bin/env"); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestCompileLocalCommandRegex(t *testing.T) {
	if _, err := CompileLocalCommand("/^node/"); err != nil {
		t.Fatalf("expected valid regex, got %v", err)
	}
}

func TestCompileLocalCommandRegexInvalid(t *testing.T) {
	_, err := CompileLocalCommand("/[invalid/")
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestCompileLocalCommandBasename(t *testing.T) {
	if _, err := CompileLocalCommand("env"); err != nil {
		t.Fatalf("expected valid basename, got %v", err)
	}
}

func TestCompileLocalCommandMiddleSlashRejected(t *testing.T) {
	_, err := CompileLocalCommand("usr/bin/env")
	if err == nil {
		t.Fatal("expected error for middle slashes")
	}
	if !strings.Contains(err.Error(), "slashes in the middle") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCompileLocalCommandEmpty(t *testing.T) {
	_, err := CompileLocalCommand("")
	if err == nil {
		t.Fatal("expected error for empty entry")
	}
}

func TestCompileLocalCommandEmptyRegex(t *testing.T) {
	_, err := CompileLocalCommand("//")
	if err == nil {
		t.Fatal("expected error for empty regex")
	}
}

func TestMatchLocalCommandAbsolutePath(t *testing.T) {
	if !MatchLocalCommand("/usr/bin/env", "/usr/bin/env") {
		t.Fatal("expected exact absolute path to match")
	}
	if MatchLocalCommand("/usr/bin/env", "/usr/bin/other") {
		t.Fatal("expected different absolute path not to match")
	}
}

func TestMatchLocalCommandBasename(t *testing.T) {
	if !MatchLocalCommand("claude", "/opt/claude-code/bin/claude") {
		t.Fatal("expected basename to match full path")
	}
	if !MatchLocalCommand("env", "/usr/bin/env") {
		t.Fatal("expected basename to match")
	}
	if MatchLocalCommand("env", "/usr/bin/printenv") {
		t.Fatal("expected different basename not to match")
	}
}

func TestMatchLocalCommandRegex(t *testing.T) {
	if !MatchLocalCommand("/python/", "/usr/bin/python3.11") {
		t.Fatal("expected regex to match")
	}
	if MatchLocalCommand("/python/", "/usr/bin/node") {
		t.Fatal("expected regex not to match non-python path")
	}
}

func TestMatchLocalCommandInvalidRegex(t *testing.T) {
	if MatchLocalCommand("/[invalid/", "/usr/bin/foo") {
		t.Fatal("expected invalid regex to return false")
	}
}

func TestLoadAcceptsAllLocalCommandForms(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands:
    - /usr/bin/env
    - /^python.*/
    - node
  mounts:
    - /workspace
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Container.LocalCommands) != 3 {
		t.Fatalf("expected 3 local_commands, got %d", len(cfg.Container.LocalCommands))
	}
}

func TestLoadRejectsMiddleSlashLocalCommand(t *testing.T) {
	raw := `
remote:
  ssh:
    addr: 127.0.0.1:22
    user: dev
    private_key_path: /tmp/id_ed25519
container:
  local_commands:
    - usr/bin/env
  mounts:
    - /workspace
`

	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "slashes in the middle") {
		t.Fatalf("expected middle-slash validation error, got: %v", err)
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
