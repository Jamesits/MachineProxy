//go:build backend_ssh

// Package ssh implements remote.Backend on top of sshconn and SFTP. It
// receives SSH destination and ssh_config-derived connection settings
// from cmd/machineproxy, and feeds the remote interface with reusable
// sessions, SFTP-backed file operations, agent upload, and platform hints.
package ssh
