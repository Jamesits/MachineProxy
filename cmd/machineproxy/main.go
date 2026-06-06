package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/initcmd"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/remote"
	"github.com/jamesits/machineproxy/pkg/supervisor"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "machineproxy: %v\n", err)
		os.Exit(1)
	}
}

func run(rawArgs []string) error {
	parsed, err := parseCLIArgs(rawArgs)
	if err != nil {
		return err
	}
	if parsed.showVersion {
		fmt.Printf("machineproxy %s\n", config.Version)
		return nil
	}

	cfg, err := config.LoadFileRaw(parsed.cfgPath)
	if err != nil {
		return fmt.Errorf("load config %q: %w", parsed.cfgPath, err)
	}

	// CLI --backend overrides config remote.type.
	if parsed.backend != "" {
		cfg.Remote.Type = parsed.backend
	}

	// Resolve the destination (if supplied) and apply it to the
	// right backend-specific config block. The destination's scheme
	// (when present) selects the backend even without --backend.
	if parsed.destination != "" {
		defaultType := remote.TypeSSH
		if cfg.Remote.Type != "" {
			defaultType = remote.Type(cfg.Remote.Type)
		}
		dst, err := remote.ParseDestination(parsed.destination, defaultType)
		if err != nil {
			return err
		}
		if parsed.backend != "" && parsed.backend != string(dst.Type) {
			return fmt.Errorf("--backend=%s conflicts with destination scheme %s://", parsed.backend, dst.Type)
		}
		cfg.Remote.Type = string(dst.Type)
		switch dst.Type {
		case remote.TypeSSH:
			cfg.Remote.SSH.Host = dst.Host
			if dst.User != "" {
				cfg.Remote.SSH.User = dst.User
			}
			if dst.Port != 0 {
				cfg.Remote.SSH.Port = dst.Port
			}
		case remote.TypeDocker:
			cfg.Remote.Docker.Container = dst.Host
		case remote.TypeCompose:
			cfg.Remote.Compose.Project = dst.Host
			cfg.Remote.Compose.Service = dst.Service
			cfg.Remote.Compose.Sequence = dst.Sequence
		}
	}

	if parsed.port != 0 {
		if remote.Type(cfg.Remote.Type) != remote.TypeSSH {
			return fmt.Errorf("-p/--port is only valid for backend ssh (got %q)", cfg.Remote.Type)
		}
		cfg.Remote.SSH.Port = parsed.port
	}
	if parsed.user != "" {
		if remote.Type(cfg.Remote.Type) != remote.TypeSSH {
			return fmt.Errorf("-l/--login is only valid for backend ssh (got %q)", cfg.Remote.Type)
		}
		cfg.Remote.SSH.User = parsed.user
	}
	if parsed.os != "" {
		cfg.Remote.OS = parsed.os
	}
	if parsed.arch != "" {
		cfg.Remote.Arch = parsed.arch
	}
	if len(parsed.mounts) > 0 {
		// CLI mounts are prepended to config mounts, because the first entry of container.mounts can be set
		// implicitly as the working directory, and we want the user to be able to use this feature.
		cfg.Container.Mounts = append(parsed.mounts, cfg.Container.Mounts...)
	}
	if parsed.workdir != "" {
		cfg.Container.WorkingDir = parsed.workdir
	}
	if len(cfg.Container.Mounts) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("default container mount to current dir: %w", err)
		}
		cfg.Container.Mounts = []string{cwd}
	}

	if err := cfg.Finalize(); err != nil {
		return err
	}

	ctx := context.Background()

	logWriter, logCloser, err := openLogWriter(cfg.LogFile)
	if err != nil {
		return fmt.Errorf("open log_file %q: %w", cfg.LogFile, err)
	}
	defer logCloser()
	log := logging.Setup(cfg.LogLevel, logWriter)
	log.Debug("config loaded", "path", parsed.cfgPath, "version", config.Version)
	if cfgJSON, err := json.Marshal(cfg); err == nil {
		log.Log(ctx, logging.LevelTrace, "parsed config", "content", string(cfgJSON))
	} else {
		log.Warn("failed to marshal config for trace log", "error", err)
	}
	if len(parsed.cmd) == 0 {
		return fmt.Errorf("missing command to run")
	}

	initcmdLog := log.With("component", "initcmd")
	resolved, err := initcmd.LookPath(ctx, initcmdLog, parsed.cmd[0], os.Getenv("PATH"), cfg.Container.PathStubDir)
	if err != nil {
		return fmt.Errorf("resolve initial command %q: %w", parsed.cmd[0], err)
	}
	log.Debug("initial command resolved", "input", parsed.cmd[0], "resolved", resolved)
	parsed.cmd[0] = resolved

	if cfg.Container.ForceResolveInitialCommandLocally != nil && *cfg.Container.ForceResolveInitialCommandLocally {
		derived, derr := initcmd.DeriveLocalCommands(ctx, initcmdLog, resolved)
		if derr != nil {
			log.Warn("derive local_commands for initial command", "path", resolved, "error", derr)
		}
		if len(derived) > 0 {
			log.Debug("auto-whitelisting initial command", "entries", derived)
			cfg.Container.LocalCommands = append(cfg.Container.LocalCommands, derived...)
		}
	}

	if len(cfg.Container.LocalCommands) == 0 {
		log.Debug("container.local_commands is empty; every exec will be forwarded to the remote")
	}

	log.Debug("starting machineproxy", "command", parsed.cmd, "backend", cfg.Remote.Type)

	deps, err := newRuntimeDeps(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer deps.Close()

	sup := supervisor.New(supervisor.Deps{
		Backend:   deps,
		NS:        deps,
		FS:        deps,
		PathStubs: deps,
		Broker:    deps,
		Launcher:  deps,
		Log:       log,
	})

	return sup.Run(ctx, parsed.cmd)
}

