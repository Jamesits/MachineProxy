package sshconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/jamesits/sshconf/pkg/client"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DialConfig specifies the minimum identification parameters for a
// machineproxy SSH connection. Everything else (identity file, known hosts,
// algorithm lists, keepalives, etc.) is resolved from the user's ssh_config.
type DialConfig struct {
	// Host is passed to ssh_config resolution as the target. It may be a
	// literal hostname or IP, or a Host alias defined in ~/.ssh/config.
	Host string
	// User, if non-empty, overrides the user resolved from ssh_config.
	User string
	// Port, if non-zero, overrides the port resolved from ssh_config.
	Port int
	// Timeout is the TCP connect timeout used when ssh_config's
	// ConnectTimeout is unset. Zero means no timeout.
	Timeout time.Duration
}

// Dialer carries a dial closure and metadata derived from the resolved
// ssh_config options. NewDialer resolves the configuration once so every
// reconnect attempt reuses the same ClientConfig.
type Dialer struct {
	// Dial establishes a fresh SSH connection to the remote.
	Dial func(ctx context.Context) (Conn, error)
	// Addr is the resolved "host:port" address of the remote after
	// ssh_config Hostname/Port substitutions.
	Addr string
	// KeepAliveInterval is ServerAliveInterval from ssh_config, or zero
	// when the user did not configure keepalives.
	KeepAliveInterval time.Duration
}

// NewDialer resolves ssh_config for cfg.Host and returns a Dialer ready to
// be handed to a Manager. Identity keys, known_hosts, crypto preferences
// and timeouts all come from the library's ssh_config resolution.
func NewDialer(cfg DialConfig) (*Dialer, error) {
	if cfg.Host == "" {
		return nil, errors.New("ssh host is required")
	}

	lookup := &client.Lookup{
		Host: cfg.Host,
		User: cfg.User,
		Port: cfg.Port,
	}
	opts, err := lookup.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolve ssh_config: %w", err)
	}

	// Fall back to the caller-provided timeout only when ssh_config does
	// not set its own ConnectTimeout.
	if cfg.Timeout > 0 && (opts.ConnectTimeout == nil || *opts.ConnectTimeout == 0) {
		secs := int(cfg.Timeout / time.Second)
		if secs < 1 {
			secs = 1
		}
		opts.ConnectTimeout = &secs
	}

	sshConfig, err := opts.SSHClientConfig(client.Callbacks{}, client.Handlers{})
	if err != nil {
		return nil, fmt.Errorf("build ssh client config: %w", err)
	}

	host := ""
	if opts.Hostname != nil {
		host = *opts.Hostname
	}
	port := 22
	if opts.Port != nil {
		port = *opts.Port
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	var keepalive time.Duration
	if opts.ServerAliveInterval != nil && *opts.ServerAliveInterval > 0 {
		keepalive = time.Duration(*opts.ServerAliveInterval) * time.Second
	}

	return &Dialer{
		Dial: func(ctx context.Context) (Conn, error) {
			return dialSSH(ctx, addr, sshConfig)
		},
		Addr:              addr,
		KeepAliveInterval: keepalive,
	}, nil
}

func dialSSH(ctx context.Context, addr string, cfg *ssh.ClientConfig) (Conn, error) {
	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial ssh: %w", err)
	}

	cc, chans, reqs, err := ssh.NewClientConn(rawConn, addr, cfg)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("create ssh client conn: %w", err)
	}

	return &sshConn{client: ssh.NewClient(cc, chans, reqs)}, nil
}

type sshConn struct {
	client *ssh.Client

	mu   sync.Mutex
	sftp *sftp.Client
}

func (c *sshConn) NewSFTP(context.Context) (SFTPClient, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sftp != nil {
		return c.sftp, nil
	}
	cli, err := sftp.NewClient(c.client)
	if err != nil {
		return nil, fmt.Errorf("create sftp client: %w", err)
	}
	c.sftp = cli
	return c.sftp, nil
}

func (c *sshConn) NewSession(context.Context) (Session, error) {
	s, err := c.client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("create ssh session: %w", err)
	}
	return &sshSession{Session: s}, nil
}

func (c *sshConn) SendKeepAlive(ctx context.Context) error {
	_ = ctx
	_, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil)
	if err != nil {
		return fmt.Errorf("ssh keepalive failed: %w", err)
	}
	return nil
}

func (c *sshConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var firstErr error
	if c.sftp != nil {
		if err := c.sftp.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.sftp = nil
	}
	if c.client != nil {
		if err := c.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.client = nil
	}
	return firstErr
}

type sshSession struct {
	*ssh.Session
}
