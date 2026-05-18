//go:build backend_docker

// Package docker implements a remote.Backend that attaches to a
// running container via the Docker SDK. Connection settings honor the
// standard docker client env vars (DOCKER_HOST, DOCKER_API_VERSION,
// DOCKER_TLS_VERIFY, DOCKER_CERT_PATH) via client.FromEnv.
package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// Config carries everything the Docker backend needs at construction.
// Container must be set; Host overrides DOCKER_HOST when non-empty.
type Config struct {
	Container string
	Host      string
	Log       *slog.Logger
}

// Backend is a remote.Backend backed by the Docker SDK.
type Backend struct {
	cfg         Config
	cli         *client.Client
	containerID string
	log         *slog.Logger

	mu         sync.Mutex
	connected  bool
	lastErr    error
	fileClient *agentFileClient
	agentPath  string // remote path where the agent was uploaded
}

// New constructs a Docker backend. The docker daemon is not contacted
// until Start.
func New(cfg Config) (*Backend, error) {
	if cfg.Container == "" {
		return nil, errors.New("docker backend: container is required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	opts, err := resolveClientOpts(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("docker context resolution: %w", err)
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Backend{cfg: cfg, cli: cli, log: log}, nil
}

// Type implements remote.Backend.
func (b *Backend) Type() remote.Type { return remote.TypeDocker }

// Addr implements remote.Backend. Returns the container name/ID the
// backend is attached to.
func (b *Backend) Addr() string { return b.cfg.Container }

// User implements remote.Backend. Docker exec runs as the container's
// configured user; we don't represent it separately.
func (b *Backend) User() string { return "" }

// KeepAliveInterval implements remote.Backend.
func (b *Backend) KeepAliveInterval() time.Duration { return 0 }

// SendKeepAlive implements remote.Backend. Docker's HTTP client doesn't
// need application-level keepalives over a local UDS; this is a no-op.
func (b *Backend) SendKeepAlive(ctx context.Context) error { return nil }

// Start verifies that the docker daemon is reachable and the target
// container is running.
func (b *Backend) Start(ctx context.Context) error {
	if _, err := b.cli.Ping(ctx); err != nil {
		b.mu.Lock()
		b.lastErr = err
		b.mu.Unlock()
		return fmt.Errorf("docker ping: %w", err)
	}
	insp, err := b.cli.ContainerInspect(ctx, b.cfg.Container)
	if err != nil {
		b.mu.Lock()
		b.lastErr = err
		b.mu.Unlock()
		return fmt.Errorf("docker inspect %q: %w", b.cfg.Container, err)
	}
	if insp.State == nil || !insp.State.Running {
		err := fmt.Errorf("container %q is not running (state=%v)", b.cfg.Container, stateString(insp.State))
		b.mu.Lock()
		b.lastErr = err
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	b.containerID = insp.ID
	b.connected = true
	b.lastErr = nil
	b.mu.Unlock()
	b.log.Debug("docker backend connected", "container", b.cfg.Container, "id", insp.ID[:12])
	return nil
}

// IsConnected implements remote.Backend.
func (b *Backend) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

// LastErr implements remote.Backend.
func (b *Backend) LastErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

// NewSession implements remote.Backend.
func (b *Backend) NewSession(ctx context.Context) (remote.Session, error) {
	b.mu.Lock()
	id := b.containerID
	b.mu.Unlock()
	if id == "" {
		return nil, errors.New("docker backend: not started")
	}
	return newSession(b.cli, id), nil
}

// Files returns a long-lived FileClient backed by a docker exec'd
// mproxy-agent. The agent is launched lazily on first use.
func (b *Backend) Files(ctx context.Context) (remote.FileClient, error) {
	b.mu.Lock()
	fc := b.fileClient
	agentPath := b.agentPath
	id := b.containerID
	b.mu.Unlock()
	if id == "" {
		return nil, errors.New("docker backend: not started")
	}
	if fc != nil {
		return fc, nil
	}
	if agentPath == "" {
		return nil, errors.New("docker backend: agent not uploaded yet (call UploadAgent first)")
	}
	fcNew, err := newAgentFileClient(ctx, b, agentPath)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.fileClient == nil {
		b.fileClient = fcNew
	} else {
		// Lost a race; close ours and use the existing one.
		_ = fcNew.Close()
		fcNew = b.fileClient
	}
	b.mu.Unlock()
	return fcNew, nil
}

// UploadAgent uploads the mproxy-agent binary to the container at the
// given path via the docker archive API.
func (b *Backend) UploadAgent(ctx context.Context, localPath, remotePath string, mode os.FileMode) (string, error) {
	b.mu.Lock()
	id := b.containerID
	b.mu.Unlock()
	if id == "" {
		return "", errors.New("docker backend: not started")
	}
	resolved, err := resolveContainerPath(ctx, b.cli, id, remotePath)
	if err != nil {
		return "", err
	}
	if err := copyFileToContainer(ctx, b.cli, id, localPath, resolved, mode); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.agentPath = resolved
	b.mu.Unlock()
	b.log.Debug("agent uploaded to container", "container", b.cfg.Container, "path", resolved)
	return resolved, nil
}

// Close shuts down the long-lived file-op agent (if any) and the
// docker client.
func (b *Backend) Close() error {
	b.mu.Lock()
	fc := b.fileClient
	b.fileClient = nil
	b.connected = false
	b.mu.Unlock()
	if fc != nil {
		_ = fc.Close()
	}
	return b.cli.Close()
}

// resolveContainerPath expands a leading "~/" against the container's
// $HOME by execing `printf %s "$HOME"`. Absolute paths pass through.
func resolveContainerPath(ctx context.Context, cli *client.Client, containerID, p string) (string, error) {
	if p == "" {
		return "", errors.New("empty container path")
	}
	if !strings.HasPrefix(p, "~/") && p != "~" {
		return p, nil
	}
	home, err := execCapture(ctx, cli, containerID, []string{"sh", "-c", "printf %s \"$HOME\""})
	if err != nil {
		return "", fmt.Errorf("resolve container $HOME: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		home = "/root"
	}
	if p == "~" {
		return home, nil
	}
	return home + p[1:], nil
}

// execCapture runs a one-shot command in the container and returns its
// stdout. Used for short housekeeping operations like resolving $HOME.
func execCapture(ctx context.Context, cli *client.Client, containerID string, cmd []string) (string, error) {
	sess := newSession(cli, containerID)
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return "", err
	}
	// Build a shell command string compatible with newSession's
	// `sh -c <string>` envelope.
	cmdStr := strings.Join(cmd, " ")
	if len(cmd) > 1 && cmd[0] == "sh" && cmd[1] == "-c" {
		cmdStr = cmd[2]
	}
	if err := sess.Start(cmdStr); err != nil {
		return "", err
	}
	doneOut := make(chan []byte, 1)
	doneErr := make(chan struct{})
	go func() {
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, err := stdout.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				doneOut <- buf
				return
			}
		}
	}()
	go func() {
		tmp := make([]byte, 256)
		for {
			if _, err := stderr.Read(tmp); err != nil {
				close(doneErr)
				return
			}
		}
	}()
	waitErr := sess.Wait()
	_ = sess.Close()
	out := <-doneOut
	<-doneErr
	if waitErr != nil {
		return string(out), waitErr
	}
	return string(out), nil
}

// shellQuote single-quotes s so it can be safely embedded in a shell
// command argument. Mirrors the helper in pkg/remoteexec/ssh_runner.go.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func stateString(s any) string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%+v", s)
}

// Compile-time check.
var _ remote.Backend = (*Backend)(nil)
