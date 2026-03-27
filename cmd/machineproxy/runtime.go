package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
	"github.com/jamesits/machineproxy/pkg/broker"
	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/envfilter"
	"github.com/jamesits/machineproxy/pkg/ns"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
	"github.com/jamesits/machineproxy/pkg/sshconn"
	"github.com/jamesits/machineproxy/pkg/workspacefs"
)

type runtimeDeps struct {
	cfg *config.Config

	sshManager   *sshconn.Manager
	namespace    *ns.Namespace
	fuseServer   *fuse.Server
	bkr          *broker.Server
	fuseMountDir string
	recorder     *agentproto.Recorder

	brokerCtxCancel context.CancelFunc
}

func newRuntimeDeps(cfg *config.Config) (*runtimeDeps, error) {
	dial, err := sshconn.NewDialFunc(sshconn.DialConfig{
		Addr:           cfg.SSH.Addr,
		User:           cfg.SSH.User,
		PrivateKeyPath: cfg.SSH.PrivateKeyPath,
		KnownHostsPath: cfg.SSH.KnownHostsPath,
		Timeout:        10 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	return &runtimeDeps{
		cfg: cfg,
		sshManager: sshconn.NewManager(sshconn.Options{
			Dial:              dial,
			ReconnectInterval: time.Second,
			KeepAliveInterval: cfg.SSH.KeepAlive,
		}),
		namespace: ns.New(ns.Deps{}),
	}, nil
}

func (d *runtimeDeps) Close() {
	if d.recorder != nil {
		_ = d.recorder.Close()
	}
	if d.brokerCtxCancel != nil {
		d.brokerCtxCancel()
	}
	if d.fuseServer != nil {
		_ = d.fuseServer.Unmount()
		d.fuseServer = nil
	}
	if d.fuseMountDir != "" {
		_ = os.RemoveAll(d.fuseMountDir)
		d.fuseMountDir = ""
	}
	d.namespace.Leave()
	if d.sshManager != nil {
		_ = d.sshManager.Close()
	}
}

func (d *runtimeDeps) StartSSH(ctx context.Context) error {
	if err := d.sshManager.Start(ctx); err != nil {
		return err
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		if d.sshManager.IsConnected() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			if lastErr := d.sshManager.LastErr(); lastErr != nil {
				return fmt.Errorf("timed out waiting for ssh connection: %w", lastErr)
			}
			return errors.New("timed out waiting for persistent ssh connection")
		case <-tick.C:
		}
	}
}

func (d *runtimeDeps) EnterNamespace(ctx context.Context) error {
	return d.namespace.Prepare(ctx)
}

func (d *runtimeDeps) MountWorkspace(ctx context.Context) error {
	tmpDir, err := os.MkdirTemp("", "machineproxy-fuse-*")
	if err != nil {
		return fmt.Errorf("create temp mountpoint: %w", err)
	}
	d.fuseMountDir = tmpDir

	raw := d.sshManager.SFTP()
	sftpClient, ok := raw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("ssh sftp client is not *sftp.Client")
	}

	backend := workspacefs.New(&workspacefs.SFTPAdapter{C: sftpClient}, d.cfg.Workspace.RemotePath)
	server, err := workspacefs.Mount(ctx, backend, d.fuseMountDir)
	if err != nil {
		return fmt.Errorf("mount workspace fuse: %w", err)
	}
	d.fuseServer = server
	return nil
}

