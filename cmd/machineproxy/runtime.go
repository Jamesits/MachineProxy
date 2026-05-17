package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
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

	sshAddr      string // resolved "host:port" after ssh_config lookup
	sshManager   *sshconn.Manager
	namespace    *ns.Namespace
	fuseServer   *fuse.Server
	bkr          *broker.Server
	fuseMountDir string
	recorder     *agentproto.Recorder
	brokerSocket string // auto-generated temp socket path

	brokerCtxCancel context.CancelFunc
}

func newRuntimeDeps(cfg *config.Config, log *slog.Logger) (*runtimeDeps, error) {
	sshLog := log.With("component", "ssh")
	dialer, err := sshconn.NewDialer(sshconn.DialConfig{
		Host:    cfg.Remote.SSH.Host,
		User:    cfg.Remote.SSH.User,
		Port:    cfg.Remote.SSH.Port,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}

	return &runtimeDeps{
		cfg:     cfg,
		log:     log,
		sshAddr: dialer.Addr,
		sshManager: sshconn.NewManager(sshconn.Options{
			Dial:              dialer.Dial,
			ReconnectInterval: time.Second,
			KeepAliveInterval: dialer.KeepAliveInterval,
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
	// Clean up the broker socket to prevent leak.
	if d.brokerSocket != "" {
		_ = os.Remove(d.brokerSocket)
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
	d.log.Log(ctx, logging.LevelTrace, "starting ssh manager", "addr", d.sshAddr, "user", d.cfg.Remote.SSH.User)
	if err := d.sshManager.Start(ctx); err != nil {
		return err
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		if d.sshManager.IsConnected() {
			d.log.Debug("ssh connected", "addr", d.sshAddr)
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
	// Parse the first mount entry for the workspace FUSE mount.
	mount, err := config.ParseMount(d.cfg.Container.Mounts[0])
	if err != nil {
		return fmt.Errorf("parse workspace mount: %w", err)
	}

	d.log.Log(ctx, logging.LevelTrace, "mounting workspace", "remote_path", mount.RemotePath)

	raw := d.sshManager.SFTP()
	sftpClient, ok := raw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("ssh sftp client is not *sftp.Client")
	}

       // Verify the remote workspace is reachable up front. Without this check,
       // FUSE happily mounts an unusable backend and the failure surfaces later
       // as a confusing "bwrap: source No such file or directory" because every
       // stat on the mountpoint forwards to a failing SFTP Stat.
       st, err := sftpClient.Stat(mount.RemotePath)
       if err != nil {
               return fmt.Errorf("remote workspace %q not reachable: %w", mount.RemotePath, err)
       }
       if !st.IsDir() {
               return fmt.Errorf("remote workspace %q is not a directory", mount.RemotePath)
       }

       tmpDir, err := os.MkdirTemp("", "machineproxy-fuse-*")
       if err != nil {
               return fmt.Errorf("create temp mountpoint: %w", err)
       }
       d.fuseMountDir = tmpDir

	fsLog := d.log.With("component", "fuse")
	backend := workspacefs.New(&workspacefs.SFTPAdapter{C: sftpClient}, mount.RemotePath, fsLog)
	server, err := workspacefs.Mount(ctx, backend, d.fuseMountDir)
	if err != nil {
		return fmt.Errorf("mount workspace fuse: %w", err)
	}
	d.fuseServer = server
	d.log.Debug("workspace mounted", "mount_dir", tmpDir, "remote_path", mount.RemotePath)
	return nil
}

func (d *runtimeDeps) StartBroker(ctx context.Context) error {
	// Generate a random temporary socket path to avoid collisions.
	f, err := os.CreateTemp("", "machineproxy-*.sock")
	if err != nil {
		return fmt.Errorf("create temp broker socket: %w", err)
	}
	socketPath := f.Name()
	if err := f.Close(); err != nil {
		d.log.Warn("close temp broker socket file", "path", socketPath, "error", err)
	}
	// broker.Start will create the Unix socket at this path; remove the placeholder first.
	if err := os.Remove(socketPath); err != nil {
		d.log.Warn("remove temp broker socket placeholder", "path", socketPath, "error", err)
	}
	d.brokerSocket = socketPath

	d.log.Log(ctx, logging.LevelTrace, "starting broker", "socket", socketPath)

	agentLocalPath, err := config.ResolveAgentBinaryPath(
		d.cfg.Components.AgentLocalPath,
		d.cfg.Remote.OS,
		d.cfg.Remote.Arch,
	)
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
		d.cfg.Components.AgentRemotePath,
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
			SSHAddr:   d.sshAddr,
			SSHUser:   d.cfg.Remote.SSH.User,
			AgentPath: d.cfg.Components.AgentRemotePath,
		}); err != nil {
			d.log.Warn("failed to write session header", "error", err)
		}
	}

	// Build a path mapper that rewrites container-local paths to remote
	// paths so the agent can chdir and exec correctly on the remote host.
	mount, _ := config.ParseMount(d.cfg.Container.Mounts[0])
	var pathMapper func(string) string
	if mount.ContainerPath != mount.RemotePath {
		containerPrefix := mount.ContainerPath
		remotePrefix := mount.RemotePath
		pathMapper = func(p string) string {
			if p == containerPrefix {
				return remotePrefix
			}
			// Match containerPrefix/ to avoid partial prefix matches.
			if strings.HasPrefix(p, containerPrefix+"/") {
				return remotePrefix + p[len(containerPrefix):]
			}
			return p
		}
	}

	brokerLog := d.log.With("component", "broker")
	envKeep := d.cfg.Agent.EnvKeep
	envRemove := d.cfg.Agent.EnvRemove
	d.bkr = broker.NewServer(broker.Deps{
		Remote: &remoteexec.AgentRunner{
			Provider:   d.sshManager,
			Transferer: transferer,
			Recorder:   d.recorder,
			AgentConfig: &agentproto.AgentConfig{
				EnvKeep:   d.cfg.Agent.EnvKeep,
				EnvRemove: d.cfg.Agent.EnvRemove,
			},
			Log: d.log.With("component", "runner"),
		},
		EnvFilter: func(env []string) []string {
			return envfilter.Filter(env, envKeep, envRemove)
		},
		PathMapper: pathMapper,
		Log:        brokerLog,
	})
	bctx, cancel := context.WithCancel(ctx)
	d.brokerCtxCancel = cancel

	go func() {
		if err := d.bkr.Start(bctx, socketPath); err != nil {
			d.log.Warn("broker stopped with error", "error", err)
		}
	}()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if _, err := os.Stat(socketPath); err == nil {
			d.log.Debug("broker ready", "socket", socketPath)
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

	tracerBin, err := config.ResolveTracerPath(d.cfg.Components.TracerPath)
	if err != nil {
		return err
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved tracer binary", "path", tracerBin)

	shimBin, err := config.ResolveShimPath(d.cfg.Components.ShimPath)
	if err != nil {
		return err
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved shim binary", "path", shimBin)

	env := ns.FormatEnv(
		os.Environ(),
		d.brokerSocket,
		shimBin,
	)

	// Strip env vars that should not leak into the container process.
	env = envfilter.Remove(env, d.cfg.Container.EnvRemove)

	// Parse the first mount entry for container path binding.
	mount, _ := config.ParseMount(d.cfg.Container.Mounts[0])

	// Use explicit working_dir if set, otherwise default to the first mount's local path.
	workingDir := d.cfg.Container.WorkingDir
	if workingDir == "" {
		workingDir = mount.ContainerPath
	}

	// Wrap the command in the ptrace-based tracer so exec interception
	// works with both dynamically and statically linked binaries.
	tracerArgs := []string{
		tracerBin,
		"--shim-path", shimBin,
		"--broker-sock", d.brokerSocket,
		"--log-level", d.cfg.LogLevel,
	}
	if len(d.cfg.Container.LocalCommands) > 0 {
		tracerArgs = append(tracerArgs, "--whitelist", strings.Join(d.cfg.Container.LocalCommands, ":"))
	}
	tracerArgs = append(tracerArgs, "--")
	tracerArgs = append(tracerArgs, cmdline...)

	d.log.Log(ctx, logging.LevelTrace, "launching child via tracer", "tracer", tracerBin, "command", cmdline)
	return d.namespace.Run(ctx, d.fuseMountDir, mount.ContainerPath, workingDir, tracerArgs, env)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
