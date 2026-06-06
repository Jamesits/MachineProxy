// Package broker runs the Unix-socket bridge between mproxy-shim and a
// remoteexec runner. Each connection begins with a CBOR-encoded exec
// request, followed by a stream of CBOR exec, stream, signal, and
// extra-fd frames from the shim. The broker feeds sanitized command
// requests and I/O to the remote execution layer while returning output
// and exit frames.
package broker
