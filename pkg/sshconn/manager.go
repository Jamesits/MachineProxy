package sshconn

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// SFTPClient is a minimal close-able interface used by the connection manager.
// Concrete code can wrap *sftp.Client.
type SFTPClient interface {
	Close() error
}

type Session interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Setenv(name, value string) error
	Start(cmd string) error
	Wait() error
	Close() error
}

// Conn describes the operations required from an SSH connection.
type Conn interface {
	NewSFTP(ctx context.Context) (SFTPClient, error)
	NewSession(ctx context.Context) (Session, error)
	SendKeepAlive(ctx context.Context) error
	Close() error
}

type Options struct {
	Dial              func(ctx context.Context) (Conn, error)
	ReconnectInterval time.Duration
	KeepAliveInterval time.Duration
}

type Manager struct {
	mu      sync.RWMutex
	conn    Conn
	sftp    SFTPClient
	opts    Options
	started bool
	lastErr error
}

func NewManager(opts Options) *Manager {
	if opts.ReconnectInterval <= 0 {
		opts.ReconnectInterval = 2 * time.Second
	}
	if opts.KeepAliveInterval < 0 {
		opts.KeepAliveInterval = 0
	}
	return &Manager{opts: opts}
}

func (m *Manager) Start(ctx context.Context) error {
	if m.opts.Dial == nil {
		return errors.New("ssh dial function is required")
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.mu.Unlock()

	// Attempt the first connection synchronously so callers get immediate
	// feedback when the remote host is unreachable.
	if err := m.reconnect(ctx); err != nil {
		m.mu.Lock()
		m.lastErr = err
		m.mu.Unlock()
	}

	go m.run(ctx)
	return nil
}

func (m *Manager) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conn != nil
}

// LastErr returns the most recent connection error, or nil if the last
// attempt succeeded. Useful for surfacing why the manager is not connected.
func (m *Manager) LastErr() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErr
}

func (m *Manager) SFTP() SFTPClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sftp
}

func (m *Manager) CurrentConn() Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conn
}

// NewSession creates an SSH session from the current connection.
// Returns an error if the connection is not established.
func (m *Manager) NewSession(ctx context.Context) (Session, error) {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return nil, errors.New("ssh connection is not ready")
	}
	return conn.NewSession(ctx)
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeLocked()
}

func (m *Manager) run(ctx context.Context) {
	var keepaliveTicker *time.Ticker
	if m.opts.KeepAliveInterval > 0 {
		keepaliveTicker = time.NewTicker(m.opts.KeepAliveInterval)
		defer keepaliveTicker.Stop()
	}

	reconnectTicker := time.NewTicker(m.opts.ReconnectInterval)
	defer reconnectTicker.Stop()

	for {
		if !m.IsConnected() {
			_ = m.reconnect(ctx)
		}

		select {
		case <-ctx.Done():
			_ = m.Close()
			return
		case <-reconnectTicker.C:
			if !m.IsConnected() {
				_ = m.reconnect(ctx)
			}
		case <-m.keepaliveChan(keepaliveTicker):
			if err := m.sendKeepAlive(ctx); err != nil {
				_ = m.Close()
			}
		}
	}
}

func (m *Manager) keepaliveChan(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func (m *Manager) reconnect(ctx context.Context) error {
	conn, err := m.opts.Dial(ctx)
	if err != nil {
		m.mu.Lock()
		m.lastErr = err
		m.mu.Unlock()
		return err
	}
	sftpClient, err := conn.NewSFTP(ctx)
	if err != nil {
		_ = conn.Close()
		m.mu.Lock()
		m.lastErr = err
		m.mu.Unlock()
		return err
	}

	m.mu.Lock()
	_ = m.closeLocked()
	m.conn = conn
	m.sftp = sftpClient
	m.lastErr = nil
	m.mu.Unlock()

	return nil
}

func (m *Manager) sendKeepAlive(ctx context.Context) error {
	m.mu.RLock()
	conn := m.conn
	m.mu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.SendKeepAlive(ctx)
}

func (m *Manager) closeLocked() error {
	var firstErr error
	if m.sftp != nil {
		if err := m.sftp.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.sftp = nil
	}
	if m.conn != nil {
		if err := m.conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.conn = nil
	}
	return firstErr
}
