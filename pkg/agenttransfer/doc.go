// Package agenttransfer caches uploads of the mproxy-agent binary. It
// receives a remote backend plus local and remote agent paths from the
// runtime wiring, and feeds remoteexec, path-stub setup, and file-client
// bootstrap with the resolved remote agent path.
package agenttransfer
