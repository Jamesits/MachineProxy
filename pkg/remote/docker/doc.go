//go:build backend_docker

// Package docker implements remote.Backend using the Docker SDK. It
// receives Docker or Compose target configuration and daemon state from
// cmd/machineproxy, and feeds the remote interface with container exec
// sessions, platform detection, agent upload, and file operations routed
// through a long-lived mproxy-agent.
package docker
