//go:build backend_docker && backend_ssh

package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jamesits/machineproxy/pkg/sshconn"
)

// builtinSSHDial reaches an ssh:// docker daemon through machineproxy's own
// SSH stack (pkg/sshconn), which honours remote.bind / remote.bind_interface.
// Each dial opens a fresh ssh connection running `docker system dial-stdio`
// and presents its stdio as a net.Conn. ssh_config is resolved once up front.
func builtinSSHDial(host, bind, bindInterface string, log *slog.Logger) (func(ctx context.Context, network, addr string) (net.Conn, error), string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, "", fmt.Errorf("parse ssh docker host %q: %w", host, err)
	}
	var user string
	if u.User != nil {
		user = u.User.Username()
	}
	var port int
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil {
			return nil, "", fmt.Errorf("ssh docker host %q: invalid port %q: %w", host, p, err)
		}
	}

	// Mirror docker/cli connhelper: `docker system dial-stdio`, scoped to a
	// non-default socket with --host when the URL carries a path.
	remoteCmd := "docker system dial-stdio"
	if strings.Trim(u.Path, "/") != "" {
		remoteCmd = "docker --host=unix://" + shellQuote(u.Path) + " system dial-stdio"
	}

	dialer, err := sshconn.NewDialer(sshconn.DialConfig{
		Host:          u.Hostname(),
		User:          user,
		Port:          port,
		Timeout:       10 * time.Second,
		Bind:          bind,
		BindInterface: bindInterface,
	})
	if err != nil {
		return nil, "", fmt.Errorf("ssh docker dialer: %w", err)
	}

	dialFn := func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := dialer.Dial(ctx)
		if err != nil {
			return nil, fmt.Errorf("ssh docker dial: %w", err)
		}
		sess, err := conn.NewSession(ctx)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ssh docker session: %w", err)
		}
		stdin, err := sess.StdinPipe()
		if err != nil {
			_ = sess.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("ssh docker stdin: %w", err)
		}
		stdout, err := sess.StdoutPipe()
		if err != nil {
			_ = sess.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("ssh docker stdout: %w", err)
		}
		if err := sess.Start(remoteCmd); err != nil {
			_ = sess.Close()
			_ = conn.Close()
			return nil, fmt.Errorf("ssh docker dial-stdio: %w", err)
		}
		return &sshStdioConn{conn: conn, sess: sess, stdin: stdin, stdout: stdout}, nil
	}
	return dialFn, dummySSHDaemonHost, nil
}

// sshStdioConn adapts an ssh exec session running `docker system dial-stdio`
// into a net.Conn: stdin is the write side, stdout the read side. The ssh exec
// channel has no deadline support, so the deadline setters are no-ops; the
// docker http transport cancels in-flight requests by calling Close, which
// tears the session and connection down and unblocks any pending Read.
type sshStdioConn struct {
	conn   sshconn.Conn
	sess   sshconn.Session
	stdin  io.WriteCloser
	stdout io.Reader
}

func (c *sshStdioConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *sshStdioConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *sshStdioConn) Close() error {
	_ = c.stdin.Close()
	serr := c.sess.Close()
	cerr := c.conn.Close()
	if serr != nil {
		return serr
	}
	return cerr
}

func (c *sshStdioConn) LocalAddr() net.Addr  { return sshStdioAddr{} }
func (c *sshStdioConn) RemoteAddr() net.Addr { return sshStdioAddr{} }

func (c *sshStdioConn) SetDeadline(time.Time) error      { return nil }
func (c *sshStdioConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshStdioConn) SetWriteDeadline(time.Time) error { return nil }

type sshStdioAddr struct{}

func (sshStdioAddr) Network() string { return "ssh" }
func (sshStdioAddr) String() string  { return "docker-dial-stdio" }
