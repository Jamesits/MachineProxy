// Package agenttransfer caches uploads of the mproxy-agent binary. It
// receives a remote backend plus local and remote agent paths from the
// runtime wiring, and exposes Ensure() which returns the resolved remote
// agent path for use by remoteexec and other callers.
package agenttransfer
