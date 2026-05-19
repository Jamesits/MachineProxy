package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Recognized values for Container.UIDMode and Container.GIDMode.
const (
	// UIDGIDModeTransparent forwards the remote UID/GID through unchanged.
	UIDGIDModeTransparent = "transparent"
	// UIDGIDModeOverride rewrites reads to the local user's UID/GID and
	// silently drops chown attempts that would otherwise propagate the
	// local user's view of ownership onto the remote.
	UIDGIDModeOverride = "override"
)

func validateUIDGIDMode(name, value string) error {
	switch value {
	case UIDGIDModeTransparent, UIDGIDModeOverride:
		return nil
	default:
		return fmt.Errorf("%s must be one of %s, %s; got %q", name, UIDGIDModeTransparent, UIDGIDModeOverride, value)
	}
}

// IDMapEntry is a parsed uid_map/gid_map line. A single entry maps a
// contiguous range of [Count] remote IDs starting at RemoteID to local
// IDs starting at LocalID.
type IDMapEntry struct {
	RemoteID uint32
	LocalID  uint32
	Count    uint32
}

// IDLookup resolves a name to a numeric ID. LookupUID and LookupGroupGID
// are the production implementations used by Validate; tests can pass an
// in-memory stub.
type IDLookup func(name string) (uint32, error)

// LookupUID resolves a local username to its UID.
func LookupUID(name string) (uint32, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse uid %q: %w", u.Uid, err)
	}
	return uint32(id), nil
}

// LookupGroupGID resolves a local group name to its GID.
func LookupGroupGID(name string) (uint32, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseUint(g.Gid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse gid %q: %w", g.Gid, err)
	}
	return uint32(id), nil
}

// CompileIDMapEntry parses a single uid_map / gid_map entry in
// /etc/subuid-like format. Supported forms:
//
//	uid:mapped_uid
//	username:mapped_uid
//	uid:mapped_uid:count
//	username:mapped_uid:count
//
// The first column identifies the remote-side ID (either as a numeric
// ID or as a name resolved via lookup); the second column is the local
// ID to surface for that range; count (default 1) extends the range.
// lookup may be nil when every entry uses a numeric first column.
func CompileIDMapEntry(s string, lookup IDLookup) (IDMapEntry, error) {
	if s == "" {
		return IDMapEntry{}, errors.New("entry must not be empty")
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return IDMapEntry{}, fmt.Errorf("entry %q: expected 2 or 3 colon-separated fields, got %d", s, len(parts))
	}
	var entry IDMapEntry
	if id, err := strconv.ParseUint(parts[0], 10, 32); err == nil {
		entry.RemoteID = uint32(id)
	} else {
		if lookup == nil {
			return IDMapEntry{}, fmt.Errorf("entry %q: first field %q is not numeric and no name lookup is configured", s, parts[0])
		}
		id, lerr := lookup(parts[0])
		if lerr != nil {
			return IDMapEntry{}, fmt.Errorf("entry %q: resolve %q: %w", s, parts[0], lerr)
		}
		entry.RemoteID = id
	}
	mapped, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return IDMapEntry{}, fmt.Errorf("entry %q: mapped id %q is not a valid uint32: %w", s, parts[1], err)
	}
	entry.LocalID = uint32(mapped)
	entry.Count = 1
	if len(parts) == 3 {
		count, err := strconv.ParseUint(parts[2], 10, 32)
		if err != nil {
			return IDMapEntry{}, fmt.Errorf("entry %q: count %q is not a valid uint32: %w", s, parts[2], err)
		}
		if count == 0 {
			return IDMapEntry{}, fmt.Errorf("entry %q: count must be > 0", s)
		}
		entry.Count = uint32(count)
	}
	return entry, nil
}

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
//
// Either side may start with "~" or "~/"; the local side is expanded
// against the local user's home immediately so the resulting ContainerPath
// is always absolute. The remote side is returned in its raw form (still
// possibly "~/..."), to be expanded against the remote user's home at
// SFTP-use time — see ExpandRemoteHome.
func ParseMount(s string) (Mount, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Mount{}, errors.New("mount entry must not be empty")
	}
	var local, remote string
	if i := strings.Index(s, ":"); i >= 0 {
		local = s[:i]
		remote = s[i+1:]
		if local == "" || remote == "" {
			return Mount{}, fmt.Errorf("invalid mount %q: both local and remote paths are required around ':'", s)
		}
	} else {
		local = s
		remote = s
	}
	if !isAbsOrHomeRelative(local) {
		return Mount{}, fmt.Errorf("mount local path must be absolute or start with ~: %q", s)
	}
	if !isAbsOrHomeRelative(remote) {
		return Mount{}, fmt.Errorf("mount remote path must be absolute or start with ~: %q", s)
	}
	expandedLocal, err := ExpandLocalHome(local)
	if err != nil {
		return Mount{}, fmt.Errorf("expand local mount path %q: %w", local, err)
	}
	return Mount{RemotePath: remote, ContainerPath: expandedLocal}, nil
}

