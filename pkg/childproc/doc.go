// Package childproc builds and manages a single child process with
// portable stdio, credential, and extra-file-descriptor plumbing. It
// receives exec specs from cmd/mproxy-agent and feeds the agent's mux
// loop with process handles, pipes, and exit codes. Signal delivery is
// the caller's responsibility.
package childproc
