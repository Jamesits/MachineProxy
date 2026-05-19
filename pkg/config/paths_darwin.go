//go:build darwin

package config

// LibDir is the install location for machineproxy support binaries on
// darwin. Defaults to a Homebrew-friendly path; override at link time
// via -ldflags "-X github.com/jamesits/machineproxy/pkg/config.LibDir=..."
// for alternative install layouts (e.g. /opt/machineproxy/lib).
var LibDir = "/usr/local/lib/machineproxy"
