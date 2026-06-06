//go:build backend_docker

// Package docker implements remote.Backend using the Docker SDK. It
// receives an already-resolved container name and daemon state from
// cmd/machineproxy (Compose resolution happens upstream before this
// package is invoked), and feeds the remote interface with container exec
// sessions, platform detection, agent upload, and file operations routed
// through a long-lived mproxy-agent.
package docker
