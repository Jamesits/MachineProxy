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
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
	"github.com/jamesits/machineproxy/pkg/broker"
	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/envfilter"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/ns"
	"github.com/jamesits/machineproxy/pkg/pathstub"
	"github.com/jamesits/machineproxy/pkg/remote"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
	"github.com/jamesits/machineproxy/pkg/workspacefs"
)

type runtimeDeps struct {
	cfg *config.Config
	log *slog.Logger

	backend         remote.Backend
	namespace       *ns.Namespace
	fuseServer      *fuse.Server
	bkr             *broker.Server
	fuseMountDir    string
	recorder        *agentproto.Recorder
	brokerSocket    string // auto-generated temp socket path
	brokerSocketDir string // private directory containing broker socket

	// Path-stub state. pathStubServer/MountDir are populated only when
	// cfg.Container.PathProxy is "prepend" or "append" and the
	// enumeration succeeded. stubEntries maps stub name → remote path
	// for the broker's PathMapper composition.
	pathStubServer   *fuse.Server
	pathStubMountDir string
	stubEntries      map[string]pathstub.Entry

	// workspaceMount holds the first container mount with its
	// RemotePath already expanded against the remote user's home.
	// Populated by MountWorkspace and consumed by StartBroker/RunChild.
	workspaceMount config.Mount

	brokerCtxCancel context.CancelFunc
}

