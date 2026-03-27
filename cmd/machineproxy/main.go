package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/supervisor"
)

func main() {
	if err := run(); err != nil {
		// Propagate the child process exit code when available.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "machineproxy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var cfgPath string
	var showVersion bool

	flag.StringVar(&cfgPath, "config", "config/machineproxy.example.yaml", "path to machineproxy config file")
	flag.BoolVar(&showVersion, "version", false, "print version")
	flag.Parse()

	if showVersion {
		fmt.Printf("machineproxy %s\n", config.Version)
		return nil
	}

	f, err := os.Open(cfgPath)
	if err != nil {
		return fmt.Errorf("open config %q: %w", cfgPath, err)
	}
	defer f.Close()

	cfg, err := config.Load(f)
	if err != nil {
		return fmt.Errorf("load config %q: %w", cfgPath, err)
	}

	log := logging.Setup(cfg.LogLevel)
	log.Debug("config loaded", "path", cfgPath, "version", config.Version)

	if flag.NArg() == 0 {
		return fmt.Errorf("missing command to run")
	}

	log.Debug("starting machineproxy", "command", flag.Args())

	deps, err := newRuntimeDeps(cfg, log)
	if err != nil {
		return err
	}
	defer deps.Close()

	sup := supervisor.New(supervisor.Deps{
		SSH:      deps,
		NS:       deps,
		FS:       deps,
		Broker:   deps,
		Launcher: deps,
		Log:      log,
	})

	return sup.Run(context.Background(), flag.Args())
}
