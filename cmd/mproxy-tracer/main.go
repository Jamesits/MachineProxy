package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/tracer"
)

func main() {
	cfg, childArgs := parseArgs(os.Args[1:])
	if cfg == nil || len(childArgs) == 0 {
		fmt.Fprintf(os.Stderr, "usage: mproxy-tracer --shim-path PATH --broker-sock PATH [--whitelist PATH:PATH] -- CMD [ARGS...]\n")
		os.Exit(2)
	}

	t := tracer.New(*cfg)

	// The child shares our process group (the terminal foreground group),
	// so terminal-generated signals (SIGINT, SIGQUIT) reach it directly.
	// We only forward SIGTERM/SIGHUP which come from the parent process.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP)

	code, err := t.Start(childArgs, os.Environ(), func(childPid int) {
		go func() {
			for sig := range sigCh {
				if s, ok := sig.(syscall.Signal); ok {
					_ = syscall.Kill(childPid, s)
				}
			}
		}()
	})
	signal.Stop(sigCh)

	if err != nil {
		fmt.Fprintf(os.Stderr, "mproxy-tracer: %v\n", err)
	}
	os.Exit(code)
}

// parseArgs parses tracer flags before "--" and returns the config and the
// remaining child command arguments.
func parseArgs(args []string) (*tracer.Config, []string) {
	cfg := &tracer.Config{}

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--":
			return cfg, args[i+1:]
		case "--shim-path":
			if i+1 >= len(args) {
				return nil, nil
			}
			i++
			cfg.ShimPath = args[i]
		case "--broker-sock":
			if i+1 >= len(args) {
				return nil, nil
			}
			i++
			cfg.BrokerSock = args[i]
		case "--whitelist":
			if i+1 >= len(args) {
				return nil, nil
			}
			i++
			cfg.Whitelist = strings.Split(args[i], ":")
		default:
			// Treat first unrecognized arg as start of child command.
			return cfg, args[i:]
		}
		i++
	}

	return nil, nil
}
