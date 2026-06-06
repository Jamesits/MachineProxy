// Package dialer builds net.Dialers that bind the local side of an
// outbound connection to a specific source address or network
// interface/VRF. It is shared by the remote backends (SSH dials the
// target host; Docker dials a TCP docker daemon) so both honour the same
// remote.bind / remote.bind_interface semantics.
package dialer
