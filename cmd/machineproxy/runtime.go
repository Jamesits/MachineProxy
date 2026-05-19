package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
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

func newRuntimeDeps(ctx context.Context, cfg *config.Config, log *slog.Logger) (*runtimeDeps, error) {
	backend, err := buildBackend(ctx, cfg, log)
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

	d.resolveRemotePlatform(ctx)

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

// resolveRemotePlatform fills in cfg.Remote.OS/Arch when the user did
// not pin them via CLI flags or the config file. Each field is handled
// independently: an operator who configured just the OS still benefits
// from arch detection, and vice versa. Detection errors are logged at
// warn level and the field falls back to the local GOOS/GOARCH so the
// run can still proceed.
func (d *runtimeDeps) resolveRemotePlatform(ctx context.Context) {
	if d.cfg.Remote.OS != "" && d.cfg.Remote.Arch != "" {
		return
	}
	info, err := d.backend.DetectPlatform(ctx)
	if err != nil {
		d.log.Warn("remote platform detection failed; falling back to local defaults",
			"error", err,
			"default_os", runtime.GOOS,
			"default_arch", runtime.GOARCH,
		)
	}
	if d.cfg.Remote.OS == "" {
		switch {
		case info.OS != "":
			d.cfg.Remote.OS = info.OS
			d.log.Debug("remote OS detected", "os", info.OS)
		default:
			d.cfg.Remote.OS = runtime.GOOS
			if err == nil {
				d.log.Warn("remote OS not reported by backend; falling back to local default",
					"default_os", runtime.GOOS)
			}
		}
	}
	if d.cfg.Remote.Arch == "" {
		switch {
		case info.Arch != "":
			d.cfg.Remote.Arch = info.Arch
			if info.Variant != "" {
				d.log.Debug("remote arch detected", "arch", info.Arch, "variant", info.Variant)
			} else {
				d.log.Debug("remote arch detected", "arch", info.Arch)
			}
		default:
			d.cfg.Remote.Arch = runtime.GOARCH
			if err == nil {
				d.log.Warn("remote arch not reported by backend; falling back to local default",
					"default_arch", runtime.GOARCH)
			}
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
	if err := preflightFUSE(); err != nil {
		return err
	}

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
	// Canonicalize the path so it matches what the child sees via
	// os.Getwd later. On darwin, /var → /private/var is a symlink that
	// otherwise breaks the broker's pathMapper prefix match.
	if resolved, rerr := filepath.EvalSymlinks(tmpDir); rerr == nil {
		tmpDir = resolved
	}
	d.fuseMountDir = tmpDir

	// On darwin there is no kernel-level bind-mount equivalent, so the
	// FUSE mount lives at its real temp path rather than being mapped
	// into a stable container_path. Rewrite the mount metadata to that
	// real path; the broker's pathMapper translates that prefix to the
	// remote path when forwarding execs and file requests.
	if runtime.GOOS == "darwin" && mount.ContainerPath != tmpDir {
		if mount.ContainerPath != "" {
			d.log.Warn("darwin: overriding container_path with FUSE mount dir (no bind-mount available)",
				"configured", mount.ContainerPath,
				"effective", tmpDir,
			)
		}
		mount.ContainerPath = tmpDir
		d.workspaceMount = mount
	}

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
	opts, err := workspacefsOptions(d.cfg)
	if err != nil {
		return fmt.Errorf("workspace fs options: %w", err)
	}
	backend := workspacefs.New(fc, mount.RemotePath, fsLog, opts)
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
	// Apply the user's env_remove patterns to the inherited shell env
	// BEFORE FormatEnv injects MPROXY_BROKER_SOCK and MPROXY_SHIM_PATH.
	// Otherwise the default container.env_remove ("MPROXY_*") would
	// strip our own variables, leaving the shim/dylib with no way to
	// find the broker socket.
	base := envfilter.Remove(os.Environ(), d.cfg.Container.EnvRemove)
	env := ns.FormatEnv(
		base,
		d.brokerSocket,
		shimBin,
		pathInj,
	)

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

	childArgv, childEnv, err := d.buildChildInvocation(ctx, cmdline, shimBin, env)
	if err != nil {
		return err
	}

	d.log.Log(ctx, logging.LevelTrace, "launching child", "command", childArgv)
	return d.namespace.Run(ctx, workingDir, childArgv, childEnv, binds)
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// workspacefsOptions translates the user-facing uid_mode / gid_mode
// config strings into the workspacefs.Options struct, capturing the
// local user's UID/GID at the same time so the override mode has
// something concrete to report. uid_map / gid_map entries are also
// compiled here; they take precedence over the mode fallback.
func workspacefsOptions(cfg *config.Config) (*workspacefs.Options, error) {
	opts := &workspacefs.Options{
		UIDMode: workspacefs.IDMode(cfg.Container.UIDMode),
		GIDMode: workspacefs.IDMode(cfg.Container.GIDMode),
	}
	uidMap, err := compileIDMap(cfg.Container.UIDMap, config.LookupUID, "container.uid_map")
	if err != nil {
		return nil, err
	}
	gidMap, err := compileIDMap(cfg.Container.GIDMap, config.LookupGroupGID, "container.gid_map")
	if err != nil {
		return nil, err
	}
	opts.UIDMap = uidMap
	opts.GIDMap = gidMap
	// Look up the local UID/GID once. On Linux/darwin os.Getuid/Getgid
	// always succeed; the user.Current() roundtrip would be needed only
	// to surface the username, which workspacefs does not consume.
	uid := os.Getuid()
	gid := os.Getgid()
	if uid < 0 || gid < 0 {
		return nil, fmt.Errorf("local uid/gid lookup returned negative values (uid=%d, gid=%d)", uid, gid)
	}
	opts.LocalUID = uint32(uid)
	opts.LocalGID = uint32(gid)
	return opts, nil
}

func compileIDMap(entries []string, lookup config.IDLookup, field string) ([]workspacefs.IDMapEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]workspacefs.IDMapEntry, 0, len(entries))
	for _, raw := range entries {
		e, err := config.CompileIDMapEntry(raw, lookup)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field, err)
		}
		out = append(out, workspacefs.IDMapEntry{
			RemoteID: e.RemoteID,
			LocalID:  e.LocalID,
			Count:    e.Count,
		})
	}
	return out, nil
}
