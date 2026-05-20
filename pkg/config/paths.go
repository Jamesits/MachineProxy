package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
