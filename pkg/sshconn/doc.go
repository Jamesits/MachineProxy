//go:build backend_ssh

// Package sshconn manages persistent SSH and SFTP connections resolved
// from ssh_config. It receives DialConfig from the SSH remote backend,
// and feeds that backend with reconnecting sessions, SFTP clients,
// keepalive state, and server-version metadata.
package sshconn
