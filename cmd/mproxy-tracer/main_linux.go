//go:build linux

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/tracer"
)

func main() {
	cfg, childArgs, logLevel := parseArgs(os.Args[1:])
	if cfg == nil || len(childArgs) == 0 {
		slog.Error("usage: mproxy-tracer --shim-path PATH --broker-sock PATH [--whitelist PATH:PATH] [--cwd-from PATH --cwd-to PATH] [--log-level LEVEL] -- CMD [ARGS...]")
		os.Exit(2)
	}

	ctx := context.Background()

	log := logging.Setup(logLevel, os.Stderr)
	cfg.Log = log

	t := tracer.New(*cfg)

	// The child shares our process group (the terminal foreground group),
	// so terminal-generated signals (SIGINT, SIGQUIT) reach it directly.
	// We only forward SIGTERM/SIGHUP which come from the parent process.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)

	log.Debug("starting tracer", "command", childArgs)
	code, err := t.Start(ctx, childArgs, os.Environ(), func(childPid int) {
		go func() {
			for sig := range sigCh {
				if s, ok := sig.(syscall.Signal); ok {
					log.Log(ctx, logging.LevelTrace, "forwarding signal to child", "signal", s, "pid", childPid)
					_ = syscall.Kill(childPid, s)
				}
			}
		}()
	})
	signal.Stop(sigCh)

	if err != nil {
		log.Error("tracer failed", "error", err)
	}
	os.Exit(code)
}

// parseArgs parses tracer flags before "--" and returns the config, the
// remaining child command arguments, and the log level.
func parseArgs(args []string) (*tracer.Config, []string, string) {
	cfg := &tracer.Config{}
	logLevel := "info"

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--":
			return cfg, args[i+1:], logLevel
		case "--shim-path":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			cfg.ShimPath = args[i]
		case "--broker-sock":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			cfg.BrokerSock = args[i]
		case "--whitelist":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			cfg.Whitelist = strings.Split(args[i], ":")
		case "--cwd-from":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			cfg.CwdFrom = args[i]
		case "--cwd-to":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			cfg.CwdTo = args[i]
		case "--log-level":
			if i+1 >= len(args) {
				return nil, nil, logLevel
			}
			i++
			logLevel = args[i]
		default:
			// Treat first unrecognized arg as start of child command.
			return cfg, args[i:], logLevel
		}
		i++
	}

	return nil, nil, logLevel
}
