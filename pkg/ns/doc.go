// Package ns prepares the local execution environment for the target
// process. It receives bind mounts, command lines, environment variables,
// and working directories from the runtime, and feeds either Bubblewrap
// on Linux or the direct Darwin launcher with the workspace and shim env.
package ns