func newRuntimeDeps(cfg *config.Config, log *slog.Logger) (*runtimeDeps, error) {
	backend, err := buildBackend(cfg, log)
	if err != nil {
		return nil, err
	}
	return &runtimeDeps{
		cfg:       cfg,
		log:       log,
		backend:   backend,
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
	if d.brokerSocket != "" {
		_ = os.Remove(d.brokerSocket)
	}
	if d.brokerSocketDir != "" {
		if err := os.RemoveAll(d.brokerSocketDir); err != nil {
			d.log.Warn("failed to remove broker socket dir", "error", err)
		}
		d.brokerSocketDir = ""
	}
	if d.pathStubServer != nil {
		if err := d.pathStubServer.Unmount(); err != nil {
			d.log.Warn("failed to unmount path-stub fuse", "error", err)
		}
		d.pathStubServer = nil
	}
	if d.pathStubMountDir != "" {
		if err := os.RemoveAll(d.pathStubMountDir); err != nil {
			d.log.Warn("failed to remove path-stub mount dir", "error", err)
		}
		d.pathStubMountDir = ""
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
	if d.backend != nil {
		if err := d.backend.Close(); err != nil {
			d.log.Warn("failed to close backend", "error", err)
		}
	}
}

// StartBackend opens the remote connection and (for Docker) uploads
// the agent binary so the FileClient can come online.
func (d *runtimeDeps) StartBackend(ctx context.Context) error {
	d.log.Log(ctx, logging.LevelTrace, "starting backend", "type", d.backend.Type(), "addr", d.backend.Addr())
	if err := d.backend.Start(ctx); err != nil {
		return err
	}
	d.log.Debug("backend connected", "type", d.backend.Type(), "addr", d.backend.Addr())

	// For backends that need an explicit bootstrap upload before
	// Files() can return a usable client, do it now. SSH self-hosts
	// the agent via SFTP, so its UploadAgent is harmless to call
	// eagerly too — but we keep the call out of the SSH path because
	// the existing flow uploads lazily via agenttransfer.Transferer.
	if d.backend.Type() == remote.TypeDocker {
		agentLocalPath, err := config.ResolveAgentBinaryPath(
			d.cfg.Components.AgentLocalPath,
			d.cfg.Remote.OS,
			d.cfg.Remote.Arch,
		)
		if err != nil {
			return fmt.Errorf("resolve agent binary: %w", err)
		}
		if _, err := d.backend.UploadAgent(ctx, agentLocalPath, d.cfg.Components.AgentRemotePath, 0o755); err != nil {
			return fmt.Errorf("upload agent to docker container: %w", err)
		}
	}
	return nil
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
	mount, err := config.ParseMount(d.cfg.Container.Mounts[0])
	if err != nil {
		return fmt.Errorf("parse workspace mount: %w", err)
	}

	fc, err := d.backend.Files(ctx)
	if err != nil {
		return err
	}

	remoteHome, err := fc.Getwd()
	if err != nil {
		return fmt.Errorf("get remote home dir for mount expansion: %w", err)
	}
	resolvedRemote, err := config.ExpandRemoteHome(mount.RemotePath, remoteHome)
	if err != nil {
		return fmt.Errorf("expand remote mount path %q: %w", mount.RemotePath, err)
	}
	mount.RemotePath = resolvedRemote
	d.workspaceMount = mount

	d.log.Log(ctx, logging.LevelTrace, "mounting workspace", "remote_path", mount.RemotePath)

	st, err := fc.Stat(mount.RemotePath)
	if err != nil {
		err = fmt.Errorf("remote workspace %q not reachable: %w", mount.RemotePath, err)
		d.log.Error("workspace not found", "error", err)
		return err
	}
	if !st.IsDir() {
		err = fmt.Errorf("remote workspace %q is not a directory", mount.RemotePath)
		d.log.Error("workspace not a directory", "error", err)
		return err
	}

	tmpDir, err := os.MkdirTemp("", "machineproxy-fuse-*")
	if err != nil {
		return fmt.Errorf("create temp mountpoint: %w", err)
	}
	d.fuseMountDir = tmpDir

	success := false
	defer func() {
		if success {
			return
		}
		if d.fuseServer != nil {
			if uerr := d.fuseServer.Unmount(); uerr != nil {
				d.log.Warn("failed to unmount fuse after mount error", "error", uerr)
			}
			d.fuseServer = nil
		}
		if rerr := os.RemoveAll(tmpDir); rerr != nil {
			d.log.Warn("failed to remove fuse mount dir after mount error", "error", rerr)
		}
		d.fuseMountDir = ""
	}()

	fsLog := d.log.With("component", "fuse")
	backend := workspacefs.New(fc, mount.RemotePath, fsLog)
	server, err := workspacefs.Mount(ctx, backend, d.fuseMountDir)
	if err != nil {
		return fmt.Errorf("mount workspace fuse: %w", err)
	}
	d.fuseServer = server
	d.log.Debug("workspace mounted", "mount_dir", tmpDir, "remote_path", mount.RemotePath)
	success = true
	return nil
}

func (d *runtimeDeps) BuildPathStubs(ctx context.Context) error {
	if d.cfg.Container.PathProxy == "disabled" {
		d.log.Debug("path proxy disabled; skipping enumeration")
		return nil
	}

	d.log.Log(ctx, logging.LevelTrace, "enumerating remote PATH")

	agentLocalPath, err := config.ResolveAgentBinaryPath(
		d.cfg.Components.AgentLocalPath,
		d.cfg.Remote.OS,
		d.cfg.Remote.Arch,
	)
	if err != nil {
		return fmt.Errorf("resolve agent binary: %w", err)
	}

	transferer := agenttransfer.New(
		d.backend,
		agentLocalPath,
		d.cfg.Components.AgentRemotePath,
		d.log.With("component", "transfer"),
	)

	runner := &remoteexec.AgentRunner{
		Provider:   d.backend,
		Transferer: transferer,
		AgentConfig: &agentproto.AgentConfig{
			EnvKeep:   d.cfg.Agent.EnvKeep,
			EnvRemove: d.cfg.Agent.EnvRemove,
		},
		Log: d.log.With("component", "pathstub-runner"),
	}

	entries, err := runner.EnumeratePaths(ctx, nil)
	if err != nil {
		return fmt.Errorf("enumerate remote PATH: %w", err)
	}
	d.log.Debug("path enumeration returned entries", "count", len(entries))

	totalEntries := len(entries)
	filtered := pathstub.FilterLocalCommands(entries, d.cfg.Container.LocalCommands, d.cfg.Container.PathStubDir)
	if skipped := totalEntries - len(filtered); skipped > 0 {
		d.log.Debug("path stubs shadowed by local_commands", "skipped", skipped)
	}

	if len(filtered) == 0 {
		d.log.Info("path proxy: no remote executables to mount")
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "machineproxy-pathstub-*")
	if err != nil {
		return fmt.Errorf("create temp mountpoint: %w", err)
	}
	d.pathStubMountDir = tmpDir

	success := false
	defer func() {
		if success {
			return
		}
		if d.pathStubServer != nil {
			if uerr := d.pathStubServer.Unmount(); uerr != nil {
				d.log.Warn("failed to unmount path-stub fuse after mount error", "error", uerr)
			}
			d.pathStubServer = nil
		}
		if rerr := os.RemoveAll(tmpDir); rerr != nil {
			d.log.Warn("failed to remove path-stub mount dir after mount error", "error", rerr)
		}
		d.pathStubMountDir = ""
	}()

	fc, err := d.backend.Files(ctx)
	if err != nil {
		return err
	}
	fsLog := d.log.With("component", "pathstub-fuse")
	backend := pathstub.New(fc, filtered, fsLog)
	server, err := pathstub.Mount(ctx, backend, tmpDir)
	if err != nil {
		return fmt.Errorf("mount path-stub fuse: %w", err)
	}
	d.pathStubServer = server
	d.stubEntries = backend.Entries()
	d.log.Debug("path stubs mounted", "count", len(filtered), "mount_dir", tmpDir)
	success = true
	return nil
}

func (d *runtimeDeps) StartBroker(ctx context.Context) error {
	socketPath, err := createBrokerSocketPath()
	if err != nil {
		return fmt.Errorf("create broker socket path: %w", err)
	}
	d.brokerSocketDir = filepath.Dir(socketPath)
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

	transferer := agenttransfer.New(
		d.backend,
		agentLocalPath,
		d.cfg.Components.AgentRemotePath,
		d.log.With("component", "transfer"),
	)

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
			Version:     config.Version,
			StartTime:   time.Now().UnixNano(),
			LocalUser:   username,
			LocalPID:    os.Getpid(),
			BackendType: string(d.backend.Type()),
			BackendAddr: d.backend.Addr(),
			SSHAddr:     sshAddrIfSSH(d.backend),
			SSHUser:     d.backend.User(),
			AgentPath:   d.cfg.Components.AgentRemotePath,
		}); err != nil {
			d.log.Warn("failed to write session header", "error", err)
		}
	}

	containerPrefix := d.workspaceMount.ContainerPath
	remotePrefix := d.workspaceMount.RemotePath
	stubPrefix := d.cfg.Container.PathStubDir
	stubMap := d.stubEntries

	hasWorkspaceRewrite := containerPrefix != remotePrefix
	hasStubRewrite := len(stubMap) > 0
	var pathMapper func(string) string
	if hasWorkspaceRewrite || hasStubRewrite {
		pathMapper = func(p string) string {
			if hasStubRewrite && strings.HasPrefix(p, stubPrefix+"/") {
				name := p[len(stubPrefix)+1:]
				if e, ok := stubMap[name]; ok {
					return e.RemotePath
				}
			}
			if hasWorkspaceRewrite {
				if p == containerPrefix {
					return remotePrefix
				}
				if strings.HasPrefix(p, containerPrefix+"/") {
					return remotePrefix + p[len(containerPrefix):]
				}
			}
			return p
		}
	}

	brokerLog := d.log.With("component", "broker")
	envKeep := d.cfg.Agent.EnvKeep
	envRemove := d.cfg.Agent.EnvRemove
	_ = envKeep
	stripStub := stubPrefix
	if !hasStubRewrite {
		stripStub = ""
	}
	d.bkr = broker.NewServer(broker.Deps{
		Remote: &remoteexec.AgentRunner{
			Provider:   d.backend,
			Transferer: transferer,
			Recorder:   d.recorder,
			AgentConfig: &agentproto.AgentConfig{
				EnvKeep:   d.cfg.Agent.EnvKeep,
				EnvRemove: d.cfg.Agent.EnvRemove,
			},
			Log: d.log.With("component", "runner"),
		},
		EnvFilter: func(env []string) []string {
			filtered := envfilter.Filter(env, d.cfg.Agent.EnvKeep, envRemove)
			return envfilter.StripPathSegment(filtered, stripStub)
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

func createBrokerSocketPath() (string, error) {
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	dir, err := os.MkdirTemp("", "machineproxy-*")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "broker.sock"), nil
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

	pathInj := ns.PathInjection{}
	if d.pathStubMountDir != "" {
		pathInj = ns.PathInjection{
			Dir:      d.cfg.Container.PathStubDir,
			Position: d.cfg.Container.PathProxy,
		}
	}
	env := ns.FormatEnv(
		os.Environ(),
		d.brokerSocket,
		shimBin,
		pathInj,
	)

	env = envfilter.Remove(env, d.cfg.Container.EnvRemove)

	mount := d.workspaceMount

	workingDir := d.cfg.Container.WorkingDir
	if workingDir == "" {
		workingDir = mount.ContainerPath
		d.log.Info("container working_dir not set; defaulting to first mount", "working_dir", workingDir)
	}

	binds := []ns.Bind{{Src: d.fuseMountDir, Dst: mount.ContainerPath}}
	if d.pathStubMountDir != "" {
		binds = append(binds, ns.Bind{Src: d.pathStubMountDir, Dst: d.cfg.Container.PathStubDir})
	}

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
	return d.namespace.Run(ctx, workingDir, tracerArgs, env, binds)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// sshAddrIfSSH returns the backend's address only when the backend is
// SSH. The recording header keeps SSHAddr populated for SSH-style
// sessions for back-compat with existing parsers.
func sshAddrIfSSH(b remote.Backend) string {
	if b.Type() == remote.TypeSSH {
		return b.Addr()
	}
	return ""
}
