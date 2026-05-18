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
	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
	"github.com/jamesits/machineproxy/pkg/broker"
	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/envfilter"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/ns"
	"github.com/jamesits/machineproxy/pkg/pathstub"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
	"github.com/jamesits/machineproxy/pkg/sshconn"
	"github.com/jamesits/machineproxy/pkg/workspacefs"
)

type runtimeDeps struct {
	cfg *config.Config
	log *slog.Logger

	sshAddr         string // resolved "host:port" after ssh_config lookup
	sshManager      *sshconn.Manager
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

	raw := d.sshManager.SFTP()
	sftpClient, ok := raw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("ssh sftp client is not *sftp.Client")
	}

	// Resolve a possibly home-relative remote path against the SFTP
	// server's working dir (typically the remote user's home).
	remoteHome, err := sftpClient.Getwd()
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

	// Roll back any partial mount state on early-return errors so we never
	// leave a stray FUSE mount or empty temp directory behind. Without this,
	// failures after the tmpDir is created (e.g., a half-established FUSE
	// mount) only get cleaned up by Close() at process exit, which may run
	// long after the kernel mount has become a problem.
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
	backend := workspacefs.New(&workspacefs.SFTPAdapter{C: sftpClient}, mount.RemotePath, fsLog)
	server, err := workspacefs.Mount(ctx, backend, d.fuseMountDir)
	if err != nil {
		return fmt.Errorf("mount workspace fuse: %w", err)
	}
	d.fuseServer = server
	d.log.Debug("workspace mounted", "mount_dir", tmpDir, "remote_path", mount.RemotePath)
	success = true
	return nil
}

// BuildPathStubs enumerates remote PATH executables and mounts a
// read-only FUSE directory that surfaces them locally. It is a no-op
// when path_proxy is "disabled" or enumeration produced no entries.
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

	sftpRaw := d.sshManager.SFTP()
	sftpClient, ok := sftpRaw.(*sftp.Client)
	if !ok {
		return fmt.Errorf("sftp client is not *sftp.Client for path stub enumeration")
	}

	transferer := agenttransfer.New(
		func() *sftp.Client { return sftpClient },
		agentLocalPath,
		d.cfg.Components.AgentRemotePath,
		d.log.With("component", "transfer"),
	)

	runner := &remoteexec.AgentRunner{
		Provider:   d.sshManager,
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

	// Drop names that local_commands routes locally. Without this the
	// tracer would whitelist the stub and try to run a remote ELF as a
	// local binary, which is rarely what the user wants.
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

	fsLog := d.log.With("component", "pathstub-fuse")
	backend := pathstub.New(&pathstubOpener{c: sftpClient}, filtered, fsLog)
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

// pathstubOpener adapts *sftp.Client.Open (which returns *sftp.File) to
// pathstub.RemoteOpener (which requires RemoteFile). Go's interface
// satisfaction is invariant in return types, hence the trivial wrapper.
type pathstubOpener struct{ c *sftp.Client }

func (o *pathstubOpener) Open(path string) (pathstub.RemoteFile, error) {
	return o.c.Open(path)
}

func (d *runtimeDeps) StartBroker(ctx context.Context) error {
	// Keep the broker socket in a private directory so only this user can
	// connect to the control channel that can launch remote commands.
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
	// Two prefixes can be rewritten:
	//   - the workspace mount   (containerPath → remotePath)
	//   - the path-stub mount   (<stub_dir>/<name> → real remote binary)
	// MountWorkspace already resolved the remote half of the workspace
	// mount against the remote user's home directory.
	containerPrefix := d.workspaceMount.ContainerPath
	remotePrefix := d.workspaceMount.RemotePath
	stubPrefix := d.cfg.Container.PathStubDir
	stubMap := d.stubEntries // nil when path proxy is disabled or empty

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
	// stripStub removes the local stub mount from PATH before the agent
	// runs the remote child — the remote machine has no such directory.
	stripStub := stubPrefix
	if !hasStubRewrite {
		stripStub = ""
	}
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
			filtered := envfilter.Filter(env, envKeep, envRemove)
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
	// Directory creation is also guarded by a private umask so the entire
	// broker control path remains private even under permissive parent umasks.
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

	// Strip env vars that should not leak into the container process.
	env = envfilter.Remove(env, d.cfg.Container.EnvRemove)

	// Use the workspace mount's local-side path (already absolute after
	// ParseMount's tilde expansion) for the container bind target.
	mount := d.workspaceMount

	// Use explicit working_dir if set, otherwise default to the first mount's local path.
	workingDir := d.cfg.Container.WorkingDir
	if workingDir == "" {
		workingDir = mount.ContainerPath
	}

	binds := []ns.Bind{{Src: d.fuseMountDir, Dst: mount.ContainerPath}}
	if d.pathStubMountDir != "" {
		binds = append(binds, ns.Bind{Src: d.pathStubMountDir, Dst: d.cfg.Container.PathStubDir})
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
	return d.namespace.Run(ctx, workingDir, tracerArgs, env, binds)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}
