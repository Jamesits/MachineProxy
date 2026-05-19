// Package supervisor orders the high-level MachineProxy startup sequence.
// It receives small runtime dependency interfaces from cmd/machineproxy,
// and feeds them in dependency order: backend, namespace, workspace FUSE,
// optional path stubs, broker, and child launcher.
package supervisor
