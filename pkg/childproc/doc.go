// Package childproc builds and manages a single child process with
// portable stdio, signal, credential, and extra-file-descriptor plumbing.
// It receives exec specs from cmd/mproxy-agent and feeds the agent's mux
// loop with process handles, pipes, exit codes, and signal delivery.
package childproc
