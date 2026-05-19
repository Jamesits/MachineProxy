//go:build linux

package config

// LibDir is the install location for machineproxy support binaries
// (mproxy-shim, mproxy-tracer, and the per-OS/arch agent layout under
// <LibDir>/agent/<goos>/<goarch>/). Override at link time via -ldflags
// "-X github.com/jamesits/machineproxy/pkg/config.LibDir=..." for
// downstream packagers.
var LibDir = "/usr/lib/machineproxy"
