package agenttransfer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/pkg/sftp"
)

// SFTPProvider returns the current SFTP client.
type SFTPProvider func() *sftp.Client

type remoteFileClient interface {
	Open(path string) (io.ReadCloser, error)
	OpenFile(path string, flags int) (io.WriteCloser, error)
	Chmod(path string, mode os.FileMode) error
	MkdirAll(path string) error
	Getwd() (string, error)
}

type sftpRemoteClient struct {
	client *sftp.Client
}

func (c sftpRemoteClient) Open(path string) (io.ReadCloser, error) {
	return c.client.Open(path)
}

func (c sftpRemoteClient) OpenFile(path string, flags int) (io.WriteCloser, error) {
	return c.client.OpenFile(path, flags)
}

func (c sftpRemoteClient) Chmod(path string, mode os.FileMode) error {
	return c.client.Chmod(path, mode)
}

func (c sftpRemoteClient) MkdirAll(path string) error {
	return c.client.MkdirAll(path)
}

func (c sftpRemoteClient) Getwd() (string, error) {
	return c.client.Getwd()
}

// Transferer uploads the agent binary to the remote host and caches
// it by SHA-256 hash so repeat uploads are skipped.
type Transferer struct {
	sftp       SFTPProvider
	remote     remoteFileClient
	localPath  string // path to the local agent binary
	remotePath string // destination on remote host
	log        *slog.Logger

	mu          sync.Mutex
	transferred bool
	localHash   string
}

func newWithRemoteClient(remote remoteFileClient, localPath, remotePath string, log *slog.Logger) *Transferer {
	t := New(nil, localPath, remotePath, log)
	t.remote = remote
	return t
}

// New creates a Transferer. localPath is the path to the pre-built
// agent binary on the local machine. remotePath is where it will be
// placed on the remote host.
func New(sftp SFTPProvider, localPath, remotePath string, log *slog.Logger) *Transferer {
	if log == nil {
		log = slog.Default()
	}
	return &Transferer{
		sftp:       sftp,
		localPath:  localPath,
		remotePath: remotePath,
		log:        log,
	}
}

// Ensure uploads the agent binary if it is not already present (or
// has changed). Returns the remote path.
func (t *Transferer) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.transferred {
		t.log.Log(ctx, logging.LevelTrace, "agent binary already transferred")
		return t.remotePath, nil
	}

	t.log.Log(ctx, logging.LevelTrace, "computing local agent hash", "path", t.localPath)
	hash, err := hashFile(t.localPath)
	if err != nil {
		return "", fmt.Errorf("hash local agent binary: %w", err)
	}
	t.localHash = hash
	t.log.Log(ctx, logging.LevelTrace, "local agent hash", "path", t.localPath, "sha256", hash)

	client, err := t.remoteClient()
	if err != nil {
		return "", err
	}

	// Expand a leading "~/" against the SFTP server's working directory
	// (typically the remote user's home). SFTP itself does not expand ~,
	// and the resolved absolute path is also what gets passed to
	// session.Start, which single-quotes its argument.
	resolved, err := expandRemoteHome(client, t.remotePath)
	if err != nil {
		return "", fmt.Errorf("expand remote agent path %q: %w", t.remotePath, err)
	}
	t.remotePath = resolved

	// Check the remote binary itself. A sidecar hash marker in /tmp is not a
	// trust boundary because other remote users may be able to write it.
	// This still creates a TOCTOU possibility though.
	remoteHash, remoteErr := hashRemoteFile(client, t.remotePath)
	if remoteErr != nil {
		t.log.Log(ctx, logging.LevelTrace, "remote agent hash unavailable", "path", t.remotePath, "error", remoteErr)
	} else {
		t.log.Log(ctx, logging.LevelTrace, "remote agent hash", "path", t.remotePath, "sha256", remoteHash)
	}
	if remoteErr == nil && remoteHash == hash {
		t.log.Debug("agent binary hash matches, skipping upload", "hash", hash[:12])
		t.transferred = true
		return t.remotePath, nil
	}

	// Ensure the parent directory exists before the upload. Required for
	// the default ~/.cache/machineproxy/ location on fresh remote hosts.
	if parent := path.Dir(t.remotePath); parent != "" && parent != "." && parent != "/" {
		if mkErr := client.MkdirAll(parent); mkErr != nil {
			return "", fmt.Errorf("create remote dir %q: %w", parent, mkErr)
		}
	}

	// Upload the binary.
	t.log.Debug("uploading agent binary", "local", t.localPath, "remote", t.remotePath)
	if err := uploadFile(client, t.localPath, t.remotePath, 0o755); err != nil {
		return "", fmt.Errorf("upload agent: %w", err)
	}

	t.transferred = true
	t.log.Debug("agent binary transferred", "remote", t.remotePath, "hash", hash[:12])
	return t.remotePath, nil
}

// expandRemoteHome resolves a leading "~" or "~/" against the SFTP
// server's working directory. Other paths are returned unchanged
// without a Getwd round-trip.
func expandRemoteHome(client remoteFileClient, p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := client.Getwd()
	if err != nil {
		return "", err
	}
	return config.ExpandRemoteHome(p, home)
}

func (t *Transferer) remoteClient() (remoteFileClient, error) {
	if t.remote != nil {
		return t.remote, nil
	}
	if t.sftp == nil {
		return nil, fmt.Errorf("sftp client is nil")
	}
	client := t.sftp()
	if client == nil {
		return nil, fmt.Errorf("sftp client is nil")
	}
	return sftpRemoteClient{client: client}, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(f)
}

func hashRemoteFile(client remoteFileClient, path string) (string, error) {
	f, err := client.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(f)
}

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func uploadFile(client remoteFileClient, localPath, remotePath string, mode os.FileMode) error {
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := client.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}

	return client.Chmod(remotePath, mode)
}
