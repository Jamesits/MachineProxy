//go:build backend_ssh

// Package ssh adapts pkg/sshconn into a remote.Backend so the rest of
// the codebase can consume the backend-agnostic interface.
package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/remote"
	"github.com/jamesits/machineproxy/pkg/sshconn"
)

// Config carries everything the SSH backend needs at construction time.
// Anything not set here is resolved from the user's ssh_config.
type Config struct {
	Host string
	User string
	Port int
	// ConnectTimeout falls back to 10s when zero. ssh_config's
	// ConnectTimeout still takes priority when set.
	ConnectTimeout time.Duration
	// Log is used by the underlying manager.
	Log *slog.Logger
}

// Backend is a remote.Backend backed by pkg/sshconn.
type Backend struct {
	cfg     Config
	addr    string
	manager *sshconn.Manager
	dialer  *sshconn.Dialer
	keep    time.Duration
}

// New constructs an SSH backend. The connection is not opened until
// Start is called.
func New(cfg Config) (*Backend, error) {
	if cfg.Host == "" {
		return nil, errors.New("ssh backend: host is required")
	}
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	dialer, err := sshconn.NewDialer(sshconn.DialConfig{
		Host:    cfg.Host,
		User:    cfg.User,
		Port:    cfg.Port,
		Timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	mgr := sshconn.NewManager(sshconn.Options{
		Dial:              dialer.Dial,
		ReconnectInterval: time.Second,
		KeepAliveInterval: dialer.KeepAliveInterval,
		Log:               log,
	})
	return &Backend{
		cfg:     cfg,
		addr:    dialer.Addr,
		manager: mgr,
		dialer:  dialer,
		keep:    dialer.KeepAliveInterval,
	}, nil
}

// Type implements remote.Backend.
func (b *Backend) Type() remote.Type { return remote.TypeSSH }

// Addr implements remote.Backend.
func (b *Backend) Addr() string { return b.addr }

// User implements remote.Backend.
func (b *Backend) User() string { return b.cfg.User }

// KeepAliveInterval implements remote.Backend.
func (b *Backend) KeepAliveInterval() time.Duration { return b.keep }

// SendKeepAlive implements remote.Backend. The sshconn.Manager already
// drives keepalives internally; this is a no-op so the generic runtime
// doesn't double-send.
func (b *Backend) SendKeepAlive(ctx context.Context) error { return nil }

// Start implements remote.Backend.
func (b *Backend) Start(ctx context.Context) error {
	if err := b.manager.Start(ctx); err != nil {
		return err
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if b.manager.IsConnected() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if lastErr := b.manager.LastErr(); lastErr != nil {
				return fmt.Errorf("timed out waiting for ssh connection: %w", lastErr)
			}
			return errors.New("timed out waiting for persistent ssh connection")
		case <-tick.C:
		}
	}
}

// IsConnected implements remote.Backend.
func (b *Backend) IsConnected() bool { return b.manager.IsConnected() }

// LastErr implements remote.Backend.
func (b *Backend) LastErr() error { return b.manager.LastErr() }

// NewSession implements remote.Backend.
func (b *Backend) NewSession(ctx context.Context) (remote.Session, error) {
	sess, err := b.manager.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	return sshSessionAdapter{Session: sess}, nil
}

// Files implements remote.Backend.
func (b *Backend) Files(ctx context.Context) (remote.FileClient, error) {
	raw := b.manager.SFTP()
	if raw == nil {
		return nil, errors.New("ssh backend: sftp client is not ready")
	}
	cli, ok := raw.(*sftp.Client)
	if !ok {
		return nil, fmt.Errorf("ssh backend: unexpected sftp client type %T", raw)
	}
	return &sftpFileClient{c: cli}, nil
}

// DetectPlatform implements remote.Backend. SSH does not have a
// daemon-side platform query, so detection is limited to whatever
// hints can be extracted from the server identification string the
// remote sent during the handshake. Architecture is essentially never
// advertised in that string; callers are expected to fall back to a
// local default when this returns an error or an empty arch.
func (b *Backend) DetectPlatform(ctx context.Context) (remote.PlatformInfo, error) {
	_ = ctx
	conn := b.manager.CurrentConn()
	if conn == nil {
		return remote.PlatformInfo{}, errors.New("ssh detect: connection not ready")
	}
	return parseServerVersion(conn.ServerVersion())
}

// UploadAgent implements remote.Backend. SSH self-hosts the agent over
// SFTP, so this delegates to the FileClient's primitives.
func (b *Backend) UploadAgent(ctx context.Context, localPath, remotePath string, mode os.FileMode) (string, error) {
	fc, err := b.Files(ctx)
	if err != nil {
		return "", err
	}
	return uploadViaFileClient(fc, localPath, remotePath, mode)
}

// Close implements remote.Backend.
func (b *Backend) Close() error { return b.manager.Close() }

// uploadViaFileClient is shared by any backend whose FileClient can
// self-host the agent (currently SSH). The Docker backend has its own
// CopyToContainer path.
func uploadViaFileClient(fc remote.FileClient, localPath, remotePath string, mode os.FileMode) (string, error) {
	resolved, err := expandRemoteHome(fc, remotePath)
	if err != nil {
		return "", fmt.Errorf("expand remote path %q: %w", remotePath, err)
	}
	// Best-effort parent mkdir. Required for fresh hosts where the
	// default ~/.cache/machineproxy/ path doesn't exist yet.
	if parent := parentDir(resolved); parent != "" && parent != "." && parent != "/" {
		if err := fc.MkdirAll(parent); err != nil {
			return "", fmt.Errorf("create remote dir %q: %w", parent, err)
		}
	}
	src, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := fc.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	if err := fc.Chmod(resolved, mode); err != nil {
		return "", err
	}
	return resolved, nil
}
