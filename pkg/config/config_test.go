package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAcceptsEmptyLocalCommands(t *testing.T) {
	// Empty local_commands is valid: it means every exec is routed to
	// the remote. Minimal configs (e.g. those written by CI) rely on this.
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
container:
  local_commands: []
  mounts:
    - /workspace
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Container.LocalCommands) != 0 {
		t.Fatalf("expected empty local_commands, got: %#v", cfg.Container.LocalCommands)
	}
}

func TestLoadParsesConfig(t *testing.T) {
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
    port: 2222
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

	if cfg.Remote.SSH.Host != "127.0.0.1" {
		t.Fatalf("expected host 127.0.0.1, got %s", cfg.Remote.SSH.Host)
	}
	if cfg.Remote.SSH.Port != 2222 {
		t.Fatalf("expected port 2222, got %d", cfg.Remote.SSH.Port)
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
    host: 127.0.0.1
    user: dev
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
    host: 127.0.0.1
    user: dev
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
    host: 127.0.0.1
    user: dev
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
    host: 127.0.0.1
    user: dev
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
    host: 127.0.0.1
    user: dev
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

func TestParseMountExpandsLocalTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	m, err := ParseMount("~/work:/remote/work")
	if err != nil {
		t.Fatalf("ParseMount() error = %v", err)
	}
	wantLocal := filepath.Join(home, "work")
	if m.ContainerPath != wantLocal {
		t.Fatalf("ContainerPath = %q, want %q", m.ContainerPath, wantLocal)
	}
	if m.RemotePath != "/remote/work" {
		t.Fatalf("RemotePath = %q, want unchanged absolute", m.RemotePath)
	}
}

func TestParseMountKeepsRemoteTildeRaw(t *testing.T) {
	m, err := ParseMount("/local/work:~/remote-home/work")
	if err != nil {
		t.Fatalf("ParseMount() error = %v", err)
	}
	if m.ContainerPath != "/local/work" {
		t.Fatalf("ContainerPath = %q, want unchanged absolute", m.ContainerPath)
	}
	if m.RemotePath != "~/remote-home/work" {
		t.Fatalf("RemotePath = %q, want raw ~/... form", m.RemotePath)
	}
}

func TestParseMountSingleTildePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	m, err := ParseMount("~/workspace")
	if err != nil {
		t.Fatalf("ParseMount() error = %v", err)
	}
	wantLocal := filepath.Join(home, "workspace")
	if m.ContainerPath != wantLocal {
		t.Fatalf("ContainerPath = %q, want %q", m.ContainerPath, wantLocal)
	}
	if m.RemotePath != "~/workspace" {
		t.Fatalf("RemotePath = %q, want raw ~/workspace", m.RemotePath)
	}
}

