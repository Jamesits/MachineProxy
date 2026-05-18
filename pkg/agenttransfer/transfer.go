// Package agenttransfer is a thin caching layer over
// remote.Backend.UploadAgent. The backend does the actual upload; this
// package just memoises the resolved remote path so multiple callers
// (path-stub enumeration, broker startup) don't repeat the work.
package agenttransfer

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// Transferer caches the resolved remote agent path so repeated callers
// of Ensure share one upload.
type Transferer struct {
	backend    remote.Backend
	localPath  string
	remotePath string
	log        *slog.Logger

	mu       sync.Mutex
	resolved string
}

// New constructs a Transferer.
func New(backend remote.Backend, localPath, remotePath string, log *slog.Logger) *Transferer {
	if log == nil {
		log = slog.Default()
	}
	return &Transferer{
		backend:    backend,
		localPath:  localPath,
		remotePath: remotePath,
		log:        log,
	}
}

// Ensure uploads the agent binary if it hasn't been already and
// returns the resolved remote path.
func (t *Transferer) Ensure(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.resolved != "" {
		return t.resolved, nil
	}
	if t.backend == nil {
		return "", errors.New("agenttransfer: backend is nil")
	}
	r, err := t.backend.UploadAgent(ctx, t.localPath, t.remotePath, 0o755)
	if err != nil {
		return "", err
	}
	t.resolved = r
	t.log.Debug("agent binary ready", "remote", r)
	return r, nil
}
