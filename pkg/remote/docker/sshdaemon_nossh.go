//go:build backend_docker && !backend_ssh

package docker

import (
	"context"
	"errors"
	"log/slog"
	"net"
)

// builtinSSHDial is unavailable without the ssh backend. ssh:// docker daemons
// still work via the system-ssh connection helper (see connhelperDial); only
// the bind-aware built-in transport needs the ssh stack compiled in.
func builtinSSHDial(_, _, _ string, _ *slog.Logger) (func(ctx context.Context, network, addr string) (net.Conn, error), string, error) {
	return nil, "", errors.New("remote.bind/remote.bind_interface with an ssh:// docker daemon requires the ssh backend; rebuild with -tags backend_ssh")
}