func TestExpandRemoteHome(t *testing.T) {
	cases := []struct {
		name string
		in   string
		home string
		want string
		err  bool
	}{
		{"absolute path unchanged", "/foo/bar", "/home/u", "/foo/bar", false},
		{"empty unchanged", "", "/home/u", "", false},
		{"tilde alone", "~", "/home/u", "/home/u", false},
		{"tilde slash", "~/foo", "/home/u", "/home/u/foo", false},
		{"tilde nested", "~/a/b/c", "/home/u", "/home/u/a/b/c", false},
		{"empty home errors", "~/foo", "", "", true},
		{"middle tilde unchanged", "/foo/~/bar", "/home/u", "/foo/~/bar", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandRemoteHome(tc.in, tc.home)
			if tc.err {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpandLocalHomePassthrough(t *testing.T) {
	got, err := ExpandLocalHome("/already/absolute")
	if err != nil {
		t.Fatalf("ExpandLocalHome() error = %v", err)
	}
	if got != "/already/absolute" {
		t.Fatalf("got %q, want unchanged", got)
	}
}

func TestLoadExpandsLocalPathsInConfig(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
  working_dir: ~/proj
  path_stub_dir: ~/.cache/stub
components:
  shim_path: ~/bin/mproxy-shim
  tracer_path: ~/bin/mproxy-tracer
  agent_local_path: ~/bin/mproxy-agent
  agent_remote_path: ~/remote/mproxy-agent
recording:
  path: ~/recordings/session
`
	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, c := range []struct {
		name, got, want string
	}{
		{"working_dir", cfg.Container.WorkingDir, filepath.Join(home, "proj")},
		{"path_stub_dir", cfg.Container.PathStubDir, filepath.Join(home, ".cache/stub")},
		{"shim_path", cfg.Components.ShimPath, filepath.Join(home, "bin/mproxy-shim")},
		{"tracer_path", cfg.Components.TracerPath, filepath.Join(home, "bin/mproxy-tracer")},
		{"agent_local_path", cfg.Components.AgentLocalPath, filepath.Join(home, "bin/mproxy-agent")},
		{"recording.path", cfg.Recording.Path, filepath.Join(home, "recordings/session")},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	// AgentRemotePath must stay tilde-prefixed for SFTP-time expansion.
	if cfg.Components.AgentRemotePath != "~/remote/mproxy-agent" {
		t.Errorf("agent_remote_path = %q, want raw ~/remote/mproxy-agent",
			cfg.Components.AgentRemotePath)
	}
}

func TestParseMountRejectsEmpty(t *testing.T) {
	_, err := ParseMount("")
	if err == nil {
		t.Fatal("expected error for empty mount")
	}
}

func TestLoadDefaultsPathProxy(t *testing.T) {
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
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
	if cfg.Container.PathProxy != "prepend" {
		t.Fatalf("expected default path_proxy=prepend, got %q", cfg.Container.PathProxy)
	}
}

func TestLoadAcceptsPathProxyValues(t *testing.T) {
	for _, v := range []string{"prepend", "append", "disabled"} {
		raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
  path_proxy: ` + v + `
`
		cfg, err := Load(strings.NewReader(raw))
		if err != nil {
			t.Fatalf("Load(%q) error = %v", v, err)
		}
		if cfg.Container.PathProxy != v {
			t.Fatalf("expected path_proxy=%q, got %q", v, cfg.Container.PathProxy)
		}
	}
}

func TestLoadRejectsPathProxyValue(t *testing.T) {
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
container:
  local_commands:
    - /usr/bin/env
  mounts:
    - /workspace
  path_proxy: yolo
`
	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "path_proxy") {
		t.Fatalf("expected path_proxy validation error, got: %v", err)
	}
}

func TestLoadRejectsEmptyMounts(t *testing.T) {
	raw := `
remote:
  ssh:
    host: 127.0.0.1
    user: dev
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

func TestLoadParsesTOMLConfig(t *testing.T) {
	raw := `
log_level = "debug"

[remote]
os = "linux"
arch = "arm64"

[remote.ssh]
host = "127.0.0.1"
user = "dev"
port = 2222

[container]
local_commands = ["/usr/bin/env", "/^python.*/"]
mounts = ["/workspace", "/local:/remote"]
`

	cfg, err := Load(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("expected log_level debug, got %q", cfg.LogLevel)
	}
	if cfg.Remote.SSH.Host != "127.0.0.1" || cfg.Remote.SSH.Port != 2222 {
		t.Fatalf("unexpected ssh: %+v", cfg.Remote.SSH)
	}
	if cfg.Remote.OS != "linux" || cfg.Remote.Arch != "arm64" {
		t.Fatalf("unexpected os/arch: %s/%s", cfg.Remote.OS, cfg.Remote.Arch)
	}
	if len(cfg.Container.LocalCommands) != 2 || len(cfg.Container.Mounts) != 2 {
		t.Fatalf("unexpected container: %+v", cfg.Container)
	}
}

func TestLoadTOMLRejectsUnknownFields(t *testing.T) {
	raw := `
log_level = "info"
mystery_field = "nope"

[remote.ssh]
host = "127.0.0.1"

[container]
local_commands = ["/usr/bin/env"]
mounts = ["/workspace"]
`
	_, err := Load(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "toml") {
		t.Fatalf("expected toml decode error for unknown field, got: %v", err)
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Format
	}{
		{"yaml mapping", "remote:\n  ssh:\n    host: x\n", FormatYAML},
		{"yaml with inline array", "key: [a, b]\n", FormatYAML},
		{"yaml with leading comments", "# a\n# b\nkey: v\n", FormatYAML},
		{"toml section header", "[remote]\nhost = \"x\"\n", FormatTOML},
		{"toml double-bracket", "[[servers]]\nname = \"x\"\n", FormatTOML},
		{"toml with leading kv", "log_level = \"info\"\n\n[remote]\n", FormatTOML},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFormat([]byte(tc.in)); got != tc.want {
				t.Fatalf("detectFormat(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLoadFileDetectsByExtension(t *testing.T) {
	dir := t.TempDir()

	// TOML body with no section header — only extension can pick TOML.
	tomlOnly := `log_level = "warn"
[remote.ssh]
host = "1.2.3.4"
[container]
local_commands = ["/usr/bin/env"]
mounts = ["/workspace"]
`
	tomlPath := filepath.Join(dir, "cfg.toml")
	if err := os.WriteFile(tomlPath, []byte(tomlOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(tomlPath)
	if err != nil {
		t.Fatalf("LoadFile(toml) error = %v", err)
	}
	if cfg.Remote.SSH.Host != "1.2.3.4" || cfg.LogLevel != "warn" {
		t.Fatalf("unexpected toml decode: %+v / %s", cfg.Remote.SSH, cfg.LogLevel)
	}

	yamlBody := `remote:
  ssh:
    host: 5.6.7.8
container:
  local_commands: [/usr/bin/env]
  mounts: [/workspace]
`
	yamlPath := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadFile(yamlPath)
	if err != nil {
		t.Fatalf("LoadFile(yaml) error = %v", err)
	}
	if cfg.Remote.SSH.Host != "5.6.7.8" {
		t.Fatalf("unexpected yaml decode host: %q", cfg.Remote.SSH.Host)
	}
}

func TestLoadParsesJSONConfig(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "log_level": "debug",
  "remote": {
    "ssh": {"host": "10.0.0.1", "user": "dev", "port": 2222},
    "os": "linux",
    "arch": "amd64"
  },
  "container": {
    "local_commands": ["/usr/bin/env"],
    "mounts": ["/workspace"]
  }
}
`
	path := filepath.Join(dir, "cfg.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(json) error = %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("expected log_level debug, got %q", cfg.LogLevel)
	}
	if cfg.Remote.SSH.Host != "10.0.0.1" || cfg.Remote.SSH.Port != 2222 {
		t.Fatalf("unexpected ssh: %+v", cfg.Remote.SSH)
	}
	if len(cfg.Container.LocalCommands) != 1 || cfg.Container.LocalCommands[0] != "/usr/bin/env" {
		t.Fatalf("unexpected local_commands: %#v", cfg.Container.LocalCommands)
	}
}

func TestLoadFileFallsBackToContentSniff(t *testing.T) {
	dir := t.TempDir()
	body := `[remote.ssh]
host = "9.9.9.9"
[container]
local_commands = ["/usr/bin/env"]
mounts = ["/workspace"]
`
	// No recognized extension — must sniff and pick TOML.
	path := filepath.Join(dir, "cfg.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile sniff error = %v", err)
	}
	if cfg.Remote.SSH.Host != "9.9.9.9" {
		t.Fatalf("unexpected sniffed host: %q", cfg.Remote.SSH.Host)
	}
}