func (d *runtimeDeps) StartBroker(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(d.cfg.Broker.SocketPath), 0o755); err != nil {
		return fmt.Errorf("create broker socket dir: %w", err)
	}

	agentLocalPath, err := resolveAgentBinaryPath(d.cfg.Agent.LocalPath)
	if err != nil {
		return fmt.Errorf("resolve agent binary: %w", err)
	}

	sftpRaw := d.sshManager.SFTP()
	sftpClient, ok := sftpRaw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("sftp client is not *sftp.Client for agent transfer")
	}

	transferer := agenttransfer.New(
		func() *sftp.Client { return sftpClient },
		agentLocalPath,
		d.cfg.Agent.RemotePath,
	)

	// Set up session recording if configured.
	if d.cfg.Recording.Path != "" && d.recorder == nil {
		rec, recErr := agentproto.NewRecorder(d.cfg.Recording.Path)
		if recErr != nil {
			return fmt.Errorf("create session recorder: %w", recErr)
		}
		d.recorder = rec

		username := "unknown"
		if u, uErr := currentUsername(); uErr == nil {
			username = u
		}
		_ = rec.WriteSessionHeader(&agentproto.SessionHeader{
			Version:   version,
			StartTime: time.Now().UnixNano(),
			LocalUser: username,
			LocalPID:  os.Getpid(),
			SSHAddr:   d.cfg.SSH.Addr,
			SSHUser:   d.cfg.SSH.User,
			AgentPath: d.cfg.Agent.RemotePath,
		})
	}

	envKeep := d.cfg.Exec.EnvKeep
	envRemove := d.cfg.Exec.EnvRemove
	d.bkr = broker.NewServer(broker.Deps{
		Remote: &remoteexec.AgentRunner{
			Provider:   d.sshManager,
			Transferer: transferer,
			Recorder:   d.recorder,
		},
		EnvFilter: func(env []string) []string {
			return envfilter.Filter(env, envKeep, envRemove)
		},
	})
	bctx, cancel := context.WithCancel(ctx)
	d.brokerCtxCancel = cancel

	go func() {
		_ = d.bkr.Start(bctx, d.cfg.Broker.SocketPath)
	}()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(d.cfg.Broker.SocketPath); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("broker socket did not become ready")
		case <-ticker.C:
		}
	}
}

func (d *runtimeDeps) RunChild(ctx context.Context, cmdline []string) error {
	if len(cmdline) == 0 {
		return errors.New("missing child command")
	}

	tracerBin, err := resolveTracerPath(d.cfg.Exec.TracerPath)
	if err != nil {
		return err
	}

	env := ns.FormatEnv(
		os.Environ(),
		d.cfg.Broker.SocketPath,
		d.cfg.Exec.ShimPath,
	)

	// Wrap the command in the ptrace-based tracer so exec interception
	// works with both dynamically and statically linked binaries.
	tracerArgs := []string{
		tracerBin,
		"--shim-path", d.cfg.Exec.ShimPath,
		"--broker-sock", d.cfg.Broker.SocketPath,
	}
	if len(d.cfg.Exec.LocalCommands) > 0 {
		tracerArgs = append(tracerArgs, "--whitelist", strings.Join(d.cfg.Exec.LocalCommands, ":"))
	}
	tracerArgs = append(tracerArgs, "--")
	tracerArgs = append(tracerArgs, cmdline...)

	return d.namespace.Run(ctx, d.fuseMountDir, d.cfg.Workspace.RemotePath, tracerArgs, env)
}

// resolveTracerPath finds the mproxy-tracer binary using the config value,
// then falling back to adjacent binary and well-known paths.
func resolveTracerPath(configPath string) (string, error) {
	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil {
			return "", fmt.Errorf("configured exec.tracer_path not found: %w", err)
		}
		return configPath, nil
	}

	// Look alongside the main binary first.
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "mproxy-tracer")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	candidates := []string{
		"/opt/machineproxy/mproxy-tracer",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", errors.New("cannot find mproxy-tracer binary; set exec.tracer_path in config or place it alongside machineproxy")
}

func resolveAgentBinaryPath(configPath string) (string, error) {
	if p := os.Getenv("MPROXY_AGENT_BIN"); p != "" {
		return p, nil
	}

	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil {
			return "", fmt.Errorf("configured agent.local_path not found: %w", err)
		}
		return configPath, nil
	}

	// Look alongside the main binary first.
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		candidate := filepath.Join(dir, "mproxy-agent")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	candidates := []string{
		"/opt/machineproxy/mproxy-agent",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", errors.New("cannot find mproxy-agent binary; set MPROXY_AGENT_BIN, set agent.local_path in config, or place it alongside machineproxy")
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
