package config

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	LogLevel string `yaml:"log_level"` // trace, debug, info, warn, error

	Remote struct {
		SSH struct {
			Addr           string        `yaml:"addr"`
			User           string        `yaml:"user"`
			PrivateKeyPath string        `yaml:"private_key_path"`
			KnownHostsPath string        `yaml:"known_hosts_path"`
			KeepAlive      time.Duration `yaml:"keep_alive"`
		} `yaml:"ssh"`
		OS   string `yaml:"os"`   // remote OS for agent binary resolution; defaults to runtime.GOOS
		Arch string `yaml:"arch"` // remote arch for agent binary resolution; defaults to runtime.GOARCH
	} `yaml:"remote"`

	Container struct {
		LocalCommands []string `yaml:"local_commands"`
		Mounts        []string `yaml:"mounts"`     // docker-compose style: [local:]remote
		EnvRemove     []string `yaml:"env_remove"`  // glob/regex patterns for env vars to strip from the container process
	} `yaml:"container"`

	Agent struct {
		EnvKeep   []string `yaml:"env_keep"`   // glob/regex patterns for inherited env vars to forward to remote
		EnvRemove []string `yaml:"env_remove"` // glob/regex patterns for env vars to always strip from remote
	} `yaml:"agent"`

	Components struct {
		ShimPath       string `yaml:"shim_path"`
		TracerPath     string `yaml:"tracer_path"`
		AgentLocalPath string `yaml:"agent_local_path"`
		AgentRemotePath string `yaml:"agent_remote_path"`
	} `yaml:"components"`

	Recording struct {
		Path string `yaml:"path"` // empty disables recording
	} `yaml:"recording"`
}

func Load(r io.Reader) (*Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() error {
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Remote.SSH.KeepAlive == 0 {
		c.Remote.SSH.KeepAlive = 20 * time.Second
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
	if c.Remote.SSH.Addr == "" {
		return errors.New("remote.ssh.addr is required")
	}
	if c.Remote.SSH.User == "" {
		return errors.New("remote.ssh.user is required")
	}
	if c.Remote.SSH.PrivateKeyPath == "" {
		return errors.New("remote.ssh.private_key_path is required")
	}
	if len(c.Container.Mounts) == 0 {
		return errors.New("container.mounts must not be empty")
	}
	for _, m := range c.Container.Mounts {
		if _, err := ParseMount(m); err != nil {
			return fmt.Errorf("container.mounts: %w", err)
		}
	}
	if len(c.Container.LocalCommands) == 0 {
		return errors.New("container.local_commands must not be empty")
	}
	for _, p := range c.Container.LocalCommands {
		if err := validateLocalCommand(p); err != nil {
			return fmt.Errorf("container.local_commands: %w", err)
		}
	}
	return nil
}

// validateLocalCommand checks that a local_commands entry uses a supported
// matching form:
//   - "/absolute/path"  — full path match (starts with /, no trailing /)
//   - "/regex/"         — regex pattern (starts and ends with /)
//   - "basename"        — last-segment match (no slashes at all)
//
// Entries with slashes only in the middle (e.g. "usr/bin/env") are rejected.
func validateLocalCommand(s string) error {
	if s == "" {
		return errors.New("entry must not be empty")
	}

	startsSlash := strings.HasPrefix(s, "/")
	endsSlash := strings.HasSuffix(s, "/") && len(s) > 1

	switch {
	// /regex/ — delimited regex pattern
	case startsSlash && endsSlash:
		pattern := s[1 : len(s)-1]
		if pattern == "" {
			return fmt.Errorf("regex pattern must not be empty: %q", s)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("invalid regex in %q: %w", s, err)
		}

	// /absolute/path — full path match
	case startsSlash && !endsSlash:
		if !filepath.IsAbs(s) {
			return fmt.Errorf("entry must be an absolute path: %q", s)
		}

	// basename — match against last segment of the executable path
	case !startsSlash && !strings.Contains(s, "/"):
		// bare name, valid

	// Reject: slashes only in the middle (e.g. "usr/bin/env")
	default:
		return fmt.Errorf("entry %q has slashes in the middle; use an absolute path (/usr/bin/env), a regex (/pattern/), or a bare name (env)", s)
	}

	return nil
}
