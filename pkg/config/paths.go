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
	// DefaultAgentRemotePath is the upload destination for mproxy-agent
	// on the remote host. A leading "~" is expanded against the remote
	// user's home directory at SFTP-use time.
	DefaultAgentRemotePath = "~/.cache/machineproxy/mproxy-agent"

	// DefaultPathStubDir is the local bind target for the PATH-stub
	// FUSE mount. A leading "~" is expanded against the local user's
	// home directory when the config is finalized; if home-directory
	// lookup fails, DefaultPathStubFallbackDir is used instead.
	DefaultPathStubDir = "~/.cache/machineproxy/pathstub"

	// DefaultPathStubFallbackDir is the absolute path used in place of
	// DefaultPathStubDir when the local home directory is unavailable.
	DefaultPathStubFallbackDir = "/tmp/machineproxy/pathstub"
)

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
