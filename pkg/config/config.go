package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Mount represents a parsed container mount entry in docker-compose style.
// Format: [local_path:]remote_path
type Mount struct {
	// RemotePath is the path on the remote machine (FUSE source).
	RemotePath string
	// ContainerPath is the path inside the container (bind target).
	// Equals RemotePath when no explicit local path is given.
	ContainerPath string
}

// ParseMount parses a docker-compose style mount string.
func ParseMount(s string) (Mount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Mount{}, errors.New("mount entry must not be empty")
	}
	if i := strings.Index(s, ":"); i >= 0 {
		local := s[:i]
		remote := s[i+1:]
		if local == "" || remote == "" {
			return Mount{}, fmt.Errorf("invalid mount %q: both local and remote paths are required around ':'", s)
		}
		if !filepath.IsAbs(local) || !filepath.IsAbs(remote) {
			return Mount{}, fmt.Errorf("mount paths must be absolute: %q", s)
		}
		return Mount{RemotePath: remote, ContainerPath: local}, nil
	}
	if !filepath.IsAbs(s) {
		return Mount{}, fmt.Errorf("mount path must be absolute: %q", s)
	}
	return Mount{RemotePath: s, ContainerPath: s}, nil
}

// Config describes machineproxy runtime behavior.
type Config struct {
	LogLevel string `yaml:"log_level" toml:"log_level" json:"log_level"` // trace, debug, info, warn, error

	Remote struct {
		// SSH is resolved via the user's ssh_config (see ssh_config(5)).
		// Host accepts either a literal hostname/IP or a Host alias defined
		// in ~/.ssh/config; User and Port, when set, override the resolved
		// values from ssh_config.
		SSH struct {
			Host string `yaml:"host" toml:"host" json:"host"`
			User string `yaml:"user" toml:"user" json:"user"`
			Port int    `yaml:"port" toml:"port" json:"port"`
		} `yaml:"ssh" toml:"ssh" json:"ssh"`
		OS   string `yaml:"os" toml:"os" json:"os"`       // remote OS for agent binary resolution; defaults to runtime.GOOS
		Arch string `yaml:"arch" toml:"arch" json:"arch"` // remote arch for agent binary resolution; defaults to runtime.GOARCH
	} `yaml:"remote" toml:"remote" json:"remote"`

	Container struct {
		LocalCommands []string `yaml:"local_commands" toml:"local_commands" json:"local_commands"`
		Mounts        []string `yaml:"mounts" toml:"mounts" json:"mounts"`                   // docker-compose style: [local:]remote
		WorkingDir    string   `yaml:"working_dir" toml:"working_dir" json:"working_dir"`    // override container working directory; defaults to first mount's local path
		EnvRemove     []string `yaml:"env_remove" toml:"env_remove" json:"env_remove"`       // glob/regex patterns for env vars to strip from the container process
	} `yaml:"container" toml:"container" json:"container"`

	Agent struct {
		EnvKeep   []string `yaml:"env_keep" toml:"env_keep" json:"env_keep"`         // glob/regex patterns for inherited env vars to forward to remote
		EnvRemove []string `yaml:"env_remove" toml:"env_remove" json:"env_remove"`   // glob/regex patterns for env vars to always strip from remote
	} `yaml:"agent" toml:"agent" json:"agent"`

	Components struct {
		ShimPath        string `yaml:"shim_path" toml:"shim_path" json:"shim_path"`
		TracerPath      string `yaml:"tracer_path" toml:"tracer_path" json:"tracer_path"`
		AgentLocalPath  string `yaml:"agent_local_path" toml:"agent_local_path" json:"agent_local_path"`
		AgentRemotePath string `yaml:"agent_remote_path" toml:"agent_remote_path" json:"agent_remote_path"`
	} `yaml:"components" toml:"components" json:"components"`

	Recording struct {
		Path string `yaml:"path" toml:"path" json:"path"` // empty disables recording
	} `yaml:"recording" toml:"recording" json:"recording"`
}

