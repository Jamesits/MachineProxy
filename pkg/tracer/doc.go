//go:build linux

// Package tracer implements Linux ptrace-based exec interception for the
// target process tree. It receives shim, broker, whitelist, environment,
// and child-command configuration from mproxy-tracer, and feeds
// non-whitelisted exec calls through mproxy-shim with environment-change
// metadata for downstream filtering.
package tracer
