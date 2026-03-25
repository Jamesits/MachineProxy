package config

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config describes machineproxy runtime behavior.
type Config struct {
	SSH struct {
		Addr           string        `yaml:"addr"`
		User           string        `yaml:"user"`
		PrivateKeyPath string        `yaml:"private_key_path"`
		KnownHostsPath string        `yaml:"known_hosts_path"`
		KeepAlive      time.Duration `yaml:"keep_alive"`
	} `yaml:"ssh"`

	Workspace struct {
		RemotePath string `yaml:"remote_path"`
	} `yaml:"workspace"`

	Exec struct {
		LocalCommands []string `yaml:"local_commands"`
		ShimPath      string   `yaml:"shim_path"`
		EnvKeep       []string `yaml:"env_keep"`   // glob patterns for inherited env vars to forward
		EnvRemove     []string `yaml:"env_remove"` // glob patterns for env vars to always strip
	} `yaml:"exec"`

	Broker struct {
		SocketPath string `yaml:"socket_path"`
	} `yaml:"broker"`

	Agent struct {
		LocalPath  string `yaml:"local_path"`
		RemotePath string `yaml:"remote_path"`
	} `yaml:"agent"`

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
	if c.SSH.KeepAlive == 0 {
		c.SSH.KeepAlive = 20 * time.Second
	}
	if c.Broker.SocketPath == "" {
		c.Broker.SocketPath = "/tmp/machineproxy.sock"
	}
	if c.Agent.RemotePath == "" {
		c.Agent.RemotePath = "/tmp/mproxy-agent"
	}
	if len(c.Exec.EnvKeep) == 0 {
		c.Exec.EnvKeep = []string{
			"HOME", "PATH", "TERM", "LANG", "LC_*",
			"USER", "LOGNAME", "SHELL",
			"EDITOR", "VISUAL", "PAGER",
			"TZ", "DISPLAY", "SSH_AUTH_SOCK", "XDG_*",
		}
	}
	if len(c.Exec.EnvRemove) == 0 {
		c.Exec.EnvRemove = []string{
			"LD_PRELOAD", "LD_LIBRARY_PATH",
			"MPROXY_*",
		}
	}
	if c.Exec.ShimPath != "" {
		if !filepath.IsAbs(c.Exec.ShimPath) {
			return errors.New("exec.shim_path must be absolute")
		}
	}
	return nil
}

func (c *Config) Validate() error {
	if c.SSH.Addr == "" {
		return errors.New("ssh.addr is required")
	}
	if c.SSH.User == "" {
		return errors.New("ssh.user is required")
	}
	if c.SSH.PrivateKeyPath == "" {
		return errors.New("ssh.private_key_path is required")
	}
	if c.Workspace.RemotePath == "" {
		return errors.New("workspace.remote_path is required")
	}
	if len(c.Exec.LocalCommands) == 0 {
		return errors.New("exec.local_commands must not be empty")
	}
	for _, p := range c.Exec.LocalCommands {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("exec.local_commands entry must be absolute path: %q", p)
		}
	}
	if c.Exec.ShimPath == "" {
		return errors.New("exec.shim_path is required")
	}
	if c.Broker.SocketPath == "" {
		return errors.New("broker.socket_path is required")
	}
	return nil
}