func (c *Config) applyDefaults() error {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Remote.OS == "" {
		c.Remote.OS = runtime.GOOS
	}
	if c.Remote.Arch == "" {
		c.Remote.Arch = runtime.GOARCH
	}
	if c.Components.AgentRemotePath == "" {
		c.Components.AgentRemotePath = "/tmp/mproxy-agent"
	}
	if len(c.Container.EnvRemove) == 0 {
		c.Container.EnvRemove = []string{
			"MPROXY_*",
		}
	}
	if len(c.Agent.EnvKeep) == 0 {
		c.Agent.EnvKeep = []string{
			"HOME", "PATH", "TERM", "LANG", "LC_*",
			"USER", "LOGNAME", "SHELL",
			"EDITOR", "VISUAL", "PAGER",
			"TZ", "DISPLAY", "SSH_AUTH_SOCK", "XDG_*",
		}
	}
	if len(c.Agent.EnvRemove) == 0 {
		c.Agent.EnvRemove = []string{
			"LD_PRELOAD", "LD_LIBRARY_PATH",
			"MPROXY_*",
		}
	}
	if c.Components.ShimPath != "" {
		if !filepath.IsAbs(c.Components.ShimPath) {
			return errors.New("components.shim_path must be absolute")
		}
	}
	if c.Components.TracerPath != "" {
		if !filepath.IsAbs(c.Components.TracerPath) {
			return errors.New("components.tracer_path must be absolute")
		}
	}
	return nil
}

func (c *Config) Validate() error {
	switch c.LogLevel {
	case "trace", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log_level must be one of trace, debug, info, warn, error; got %q", c.LogLevel)
	}
	if c.Remote.SSH.Host == "" {
		return errors.New("remote.ssh.host is required")
	}
	if c.Remote.SSH.Port < 0 || c.Remote.SSH.Port > 65535 {
		return fmt.Errorf("remote.ssh.port must be between 0 and 65535; got %d", c.Remote.SSH.Port)
	}
	if len(c.Container.Mounts) == 0 {
		return errors.New("container.mounts must not be empty")
	}
	for _, m := range c.Container.Mounts {
		if _, err := ParseMount(m); err != nil {
			return fmt.Errorf("container.mounts: %w", err)
		}
	}
	if c.Container.WorkingDir != "" && !filepath.IsAbs(c.Container.WorkingDir) {
		return errors.New("container.working_dir must be absolute")
	}
	if len(c.Container.LocalCommands) == 0 {
		return errors.New("container.local_commands must not be empty")
	}
	for _, p := range c.Container.LocalCommands {
		if _, err := CompileLocalCommand(p); err != nil {
			return fmt.Errorf("container.local_commands: %w", err)
		}
	}
	return nil
}

// LocalCommandRule is a compiled local_commands entry that can match
// against executable pathnames. Supported forms:
//   - "/absolute/path"  — exact full path match
//   - "/regex/"         — regex matched against the full pathname
//   - "basename"        — matched against the last segment of the path
type LocalCommandRule struct {
	raw   string
	regex *regexp.Regexp // non-nil for /regex/ entries
	abs   string         // non-empty for /absolute/path entries
	base  string         // non-empty for basename entries
}

// CompileLocalCommand parses and validates a local_commands entry,
// returning a rule that can match pathnames.
func CompileLocalCommand(s string) (*LocalCommandRule, error) {
	if s == "" {
		return nil, errors.New("entry must not be empty")
	}

	startsSlash := strings.HasPrefix(s, "/")
	endsSlash := strings.HasSuffix(s, "/") && len(s) > 1

	switch {
	// /regex/ — delimited regex pattern
	case startsSlash && endsSlash:
		pattern := s[1 : len(s)-1]
		if pattern == "" {
			return nil, fmt.Errorf("regex pattern must not be empty: %q", s)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex in %q: %w", s, err)
		}
		return &LocalCommandRule{raw: s, regex: re}, nil

	// /absolute/path — full path match
	case startsSlash && !endsSlash:
		if !filepath.IsAbs(s) {
			return nil, fmt.Errorf("entry must be an absolute path: %q", s)
		}
		return &LocalCommandRule{raw: s, abs: s}, nil

	// basename — match against last segment of the executable path
	case !startsSlash && !strings.Contains(s, "/"):
		return &LocalCommandRule{raw: s, base: s}, nil

	// Reject: slashes only in the middle (e.g. "usr/bin/env")
	default:
		return nil, fmt.Errorf("entry %q has slashes in the middle; use an absolute path (/usr/bin/env), a regex (/pattern/), or a bare name (env)", s)
	}
}

// Match reports whether pathname matches this rule.
func (r *LocalCommandRule) Match(pathname string) bool {
	switch {
	case r.regex != nil:
		return r.regex.MatchString(pathname)
	case r.abs != "":
		return pathname == r.abs
	default:
		return filepath.Base(pathname) == r.base
	}
}

// MatchLocalCommand is a convenience that compiles entry and matches in one
// step. Returns false for malformed entries.
func MatchLocalCommand(entry, pathname string) bool {
	r, err := CompileLocalCommand(entry)
	if err != nil {
		return false
	}
	return r.Match(pathname)
}