// isAbsOrHomeRelative reports whether p is a valid path-like string
// that ParseMount and Validate will accept: either an OS-absolute path
// or a "~"/"~/..." home-relative path.
func isAbsOrHomeRelative(p string) bool {
	return filepath.IsAbs(p) || p == "~" || strings.HasPrefix(p, "~/")
}

// Config describes machineproxy runtime behavior.
type Config struct {
	LogLevel string `yaml:"log_level" toml:"log_level" json:"log_level"` // trace, debug, info, warn, error
	LogFile  string `yaml:"log_file"  toml:"log_file"  json:"log_file"`  // empty = stderr

	Remote struct {
		// Type selects the backend implementation: "ssh" (default) or
		// "docker". The CLI --backend flag overrides this when set.
		Type string `yaml:"type" toml:"type" json:"type"`
		// SSH is resolved via the user's ssh_config (see ssh_config(5)).
		// Host accepts either a literal hostname/IP or a Host alias defined
		// in ~/.ssh/config; User and Port, when set, override the resolved
		// values from ssh_config.
		SSH struct {
			Host string `yaml:"host" toml:"host" json:"host"`
			User string `yaml:"user" toml:"user" json:"user"`
			Port int    `yaml:"port" toml:"port" json:"port"`
		} `yaml:"ssh" toml:"ssh" json:"ssh"`
		// Docker selects a running container by name or ID. Host (when
		// set) overrides the DOCKER_HOST env var for this invocation.
		Docker struct {
			Container string `yaml:"container" toml:"container" json:"container"`
			Host      string `yaml:"host" toml:"host" json:"host"`
		} `yaml:"docker" toml:"docker" json:"docker"`
		// Compose selects a running container by Docker Compose project and
		// service name. Project "." resolves from the working directory.
		// Sequence (1-based) picks a specific replica of a scaled service;
		// 0 means auto (errors if more than one container matches).
		// Host, when set, overrides the DOCKER_HOST env var.
		Compose struct {
			Project  string `yaml:"project"  toml:"project"  json:"project"`
			Service  string `yaml:"service"  toml:"service"  json:"service"`
			Sequence int    `yaml:"sequence" toml:"sequence" json:"sequence"`
			Host     string `yaml:"host"     toml:"host"     json:"host"`
		} `yaml:"compose" toml:"compose" json:"compose"`
		// OS selects the remote-side agent binary's GOOS. When empty,
		// the runtime first asks the backend to detect it and only
		// falls back to the local runtime.GOOS if detection fails.
		OS string `yaml:"os" toml:"os" json:"os"`
		// Arch selects the remote-side agent binary's GOARCH. Empty
		// triggers the same detection/fallback chain as OS.
		Arch string `yaml:"arch" toml:"arch" json:"arch"`
	} `yaml:"remote" toml:"remote" json:"remote"`

	Container struct {
		LocalCommands []string `yaml:"local_commands" toml:"local_commands" json:"local_commands"`
		Mounts        []string `yaml:"mounts" toml:"mounts" json:"mounts"`                // docker-compose style: [local:]remote
		WorkingDir    string   `yaml:"working_dir" toml:"working_dir" json:"working_dir"` // override container working directory; defaults to first mount's local path
		EnvRemove     []string `yaml:"env_remove" toml:"env_remove" json:"env_remove"`    // glob/regex patterns for env vars to strip from the container process
		// PathProxy controls the FUSE-backed PATH-stub directory that
		// surfaces remote-side executables inside the container.
		//   "prepend"  — stubs win over locally-installed binaries (default)
		//   "append"   — local binaries win; stubs only fill gaps
		//   "disabled" — skip enumeration entirely
		PathProxy string `yaml:"path_proxy" toml:"path_proxy" json:"path_proxy"`
		// PathStubDir is the in-container directory where the stub FUSE
		// is bind-mounted. Defaults to $HOME/.cache/machineproxy/pathstub
		// so bwrap (which lacks privilege to mkdir parents under /var)
		// can create the bind target. A leading "~" is expanded against
		// the local user's home; the resulting path must be absolute.
		PathStubDir string `yaml:"path_stub_dir" toml:"path_stub_dir" json:"path_stub_dir"`
		// ForceResolveInitialCommandLocally controls whether the
		// resolved entrypoint command, its shebang interpreter, and
		// any env-target are auto-appended to LocalCommands. The
		// initial exec is always resolved against the local PATH
		// (the path-stub directory is skipped) because the path-stub
		// serves remote ELF binaries that cannot be loaded by the
		// local kernel; whitelisting the resolved chain prevents the
		// tracer from later routing those same paths to the remote
		// when they are re-execed (for instance by the kernel's
		// binfmt_script interpreter). Defaults to true; set false to
		// opt out.
		ForceResolveInitialCommandLocally *bool `yaml:"force_resolve_initial_command_locally" toml:"force_resolve_initial_command_locally" json:"force_resolve_initial_command_locally"`
		// UIDMode controls how the workspace FUSE mount reports file
		// owner UIDs and how chown(uid, _) calls are forwarded:
		//   "transparent" — pass the remote UID through unchanged
		//   "override"    — always report the local user's UID for
		//                   reads, and silently no-op uid changes on
		//                   write. This is the default because the
		//                   remote often runs as a different user
		//                   (e.g. root) than the local process, and
		//                   transparent ownership reporting trips
		//                   default_permissions in the kernel.
		UIDMode string `yaml:"uid_mode" toml:"uid_mode" json:"uid_mode"`
		// GIDMode is the GID counterpart to UIDMode; same semantics.
		GIDMode string `yaml:"gid_mode" toml:"gid_mode" json:"gid_mode"`
		// UIDMap is a list of /etc/subuid-like entries that translate
		// remote-reported UIDs to the local view (and back, on chown).
		// Each entry has one of these shapes:
		//   "uid:mapped_uid"
		//   "username:mapped_uid"          (username resolved via getpwnam)
		//   "uid:mapped_uid:count"
		//   "username:mapped_uid:count"
		// A UID covered by any entry takes the mapped value regardless
		// of UIDMode; UIDs not covered fall back to UIDMode behaviour.
		// See CompileIDMapEntry for the full grammar.
		UIDMap []string `yaml:"uid_map" toml:"uid_map" json:"uid_map"`
		// GIDMap is the GID counterpart to UIDMap. Names in the first
		// field are resolved via getgrnam instead of getpwnam.
		GIDMap []string `yaml:"gid_map" toml:"gid_map" json:"gid_map"`
	} `yaml:"container" toml:"container" json:"container"`

	Agent struct {
		EnvKeep   []string `yaml:"env_keep" toml:"env_keep" json:"env_keep"`       // glob/regex patterns for inherited env vars to forward to remote
		EnvRemove []string `yaml:"env_remove" toml:"env_remove" json:"env_remove"` // glob/regex patterns for env vars to always strip from remote
	} `yaml:"agent" toml:"agent" json:"agent"`

	Components struct {
		ShimPath        string `yaml:"shim_path" toml:"shim_path" json:"shim_path"`
		TracerPath      string `yaml:"tracer_path" toml:"tracer_path" json:"tracer_path"`
		InterposerPath  string `yaml:"interposer_path" toml:"interposer_path" json:"interposer_path"`
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
	if c.Remote.Type == "" {
		c.Remote.Type = "ssh"
	}
	// Remote.OS and Remote.Arch deliberately stay empty when the user
	// did not configure them. The runtime layer queries the backend
	// for the actual platform once the connection is up, and only
	// falls back to the local GOOS/GOARCH if that detection fails.
	if c.Components.AgentRemotePath == "" {
		// "~" is expanded against the remote user's home directory at
		// upload time (the SFTP server's default working dir).
		c.Components.AgentRemotePath = DefaultAgentRemotePath
	}
	if len(c.Container.EnvRemove) == 0 {
		c.Container.EnvRemove = []string{
			"MPROXY_*",
		}
	}
	if c.Container.PathProxy == "" {
		// Default "append" so locally-installed binaries shadow remote
		// stubs of the same name. The initial command is always resolved
		// locally regardless (see cmd/machineproxy main), so the value
		// only affects PATH lookups performed by the traced process and
		// its descendants.
		c.Container.PathProxy = "append"
	}
	if c.Container.PathStubDir == "" {
		c.Container.PathStubDir = defaultPathStubDir()
	}
	if c.Container.ForceResolveInitialCommandLocally == nil {
		t := true
		c.Container.ForceResolveInitialCommandLocally = &t
	}
	if c.Container.UIDMode == "" {
		c.Container.UIDMode = UIDGIDModeOverride
	}
	if c.Container.GIDMode == "" {
		c.Container.GIDMode = UIDGIDModeOverride
	}
	// Expand all local-side path fields up front so downstream consumers
	// only ever see absolute paths. Remote-side fields (AgentRemotePath,
	// Container.Mounts remote half) keep their "~/..." form here and are
	// expanded against the remote user's home at SFTP-use time.
	for _, f := range []struct {
		name string
		ptr  *string
	}{
		{"log_file", &c.LogFile},
		{"container.path_stub_dir", &c.Container.PathStubDir},
		{"container.working_dir", &c.Container.WorkingDir},
		{"components.shim_path", &c.Components.ShimPath},
		{"components.tracer_path", &c.Components.TracerPath},
		{"components.interposer_path", &c.Components.InterposerPath},
		{"components.agent_local_path", &c.Components.AgentLocalPath},
		{"recording.path", &c.Recording.Path},
	} {
		if *f.ptr == "" {
			continue
		}
		expanded, err := ExpandLocalHome(*f.ptr)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		*f.ptr = expanded
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
	if c.Components.InterposerPath != "" {
		if !filepath.IsAbs(c.Components.InterposerPath) {
			return errors.New("components.interposer_path must be absolute")
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
	switch c.Remote.Type {
	case "ssh":
		if c.Remote.SSH.Host == "" {
			return errors.New("remote.ssh.host is required when remote.type=ssh")
		}
		if c.Remote.SSH.Port < 0 || c.Remote.SSH.Port > 65535 {
			return fmt.Errorf("remote.ssh.port must be between 0 and 65535; got %d", c.Remote.SSH.Port)
		}
	case "docker":
		if c.Remote.Docker.Container == "" {
			return errors.New("remote.docker.container is required when remote.type=docker")
		}
	case "compose":
		if c.Remote.Compose.Service == "" {
			return errors.New("remote.compose.service is required when remote.type=compose")
		}
		if c.Remote.Compose.Sequence < 0 {
			return fmt.Errorf("remote.compose.sequence must be >= 0; got %d", c.Remote.Compose.Sequence)
		}
	default:
		return fmt.Errorf("remote.type must be one of ssh, docker, compose; got %q", c.Remote.Type)
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
	for _, p := range c.Container.LocalCommands {
		if _, err := CompileLocalCommand(p); err != nil {
			return fmt.Errorf("container.local_commands: %w", err)
		}
	}
	switch c.Container.PathProxy {
	case "prepend", "append", "disabled":
	default:
		return fmt.Errorf("container.path_proxy must be one of prepend, append, disabled; got %q", c.Container.PathProxy)
	}
	if err := validateUIDGIDMode("container.uid_mode", c.Container.UIDMode); err != nil {
		return err
	}
	if err := validateUIDGIDMode("container.gid_mode", c.Container.GIDMode); err != nil {
		return err
	}
	for _, e := range c.Container.UIDMap {
		if _, err := CompileIDMapEntry(e, LookupUID); err != nil {
			return fmt.Errorf("container.uid_map: %w", err)
		}
	}
	for _, e := range c.Container.GIDMap {
		if _, err := CompileIDMapEntry(e, LookupGroupGID); err != nil {
			return fmt.Errorf("container.gid_map: %w", err)
		}
	}
	if !filepath.IsAbs(c.Container.PathStubDir) {
		return fmt.Errorf("container.path_stub_dir must be absolute; got %q", c.Container.PathStubDir)
	}
	return nil
}

// defaultPathStubDir returns the resolved default for the PATH-stub
// mount point. DefaultPathStubDir is expanded against the local user's
// home; if that lookup fails we emit a warning and fall back to
// DefaultPathStubFallbackDir so bwrap can still mkdir the bind target.
func defaultPathStubDir() string {
	expanded, err := ExpandLocalHome(DefaultPathStubDir)
	if err == nil {
		return expanded
	}
	slog.Default().Warn(
		"could not determine local home directory; using path-stub fallback",
		"error", err,
		"fallback", DefaultPathStubFallbackDir,
	)
	return DefaultPathStubFallbackDir
}

// ExpandLocalHome resolves a leading "~" or "~/" against the local
// user's home directory. Other paths are returned unchanged. Returns
// an error only if "~" is used but the home dir cannot be looked up.
func ExpandLocalHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand ~: %w", err)
	}
	if home == "" {
		return "", errors.New("expand ~: home directory is empty")
	}
	if p == "~" {
		return home, nil
	}
	return filepath.Join(home, p[2:]), nil
}

// ExpandRemoteHome resolves a leading "~" or "~/" against remoteHome
// (typically the SFTP server's default working directory). Other paths
// are returned unchanged. SFTP servers do not expand "~" themselves and
// session.Start single-quotes its argument, so this rewrite must happen
// client-side before any remote use.
func ExpandRemoteHome(p, remoteHome string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	if remoteHome == "" {
		return "", errors.New("expand ~: remote home directory is empty")
	}
	if p == "~" {
		return remoteHome, nil
	}
	// path.Join (POSIX) — remote paths are SFTP/POSIX-style regardless
	// of the local OS.
	return path.Join(remoteHome, p[2:]), nil
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
