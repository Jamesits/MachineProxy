// Package workspacefs exposes a remote workspace as a local FUSE
// filesystem. It receives a remote.FileClient, mount root, logging, and
// UID/GID translation options from the runtime, and feeds go-fuse nodes
// used by the namespace and target process with POSIX-like file behavior.
package workspacefs
