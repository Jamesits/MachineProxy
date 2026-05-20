// Package config loads, defaults, validates, and resolves MachineProxy
// configuration. It receives TOML, JSON or YAML files plus CLI overrides
// from cmd/machineproxy, and feeds runtime wiring for backends, namespace
// setup, FUSE mounts, environment filtering, and component discovery.
package config
