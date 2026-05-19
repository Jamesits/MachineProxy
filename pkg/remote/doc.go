// Package remote defines the backend-neutral command and file-operation
// interfaces for a target machine. It receives parsed destinations and
// backend implementations from runtime setup, and feeds remoteexec,
// workspacefs, pathstub, and agenttransfer with sessions, file clients,
// platform metadata, and agent upload hooks.
package remote
