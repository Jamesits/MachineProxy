package sshconn

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type DialConfig struct {
	Addr           string
	User           string
	PrivateKeyPath string
	KnownHostsPath string
	Timeout        time.Duration
}

func NewDialFunc(cfg DialConfig) (func(ctx context.Context) (Conn, error), error) {
	signer, err := loadSigner(cfg.PrivateKeyPath)
	if err != nil {
		return nil, err
	}

	algorithms := ssh.SupportedAlgorithms()
	// TODO: differenciate ssh.InsecureAlgorithms()
	sshConfig := &ssh.ClientConfig{
		User:              cfg.User,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.InsecureIgnoreHostKey(),
		Timeout:           cfg.Timeout,
		HostKeyAlgorithms: algorithms.HostKeys,
	}
	sshConfig.Config.Ciphers = algorithms.Ciphers
	sshConfig.Config.KeyExchanges = algorithms.KeyExchanges
	sshConfig.Config.MACs = algorithms.MACs

	return func(ctx context.Context) (Conn, error) {
		return dialSSH(ctx, cfg.Addr, sshConfig)
	}, nil
}

func loadSigner(privateKeyPath string) (ssh.Signer, error) {
	pk, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pk)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return signer, nil
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
