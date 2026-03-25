package agenttransfer

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pkg/sftp"
)

// SFTPProvider returns the current SFTP client.
type SFTPProvider func() *sftp.Client

// Transferer uploads the agent binary to the remote host and caches
// it by SHA-256 hash so repeat uploads are skipped.
type Transferer struct {
	sftp       SFTPProvider
	localPath  string // path to the local agent binary
	remotePath string // destination on remote host

	mu          sync.Mutex
	transferred bool
	localHash   string
}

// New creates a Transferer. localPath is the path to the pre-built
// agent binary on the local machine. remotePath is where it will be
// placed on the remote host.
func New(sftp SFTPProvider, localPath, remotePath string) *Transferer {
	return &Transferer{
		sftp:       sftp,
		localPath:  localPath,
		remotePath: remotePath,
	}
}

// Ensure uploads the agent binary if it is not already present (or
// has changed). Returns the remote path.
func (t *Transferer) Ensure() (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.transferred {
		return t.remotePath, nil
	}

	hash, err := t.computeLocalHash()
	if err != nil {
		return "", fmt.Errorf("hash local agent binary: %w", err)
	}
	t.localHash = hash

	client := t.sftp()
	if client == nil {
		return "", fmt.Errorf("sftp client is nil")
	}

	// Check if remote already has this version.
	hashPath := t.remotePath + ".sha256"
	if existing, err := readRemoteFile(client, hashPath); err == nil && string(existing) == hash {
		t.transferred = true
		return t.remotePath, nil
	}

	// Upload the binary.
	if err := uploadFile(client, t.localPath, t.remotePath, 0o755); err != nil {
		return "", fmt.Errorf("upload agent: %w", err)
	}

	// Write the hash marker.
	if err := writeRemoteFile(client, hashPath, []byte(hash), 0o644); err != nil {
		// Non-fatal: next run will just re-upload.
		_ = err
	}

	t.transferred = true
	return t.remotePath, nil
}

func (t *Transferer) computeLocalHash() (string, error) {
	f, err := os.Open(t.localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func readRemoteFile(client *sftp.Client, path string) ([]byte, error) {
	f, err := client.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func uploadFile(client *sftp.Client, localPath, remotePath string, mode os.FileMode) error {
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
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}

	return client.Chmod(remotePath, mode)
}

func writeRemoteFile(client *sftp.Client, path string, data []byte, mode os.FileMode) error {
	f, err := client.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return client.Chmod(path, mode)
}
