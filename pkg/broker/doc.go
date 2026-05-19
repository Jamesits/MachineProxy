// Package broker runs the Unix-socket bridge between mproxy-shim and a
// remoteexec runner. It receives CBOR exec, stream, signal, and extra-fd
// frames from the shim, and feeds sanitized command requests and I/O to
// the remote execution layer while returning output and exit frames.
package broker
