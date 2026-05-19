// Package remoteexec executes broker requests on a remote backend. It
// receives command, environment, stream, signal, and extra-fd requests
// from broker, and feeds remote sessions or mproxy-agent over agentproto
// while returning stdout, stderr, and exit status to the broker.
package remoteexec
