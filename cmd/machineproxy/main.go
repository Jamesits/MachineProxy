package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/initcmd"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/supervisor"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		// Propagate the child process exit code when available.
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

	if parsed.host != "" {
		cfg.Remote.SSH.Host = parsed.host
	}
	if parsed.user != "" {
		cfg.Remote.SSH.User = parsed.user
	}
	if parsed.port != 0 {
		cfg.Remote.SSH.Port = parsed.port
	}
	if parsed.arch != "" {
		cfg.Remote.Arch = parsed.arch
	}
	if len(parsed.mounts) > 0 {
		// -v entries take precedence over config, so put them first.
		// The first mount also seeds container.working_dir when unset.
		cfg.Container.Mounts = append(parsed.mounts, cfg.Container.Mounts...)
	}
	if parsed.workdir != "" {
		cfg.Container.WorkingDir = parsed.workdir
	}
	// If nothing supplied mounts (neither config nor -v), fall back to the
	// caller's current directory so `machineproxy host -- cmd` works from
	// any workspace without bespoke configuration.
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

	log := logging.Setup(cfg.LogLevel)
	log.Debug("config loaded", "path", parsed.cfgPath, "version", config.Version)
	if cfgJSON, err := json.Marshal(cfg); err == nil {
		log.Log(ctx, logging.LevelTrace, "parsed config", "content", string(cfgJSON))
	} else {
		log.Warn("failed to marshal config for trace log", "error", err)
	}
	if len(parsed.cmd) == 0 {
		return fmt.Errorf("missing command to run")
	}

	// Resolve the entrypoint here, outside the container, against the
	// host PATH minus the path-stub directory. The path-stub serves
	// FUSE-backed remote ELFs that the local kernel cannot load, so
	// even when path_proxy is "prepend" the initial exec must come
	// from a real local binary.
	initcmdLog := log.With("component", "initcmd")
	resolved, err := initcmd.LookPath(ctx, initcmdLog, parsed.cmd[0], os.Getenv("PATH"), cfg.Container.PathStubDir)
	if err != nil {
		return fmt.Errorf("resolve initial command %q: %w", parsed.cmd[0], err)
	}
	log.Debug("initial command resolved", "input", parsed.cmd[0], "resolved", resolved)
	parsed.cmd[0] = resolved

	// Auto-whitelist the entrypoint (plus its shebang chain) so the
	// tracer lets the kernel's exec-recursion through binfmt_script and
	// any in-process re-exec land locally instead of being routed to
	// the remote, where the same path may not exist.
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

	log.Debug("starting machineproxy", "command", parsed.cmd)

	deps, err := newRuntimeDeps(cfg, log)
	if err != nil {
		return err
	}
	defer deps.Close()

	sup := supervisor.New(supervisor.Deps{
		SSH:       deps,
		NS:        deps,
		FS:        deps,
		PathStubs: deps,
		Broker:    deps,
		Launcher:  deps,
		Log:       log,
	})

	return sup.Run(ctx, parsed.cmd)
}

// cliArgs captures everything parsed off the command line: machineproxy's
// own flags, the OpenSSH-style destination override, and the child
// command to run inside the container.
type cliArgs struct {
	cfgPath     string
	showVersion bool
	port        int // 0 = unset; overrides remote.ssh.port when nonzero
	user        string
	host        string
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

// parseCLIArgs implements the OpenSSH-compatible invocation:
//
//	machineproxy [flags] [-p port] [-l user] [[user@]host] [--] [cmd...]
//
// The first standalone "--" separates the destination section from the
// command section. Because Go's flag package consumes a "--" that
// immediately follows the last flag, we split on "--" ourselves before
// invoking flag.Parse so callers can write `machineproxy -- cmd` to mean
// "keep the destination from the config file."
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
	// Register both the OpenSSH-style short flag and a long alias for each
	// override. Both point at the same destination so either form parses.
	const portUsage = "remote SSH `port` (overrides remote.ssh.port)"
	fs.IntVar(&out.port, "p", 0, portUsage)
	fs.IntVar(&out.port, "port", 0, portUsage)
	const loginUsage = "remote SSH `user` (overrides remote.ssh.user and any user@host destination)"
	fs.StringVar(&loginUser, "l", "", loginUsage)
	fs.StringVar(&loginUser, "login", "", loginUsage)
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
			out.user, out.host = splitUserHost(positional[0])
		}
		if len(afterSep) > 0 {
			out.cmd = afterSep
		}
	} else if len(positional) > 0 {
		out.user, out.host = splitUserHost(positional[0])
		if len(positional) > 1 {
			out.cmd = positional[1:]
		}
	}

	// -l/--login wins over any user encoded in [user@]host, matching
	// `ssh -l`.
	if loginUser != "" {
		out.user = loginUser
	}
	return out, nil
}

// splitUserHost splits an OpenSSH-style "[user@]host" destination. The
// last "@" is the separator so IPv6 literals like "user@2001:db8::1"
// parse correctly. A bare "host" returns user="".
func splitUserHost(s string) (user, host string) {
	if i := strings.LastIndex(s, "@"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}
