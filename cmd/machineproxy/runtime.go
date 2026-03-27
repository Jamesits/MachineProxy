package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/ns"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
	"github.com/jamesits/machineproxy/pkg/sshconn"
	"github.com/jamesits/machineproxy/pkg/workspacefs"
)

type runtimeDeps struct {
	cfg *config.Config
	log *slog.Logger

	sshManager   *sshconn.Manager
	namespace    *ns.Namespace
	fuseServer   *fuse.Server
	bkr          *broker.Server
	fuseMountDir string
	recorder     *agentproto.Recorder

	brokerCtxCancel context.CancelFunc
}

func newRuntimeDeps(cfg *config.Config, log *slog.Logger) (*runtimeDeps, error) {
	sshLog := log.With("component", "ssh")
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
		log: log,
		sshManager: sshconn.NewManager(sshconn.Options{
			Dial:              dial,
			ReconnectInterval: time.Second,
			KeepAliveInterval: cfg.SSH.KeepAlive,
			Log:               sshLog,
		}),
		namespace: ns.New(ns.Deps{Log: log.With("component", "ns")}),
	}, nil
}

func (d *runtimeDeps) Close() {
	d.log.Debug("shutting down")
	if d.recorder != nil {
		if err := d.recorder.Close(); err != nil {
			d.log.Warn("failed to close recorder", "error", err)
		}
	}
	if d.brokerCtxCancel != nil {
		d.brokerCtxCancel()
	}
	if d.fuseServer != nil {
		if err := d.fuseServer.Unmount(); err != nil {
			d.log.Warn("failed to unmount fuse", "error", err)
		}
		d.fuseServer = nil
	}
	if d.fuseMountDir != "" {
		if err := os.RemoveAll(d.fuseMountDir); err != nil {
			d.log.Warn("failed to remove fuse mount dir", "error", err)
		}
		d.fuseMountDir = ""
	}
	d.namespace.Leave()
	if d.sshManager != nil {
		if err := d.sshManager.Close(); err != nil {
			d.log.Warn("failed to close ssh manager", "error", err)
		}
	}
}

func (d *runtimeDeps) StartSSH(ctx context.Context) error {
	d.log.Log(ctx, logging.LevelTrace, "starting ssh manager", "addr", d.cfg.SSH.Addr, "user", d.cfg.SSH.User)
	if err := d.sshManager.Start(ctx); err != nil {
		return err
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		if d.sshManager.IsConnected() {
			d.log.Debug("ssh connected", "addr", d.cfg.SSH.Addr)
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
	d.log.Log(ctx, logging.LevelTrace, "preparing namespace")
	if err := d.namespace.Prepare(ctx); err != nil {
		return err
	}
	d.log.Debug("namespace ready")
	return nil
}

func (d *runtimeDeps) MountWorkspace(ctx context.Context) error {
	d.log.Log(ctx, logging.LevelTrace, "mounting workspace", "remote_path", d.cfg.Workspace.RemotePath)
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

	fsLog := d.log.With("component", "fuse")
	backend := workspacefs.New(&workspacefs.SFTPAdapter{C: sftpClient}, d.cfg.Workspace.RemotePath, fsLog)
	server, err := workspacefs.Mount(ctx, backend, d.fuseMountDir)
	if err != nil {
		return fmt.Errorf("mount workspace fuse: %w", err)
	}
	d.fuseServer = server
	d.log.Debug("workspace mounted", "mount_dir", tmpDir, "remote_path", d.cfg.Workspace.RemotePath)
	return nil
}

func (d *runtimeDeps) StartBroker(ctx context.Context) error {
	d.log.Log(ctx, logging.LevelTrace, "starting broker", "socket", d.cfg.Broker.SocketPath)
	if err := os.MkdirAll(filepath.Dir(d.cfg.Broker.SocketPath), 0o755); err != nil {
		return fmt.Errorf("create broker socket dir: %w", err)
	}

	agentLocalPath, err := config.ResolveAgentBinaryPath(d.cfg.Agent.LocalPath)
	if err != nil {
		return fmt.Errorf("resolve agent binary: %w", err)
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved agent binary", "path", agentLocalPath)

	sftpRaw := d.sshManager.SFTP()
	sftpClient, ok := sftpRaw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("sftp client is not *sftp.Client for agent transfer")
	}

	transferer := agenttransfer.New(
		func() *sftp.Client { return sftpClient },
		agentLocalPath,
		d.cfg.Agent.RemotePath,
		d.log.With("component", "transfer"),
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
		if err := rec.WriteSessionHeader(&agentproto.SessionHeader{
			Version:   config.Version,
			StartTime: time.Now().UnixNano(),
			LocalUser: username,
			LocalPID:  os.Getpid(),
			SSHAddr:   d.cfg.SSH.Addr,
			SSHUser:   d.cfg.SSH.User,
			AgentPath: d.cfg.Agent.RemotePath,
		}); err != nil {
			d.log.Warn("failed to write session header", "error", err)
		}
	}

	brokerLog := d.log.With("component", "broker")
	envKeep := d.cfg.Exec.EnvKeep
	envRemove := d.cfg.Exec.EnvRemove
	d.bkr = broker.NewServer(broker.Deps{
		Remote: &remoteexec.AgentRunner{
			Provider:   d.sshManager,
			Transferer: transferer,
			Recorder:   d.recorder,
			Log:        d.log.With("component", "runner"),
		},
		EnvFilter: func(env []string) []string {
			return envfilter.Filter(env, envKeep, envRemove)
		},
		Log: brokerLog,
	})
	bctx, cancel := context.WithCancel(ctx)
	d.brokerCtxCancel = cancel

	go func() {
		if err := d.bkr.Start(bctx, d.cfg.Broker.SocketPath); err != nil {
			d.log.Warn("broker stopped with error", "error", err)
		}
	}()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(d.cfg.Broker.SocketPath); err == nil {
			d.log.Debug("broker ready", "socket", d.cfg.Broker.SocketPath)
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

	tracerBin, err := config.ResolveTracerPath(d.cfg.Exec.TracerPath)
	if err != nil {
		return err
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved tracer binary", "path", tracerBin)

	shimBin, err := config.ResolveShimPath(d.cfg.Exec.ShimPath)
	if err != nil {
		return err
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved shim binary", "path", shimBin)

	env := ns.FormatEnv(
		os.Environ(),
		d.cfg.Broker.SocketPath,
		shimBin,
	)

	// Wrap the command in the ptrace-based tracer so exec interception
	// works with both dynamically and statically linked binaries.
	tracerArgs := []string{
		tracerBin,
		"--shim-path", shimBin,
		"--broker-sock", d.cfg.Broker.SocketPath,
		"--log-level", d.cfg.LogLevel,
	}
	if len(d.cfg.Exec.LocalCommands) > 0 {
		tracerArgs = append(tracerArgs, "--whitelist", strings.Join(d.cfg.Exec.LocalCommands, ":"))
	}
	tracerArgs = append(tracerArgs, "--")
	tracerArgs = append(tracerArgs, cmdline...)

	d.log.Log(ctx, logging.LevelTrace, "launching child via tracer", "tracer", tracerBin, "command", cmdline)
	return d.namespace.Run(ctx, d.fuseMountDir, d.cfg.Workspace.RemotePath, tracerArgs, env)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