// openLogWriter returns a writer for log output and a closer to call on exit.
// When path is empty the writer is os.Stderr and the closer is a no-op.
func openLogWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// cliArgs captures everything parsed off the command line: machineproxy's
// own flags, the destination override, and the child command to run inside
// the container.
type cliArgs struct {
	cfgPath     string
	showVersion bool
	backend     string // empty = use config; "ssh"/"docker" otherwise
	destination string // raw destination string; parsed in run()
	port        int    // 0 = unset; overrides remote.ssh.port when nonzero
	user        string
	os          string   // empty = unset; overrides remote.os when set
	arch        string   // empty = unset; overrides remote.arch when set
	mounts      []string // prepended to container.mounts (CLI first)
	workdir     string   // empty = unset; overrides container.working_dir
	cmd         []string
}

// repeatedString is a flag.Value that accumulates each occurrence of a
// flag into a slice, in the order they appeared on the command line.
type repeatedString struct{ dst *[]string }

func (r *repeatedString) String() string {
	if r == nil || r.dst == nil {
		return ""
	}
	return strings.Join(*r.dst, ",")
}

func (r *repeatedString) Set(v string) error {
	*r.dst = append(*r.dst, v)
	return nil
}

// parseCLIArgs implements:
//
//	machineproxy [flags] [-p port] [-l user] DEST [-- cmd...]
//
// DEST is either a bare host (which defaults to ssh), or a
// scheme-prefixed destination such as ssh://user@host:port or
// docker://container.
func parseCLIArgs(args []string) (*cliArgs, error) {
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}

	var flagPart, afterSep []string
	sepPresent := sepIdx >= 0
	if sepPresent {
		flagPart = args[:sepIdx]
		afterSep = args[sepIdx+1:]
	} else {
		flagPart = args
	}

	out := &cliArgs{}
	var loginUser string
	fs := flag.NewFlagSet("machineproxy", flag.ContinueOnError)
	fs.StringVar(&out.cfgPath, "config", "/etc/machineproxy/machineproxy.toml", "path to machineproxy config file (TOML/YAML/JSON)")
	fs.BoolVar(&out.showVersion, "version", false, "print version")
	fs.StringVar(&out.backend, "backend", "", "remote backend `type` (ssh or docker); overrides remote.type from config")
	const portUsage = "remote SSH `port` (overrides remote.ssh.port; ssh-only)"
	fs.IntVar(&out.port, "p", 0, portUsage)
	fs.IntVar(&out.port, "port", 0, portUsage)
	const loginUsage = "remote SSH `user` (overrides remote.ssh.user; ssh-only)"
	fs.StringVar(&loginUser, "l", "", loginUsage)
	fs.StringVar(&loginUser, "login", "", loginUsage)
	fs.StringVar(&out.os, "os", "", "remote machine `os` for agent binary selection (overrides remote.os)")
	fs.StringVar(&out.arch, "arch", "", "remote machine `arch` for agent binary selection (overrides remote.arch)")
	const mountUsage = "container mount entry in [local:]remote form (prepended to container.mounts; may be repeated)"
	mountFlag := &repeatedString{dst: &out.mounts}
	fs.Var(mountFlag, "v", mountUsage)
	fs.Var(mountFlag, "mount", mountUsage)
	const workdirUsage = "container working `dir` (overrides container.working_dir)"
	fs.StringVar(&out.workdir, "w", "", workdirUsage)
	fs.StringVar(&out.workdir, "workdir", "", workdirUsage)
	if err := fs.Parse(flagPart); err != nil {
		return nil, err
	}

	positional := fs.Args()
	if sepPresent {
		if len(positional) > 1 {
			return nil, fmt.Errorf("unexpected arguments before --: %v", positional[1:])
		}
		if len(positional) == 1 {
			out.destination = positional[0]
		}
		if len(afterSep) > 0 {
			out.cmd = afterSep
		}
	} else if len(positional) > 0 {
		out.destination = positional[0]
		if len(positional) > 1 {
			out.cmd = positional[1:]
		}
	}

	if loginUser != "" {
		out.user = loginUser
	}
	return out, nil
}
