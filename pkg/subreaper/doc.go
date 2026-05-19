// Package subreaper installs platform-specific orphan-process cleanup
// for MachineProxy's local process tree. It receives lifecycle context
// from the command runtime, and feeds the operating system with subreaper,
// procctl, or job-object setup plus SIGCHLD reaping where available.
package subreaper
