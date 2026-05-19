//go:build !linux && !darwin

package config

// LibDir fallback for platforms where MachineProxy is not a first-class
// host target (currently freebsd/windows, used only by the cross-built
// mproxy-agent binary). The agent does not consult LibDir, so the value
// is mostly cosmetic — having it defined keeps the package importable
// from pkg/pathstub and similar shared code without per-OS gating.
var LibDir = "/usr/local/lib/machineproxy"
