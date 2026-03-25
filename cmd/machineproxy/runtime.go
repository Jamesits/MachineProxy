package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/broker"
	"github.com/jamesits/machineproxy/pkg/config"
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

	d.bkr = broker.NewServer(broker.Deps{Remote: &remoteexec.SSHRunner{Provider: d.sshManager}})
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

	hookLibrary, err := resolveHookLibraryPath()
	if err != nil {
		return err
	}

	env := ns.FormatEnv(
		os.Environ(),
		d.cfg.Broker.SocketPath,
		d.cfg.Exec.ShimPath,
		d.cfg.Exec.LocalCommands,
		hookLibrary,
	)

	return d.namespace.Run(ctx, d.fuseMountDir, d.cfg.Workspace.RemotePath, cmdline, env)
}

func resolveHookLibraryPath() (string, error) {
	if p := os.Getenv("MPROXY_HOOK_LIB"); p != "" {
		return p, nil
	}

	candidates := []string{
		"/opt/machineproxy/libmproxyhook.so",
		"c/hook/libmproxyhook.so",
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", errors.New("cannot find hook library; set MPROXY_HOOK_LIB or build c/hook/libmproxyhook.so")
}
