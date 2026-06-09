package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Packager-overridable path defaults. These are var (not const) so a
// downstream packager can repoint them at build time via, e.g.:
//
//	go build -ldflags "-X github.com/jamesits/machineproxy/pkg/config.LibDir=/opt/machineproxy/lib"
//
// LibDir's default is OS-specific and lives in paths_<goos>.go.
var (
	// DefaultCacheDir overrides the local-side cache parent directory
	// used to derive the PATH-stub bind target (and any future cache
	// locations). A leading "~" is expanded against the local user's
	// home directory at use time. When empty (the default), local
	// callers resolve $XDG_CACHE_HOME/machineproxy, falling back to
	// ~/.cache/machineproxy when XDG_CACHE_HOME is unset or relative.
	DefaultCacheDir = ""

	// DefaultAgentRemotePath is the upload destination for mproxy-agent
	// on the remote host. A leading "~" is expanded against the remote
	// user's home directory at SFTP-use time. This stays independent
	// of DefaultCacheDir because remote-side env vars aren't visible at
	// config-load time.
	DefaultAgentRemotePath = "~/.cache/machineproxy/mproxy-agent"

	// DefaultPathStubFallbackDir is the absolute path used in place of
	// the resolved local cache dir when home-directory lookup fails.
	DefaultPathStubFallbackDir = "/tmp/machineproxy/pathstub"
)

// LocalCacheDir resolves the local-side cache parent directory:
//   - DefaultCacheDir (with "~" expanded) when non-empty
//   - $XDG_CACHE_HOME/machineproxy when XDG_CACHE_HOME is an absolute path
//   - ~/.cache/machineproxy otherwise
//
// Returns an error only when "~" expansion is required and the local home
// directory cannot be determined.
func LocalCacheDir() (string, error) {
	if DefaultCacheDir != "" {
		return ExpandLocalHome(DefaultCacheDir)
	}
	// Honour XDG_CACHE_HOME only when it is an absolute path, as the
	// freedesktop spec requires; relative values must be ignored.
	if xdg := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "machineproxy"), nil
	}
	return ExpandLocalHome("~/.cache/machineproxy")
}

// ResolveShimPath finds the mproxy-shim binary using the config value,
// then falling back to adjacent binary and well-known install paths.
func ResolveShimPath(configPath string) (string, error) {
	return resolveBinary("mproxy-shim", configPath, []string{
		filepath.Join(LibDir, "mproxy-shim"),
	})
}

// ResolveTracerPath finds the mproxy-tracer binary using the config value,
// then falling back to adjacent binary and well-known install paths.
func ResolveTracerPath(configPath string) (string, error) {
	return resolveBinary("mproxy-tracer", configPath, []string{
		filepath.Join(LibDir, "mproxy-tracer"),
	})
}

// ResolveAgentBinaryPath finds the mproxy-agent binary. It checks the
// MPROXY_AGENT_BIN env var first, then the config value, adjacent binary,
// and well-known install paths. The goos and goarch parameters select the
// correct platform-specific binary for the remote host.
func ResolveAgentBinaryPath(configPath, goos, goarch string) (string, error) {
	if p := os.Getenv("MPROXY_AGENT_BIN"); p != "" {
		return p, nil
	}
	name := "mproxy-agent"
	if goos == "windows" {
		name = "mproxy-agent.exe"
	}
	return resolveBinary(name, configPath, []string{
		filepath.Join(LibDir, "agent", goos, goarch, name),
	})
}

// ResolveInterposerPath finds the libmproxy_interposer.dylib bundle for
// the local darwin host. It looks adjacent to the running machineproxy
// binary first (development layout), then under <LibDir>/agent/darwin/
// <goarch>/ (install layout, mirroring ResolveAgentBinaryPath). On
// non-darwin hosts the dylib is not used; callers should branch on
// runtime.GOOS before invoking this.
func ResolveInterposerPath(configPath string) (string, error) {
	const name = "libmproxy_interposer.dylib"
	return resolveBinary(name, configPath, []string{
		filepath.Join(LibDir, "agent", "darwin", runtime.GOARCH, name),
	})
}

// resolveBinary locates a binary by trying, in order: the explicit config
// path, a sibling of the current executable, and the supplied fallback
// candidates.
func resolveBinary(name, configPath string, candidates []string) (string, error) {
	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil {
			return "", fmt.Errorf("configured path for %s not found: %w", name, err)
		}
		return configPath, nil
	}

	// Look alongside the main binary first.
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("cannot find %s binary; set its path in the config or place it alongside machineproxy", name)
}

// isPosixAncestor reports whether ancestor is an ancestor of (or equal
// to) p when both are interpreted as POSIX-style paths. Returns false
// when the two paths use incompatible forms (one absolute, one "~/...").
func isPosixAncestor(ancestor, p string) bool {
	if ancestor == "" || p == "" {
		return false
	}
	if homePrefix(ancestor) != homePrefix(p) {
		return false
	}
	ancestor = path.Clean(ancestor)
	p = path.Clean(p)
	if ancestor == p {
		return true
	}
	return strings.HasPrefix(p, ancestor+"/")
}

// isLocalAncestor is the host-filesystem counterpart of isPosixAncestor.
// Both paths must already be absolute in the local OS path style.
func isLocalAncestor(ancestor, p string) bool {
	if ancestor == "" || p == "" {
		return false
	}
	if !filepath.IsAbs(ancestor) || !filepath.IsAbs(p) {
		return false
	}
	ancestor = filepath.Clean(ancestor)
	p = filepath.Clean(p)
	if ancestor == p {
		return true
	}
	return strings.HasPrefix(p, ancestor+string(filepath.Separator))
}

// homePrefix returns "~" when p is home-relative ("~" or "~/...") and
// "" when p is otherwise. Used to gate ancestor comparisons to compatible
// path forms.
func homePrefix(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return "~"
	}
	return ""
}

// defaultPathStubDir returns the resolved default for the PATH-stub
// mount point: <local cache dir>/pathstub. If the local cache dir
// cannot be resolved (typically because the home directory lookup
// fails), we emit a warning and fall back to DefaultPathStubFallbackDir
// so bwrap can still mkdir the bind target.
func defaultPathStubDir() string {
	base, err := LocalCacheDir()
	if err == nil {
		return filepath.Join(base, "pathstub")
	}
	slog.Default().Warn(
		"could not resolve local cache directory; using path-stub fallback",
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
