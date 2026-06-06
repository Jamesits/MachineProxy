//go:build backend_docker

package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/docker/cli/cli/connhelper"
	"github.com/moby/moby/client"
)

// dummySSHDaemonHost is the placeholder HTTP host used for ssh:// daemons: the
// real connection is made by the dial func, but the moby client still needs a
// syntactically valid host for request URLs. http scheme keeps TLS out of the
// path. Matches the value docker/cli's connhelper uses.
const dummySSHDaemonHost = "http://docker.example.com"

// appendSSHDaemonDial wires a docker client to reach an ssh:// daemon. It
// resets the host to a dummy HTTP host and installs a custom dial func; both
// must come after the base options because client.WithHost runs
// sockets.ConfigureTransport, which would otherwise overwrite our DialContext.
func appendSSHDaemonDial(opts []client.Opt, host, bind, bindInterface string, log *slog.Logger) ([]client.Opt, error) {
	if log == nil {
		log = slog.Default()
	}
	dialFn, dummyHost, err := sshDaemonDialer(host, bind, bindInterface, log)
	if err != nil {
		return nil, err
	}
	return append(opts,
		client.WithHost(dummyHost),
		client.WithDialContext(dialFn),
	), nil
}

// sshDaemonDialer selects how the ssh:// daemon is reached. With no local
// binding configured it uses docker/cli's connection helper (the standard
// `docker -H ssh://…` mechanism, which execs the system ssh binary). When
// remote.bind / remote.bind_interface is set it switches to machineproxy's
// built-in ssh transport, which is the only path that can honour the binding.
func sshDaemonDialer(host, bind, bindInterface string, log *slog.Logger) (func(ctx context.Context, network, addr string) (net.Conn, error), string, error) {
	if bind != "" || bindInterface != "" {
		log.Info("using machineproxy's built-in ssh transport for the docker daemon because remote.bind/remote.bind_interface is set",
			"host", host, "bind", bind, "bind_interface", bindInterface)
		return builtinSSHDial(host, bind, bindInterface, log)
	}
	return connhelperDial(host)
}

// connhelperDial builds the system-ssh dial func via docker/cli's connhelper.
func connhelperDial(host string) (func(ctx context.Context, network, addr string) (net.Conn, error), string, error) {
	helper, err := connhelper.GetConnectionHelper(host)
	if err != nil {
		return nil, "", fmt.Errorf("ssh docker connection helper for %q: %w", host, err)
	}
	if helper == nil {
		return nil, "", fmt.Errorf("no docker connection helper for %q", host)
	}
	return helper.Dialer, helper.Host, nil
}
