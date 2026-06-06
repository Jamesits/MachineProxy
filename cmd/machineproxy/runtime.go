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
	bkr             *broker.Server
	recorder        *agentproto.Recorder
	brokerSocket    string // auto-generated temp socket path
	brokerSocketDir string // private directory containing broker socket

	// mounts holds every realized container mount. Each distinct remote
	// path gets its own FUSE server; entries that share a remote path
	// (e.g. the identity entries added by container.auto_identity_mounts)
	// reuse an earlier mount's FUSE dir and carry a nil server, so the
	// same content is bound at several container paths via a single mount.
	// Populated by MountWorkspace; mounts[0] is the workspace.
	mounts []mountInstance

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

	// cwdFrom/cwdTo describe the getcwd(2)/$PWD prefix rewrite handed to the
	// tracer (container.cwd_remap). Empty cwdTo means no rewrite. Resolved
	// in RunChild via resolveCwdRemap.
	cwdFrom string
	cwdTo   string

	brokerCtxCancel context.CancelFunc
}

// mountInstance is one realized container mount. mount carries the
// container path and the remote-home-expanded remote path. fuseDir is the
// host directory where the remote subtree is FUSE-mounted; server owns that
// FUSE mount, or is nil when this instance reuses an earlier same-remote
// mount's fuseDir (only an extra bind is needed, not a second FUSE server).
type mountInstance struct {
	mount   config.Mount
	fuseDir string
	server  *fuse.Server
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
	// Unmount every FUSE server first, then remove the host dirs. Several
	// instances may share a fuseDir (same remote path), so remove each dir
	// only once.
	for _, inst := range d.mounts {
		if inst.server != nil {
			if err := inst.server.Unmount(); err != nil {
				d.log.Warn("failed to unmount fuse", "error", err, "mount_dir", inst.fuseDir)
			}
		}
	}
	removed := make(map[string]struct{}, len(d.mounts))
	for _, inst := range d.mounts {
		if inst.fuseDir == "" {
			continue
		}
		if _, done := removed[inst.fuseDir]; done {
			continue
		}
		removed[inst.fuseDir] = struct{}{}
		if err := os.RemoveAll(inst.fuseDir); err != nil {
			d.log.Warn("failed to remove fuse mount dir", "error", err, "mount_dir", inst.fuseDir)
		}
	}
	d.mounts = nil
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

// MountWorkspace realizes every container mount. Each distinct remote path
// gets its own FUSE server; entries that share a remote path (notably the
// identity aliases added by container.auto_identity_mounts) reuse the first
// mount's FUSE dir so the same content is later bound at several container
// paths. mounts[0] is the workspace and drives working-dir/cwd resolution.
func (d *runtimeDeps) MountWorkspace(ctx context.Context) error {
	if err := preflightFUSE(); err != nil {
		return err
	}

	fc, err := d.backend.Files(ctx)
	if err != nil {
		return err
	}
	remoteHome, err := fc.Getwd()
	if err != nil {
		return fmt.Errorf("get remote home dir for mount expansion: %w", err)
	}

	fsLog := d.log.With("component", "fuse")
	opts, err := workspacefsOptions(d.cfg)
	if err != nil {
		return fmt.Errorf("workspace fs options: %w", err)
	}

	// fuseByRemote maps a resolved remote path to the FUSE dir already
	// serving it, so repeated remote paths share one server. containerSeen
	// guards against two mounts claiming the same bind target.
	fuseByRemote := make(map[string]string)
	containerSeen := make(map[string]struct{})

	success := false
	defer func() {
		if success {
			return
		}
		for _, inst := range d.mounts {
			if inst.server != nil {
				if uerr := inst.server.Unmount(); uerr != nil {
					d.log.Warn("failed to unmount fuse after mount error", "error", uerr)
				}
			}
		}
		removed := make(map[string]struct{}, len(d.mounts))
		for _, inst := range d.mounts {
			if inst.fuseDir == "" {
				continue
			}
			if _, done := removed[inst.fuseDir]; done {
				continue
			}
			removed[inst.fuseDir] = struct{}{}
			if rerr := os.RemoveAll(inst.fuseDir); rerr != nil {
				d.log.Warn("failed to remove fuse mount dir after mount error", "error", rerr)
			}
		}
		d.mounts = nil
	}()

	for _, raw := range d.cfg.Container.Mounts {
		mount, perr := config.ParseMount(raw)
		if perr != nil {
			return fmt.Errorf("parse mount %q: %w", raw, perr)
		}
		resolvedRemote, eerr := config.ExpandRemoteHome(mount.RemotePath, remoteHome)
		if eerr != nil {
			return fmt.Errorf("expand remote mount path %q: %w", mount.RemotePath, eerr)
		}
		mount.RemotePath = resolvedRemote

		// The local path (bind target) usually exists as a directory already;
		// bwrap will create the mountpoint regardless, but a missing or
		// non-directory path is almost always a typo worth flagging.
		if st, lerr := os.Stat(mount.ContainerPath); lerr != nil {
			if errors.Is(lerr, os.ErrNotExist) {
				d.log.Warn("mount local path does not exist",
					"local_path", mount.ContainerPath, "mount", raw)
			} else {
				d.log.Warn("mount local path not accessible",
					"local_path", mount.ContainerPath, "mount", raw, "error", lerr)
			}
		} else if !st.IsDir() {
			d.log.Warn("mount local path is not a directory",
				"local_path", mount.ContainerPath, "mount", raw)
		}

		d.log.Log(ctx, logging.LevelTrace, "mounting workspace",
			"remote_path", mount.RemotePath, "container_path", mount.ContainerPath)

		// Reuse an existing FUSE mount when another entry already serves
		// this remote path; otherwise stand up a fresh server for it.
		fuseDir, shared := fuseByRemote[mount.RemotePath]
		var server *fuse.Server
		if shared {
			d.log.Debug("reusing fuse mount for shared remote path",
				"remote_path", mount.RemotePath, "mount_dir", fuseDir)
		} else {
			st, serr := fc.Stat(mount.RemotePath)
			if serr != nil {
				serr = fmt.Errorf("remote mount %q not reachable: %w", mount.RemotePath, serr)
				d.log.Error("mount not found", "error", serr)
				return serr
			}
			if !st.IsDir() {
				err = fmt.Errorf("remote mount %q is not a directory", mount.RemotePath)
				d.log.Error("mount not a directory", "error", err)
				return err
			}

			tmpDir, terr := os.MkdirTemp("", "machineproxy-fuse-*")
			if terr != nil {
				return fmt.Errorf("create temp mountpoint: %w", terr)
			}
			// Canonicalize so it matches what the child sees via os.Getwd
			// later. On darwin /var → /private/var is a symlink that would
			// otherwise break the broker's pathMapper prefix match.
			if resolved, rerr := filepath.EvalSymlinks(tmpDir); rerr == nil {
				tmpDir = resolved
			}
			fuseDir = tmpDir

			backend := workspacefs.New(fc, mount.RemotePath, fsLog, opts)
			server, err = workspacefs.Mount(ctx, backend, fuseDir)
			if err != nil {
				return fmt.Errorf("mount workspace fuse: %w", err)
			}
			fuseByRemote[mount.RemotePath] = fuseDir
			d.log.Debug("workspace mounted", "mount_dir", fuseDir, "remote_path", mount.RemotePath)
		}

		// On darwin there is no kernel-level bind-mount, so each mount lives
		// at its real FUSE temp path rather than at its configured container
		// path; the broker's pathMapper translates that prefix to the remote
		// path. Identity aliases cannot be realized without bind mounts.
		if runtime.GOOS == "darwin" && mount.ContainerPath != fuseDir {
			if mount.ContainerPath != "" {
				d.log.Warn("darwin: overriding container_path with FUSE mount dir (no bind-mount available)",
					"configured", mount.ContainerPath,
					"effective", fuseDir,
				)
			}
			mount.ContainerPath = fuseDir
		}

		// Two mounts at the same container path would collide as bind
		// targets; keep the first and warn about the rest.
		if _, dup := containerSeen[mount.ContainerPath]; dup {
			d.log.Warn("duplicate container path; ignoring later mount",
				"container_path", mount.ContainerPath, "remote_path", mount.RemotePath, "mount", raw)
			continue
		}
		containerSeen[mount.ContainerPath] = struct{}{}

		d.mounts = append(d.mounts, mountInstance{
			mount:   mount,
			fuseDir: fuseDir,
			server:  server,
		})
	}

	if len(d.mounts) == 0 {
		return errors.New("no container mounts configured")
	}
	d.workspaceMount = d.mounts[0].mount

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

	if d.cfg.Logging.RecordingFile != "" && d.recorder == nil {
		rec, recErr := agentproto.NewRecorder(d.cfg.Logging.RecordingFile)
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

	stubPrefix := d.cfg.Container.PathStubDir
	stubMap := d.stubEntries

	mounts := make([]config.Mount, len(d.mounts))
	for i, inst := range d.mounts {
		mounts[i] = inst.mount
	}
	pathMapper := buildPathMapper(mounts, stubPrefix, stubMap)

	brokerLog := d.log.With("component", "broker")
	envKeep := d.cfg.Agent.EnvKeep
	envRemove := d.cfg.Agent.EnvRemove
	_ = envKeep
	stripStub := stubPrefix
	if len(stubMap) == 0 {
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

	// Pick the directory to launch the first program in (container.cwd_mode).
	// An explicit mode with an invalid working_dir fails startup here.
	workingDir, err := d.resolveWorkingDir()
	if err != nil {
		return err
	}
	// Enable the tracer's getcwd/$PWD remap hook when container.cwd_remap=remote.
	d.resolveCwdRemap()

	// Bind every realized mount into the container. Instances that share a
	// remote path point at the same FUSE dir, so the same content surfaces
	// at each container path (this is what makes auto_identity_mounts work).
	binds := make([]ns.Bind, 0, len(d.mounts)+1)
	for _, inst := range d.mounts {
		binds = append(binds, ns.Bind{Src: inst.fuseDir, Dst: inst.mount.ContainerPath})
	}
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

// resolveWorkingDir picks the directory the first program is launched in,
// per container.cwd_mode. A non-empty container.working_dir always wins;
// otherwise the mode selects the first mount's local or remote path. For
// "explicit" mode working_dir is required and must be a local directory,
// otherwise startup fails with an ENOENT error.
func (d *runtimeDeps) resolveWorkingDir() (string, error) {
	mode := d.cfg.Container.CwdMode
	wd := d.cfg.Container.WorkingDir // raw config value; "" means unset

	if mode == config.CwdModeExplicit {
		if wd == "" {
			return "", fmt.Errorf("container.cwd_mode=explicit requires container.working_dir: %w", syscall.ENOENT)
		}
		if fi, err := os.Stat(wd); err != nil || !fi.IsDir() {
			d.log.Error("container.cwd_mode=explicit: working_dir is not a local directory",
				"working_dir", wd, "error", err)
			return "", fmt.Errorf("container.working_dir %q is not a local directory: %w", wd, syscall.ENOENT)
		}
		return wd, nil
	}

	if wd != "" {
		return wd, nil
	}
	if mode == config.CwdModeRemote {
		return d.workspaceMount.RemotePath, nil
	}
	// inherit / local: the first mount's container-side path (the default).
	return d.workspaceMount.ContainerPath, nil
}

// resolveCwdRemap enables the tracer's getcwd/$PWD remap hook when
// container.cwd_remap=remote, rewriting the first mount's container-path
// prefix to its remote path. It is a no-op (hook disabled) for "local" or
// when the two sides coincide.
func (d *runtimeDeps) resolveCwdRemap() {
	if d.cfg.Container.CwdRemap != config.CwdRemapRemote {
		return
	}
	if d.workspaceMount.ContainerPath == d.workspaceMount.RemotePath {
		return
	}
	d.cwdFrom = d.workspaceMount.ContainerPath
	d.cwdTo = d.workspaceMount.RemotePath
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
