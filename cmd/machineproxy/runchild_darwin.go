//go:build darwin

package main

import (
	"context"
	"strings"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/logging"
)

// buildChildInvocation runs the user's cmdline directly. Exec
// interception is delivered by the DYLD interposer dylib injected via
// DYLD_INSERT_LIBRARIES; there is no separate tracer process.
//
// MPROXY_WHITELIST is the colon-joined list of paths the dylib will
// allow to run locally (matching the Linux tracer's --whitelist flag).
//
// MPROXY_BROKER_SOCK and MPROXY_SHIM_PATH are already injected upstream
// by ns.FormatEnv; we add DYLD_INSERT_LIBRARIES and MPROXY_WHITELIST on
// top.
//
// The dylib may not be installed yet (e.g. during Phase 1 of the macOS
// port). When ResolveInterposerPath returns an error we log a warning
// and run without injection — the child will work, but every exec
// falls through to local execution rather than being routed remotely.
func (d *runtimeDeps) buildChildInvocation(ctx context.Context, cmdline []string, shimBin string, env []string) ([]string, []string, error) {
	_ = shimBin // already in env via ns.FormatEnv as MPROXY_SHIM_PATH

	out := append([]string(nil), env...)

	if dylib, err := config.ResolveInterposerPath(d.cfg.Components.InterposerPath); err == nil {
		d.log.Log(ctx, logging.LevelTrace, "resolved interposer dylib", "path", dylib)
		out = append(out, "DYLD_INSERT_LIBRARIES="+dylib)
	} else {
		d.log.Warn("interposer dylib not found; exec interception disabled", "error", err)
	}

	if len(d.cfg.Container.LocalCommands) > 0 {
		out = append(out, "MPROXY_WHITELIST="+strings.Join(d.cfg.Container.LocalCommands, ":"))
	}

	return cmdline, out, nil
}
