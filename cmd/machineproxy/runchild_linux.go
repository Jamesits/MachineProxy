//go:build linux

package main

import (
	"context"
	"strings"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/logging"
)

// buildChildInvocation wraps the user's cmdline with the ptrace tracer
// so every execve/execveat under the child is intercepted and routed
// through mproxy-shim.
func (d *runtimeDeps) buildChildInvocation(ctx context.Context, cmdline []string, shimBin string, env []string) ([]string, []string, error) {
	tracerBin, err := config.ResolveTracerPath(d.cfg.Components.TracerPath)
	if err != nil {
		return nil, nil, err
	}
	d.log.Log(ctx, logging.LevelTrace, "resolved tracer binary", "path", tracerBin)

	argv := []string{
		tracerBin,
		"--shim-path", shimBin,
		"--broker-sock", d.brokerSocket,
		"--log-level", d.cfg.Logging.Level,
	}
	if len(d.cfg.Container.LocalCommands) > 0 {
		argv = append(argv, "--whitelist", strings.Join(d.cfg.Container.LocalCommands, ":"))
	}
	if d.cwdTo != "" {
		argv = append(argv, "--cwd-from", d.cwdFrom, "--cwd-to", d.cwdTo)
	}
	argv = append(argv, "--")
	argv = append(argv, cmdline...)

	return argv, env, nil
}
