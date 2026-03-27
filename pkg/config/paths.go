package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// LibDir is the standard install location for machineproxy support binaries.
const LibDir = "/usr/lib/machineproxy"

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
	return resolveBinary("mproxy-agent", configPath, []string{
		filepath.Join(LibDir, "agent", goos, goarch, "mproxy-agent"),
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
