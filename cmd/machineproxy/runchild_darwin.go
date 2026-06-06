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

	// getcwd(3)/$PWD remapping (container.cwd_remap=remote). The interposer
	// reads MPROXY_CWD_FROM/TO to rewrite getcwd results and $PWD on exec; we
	// also remap $PWD in the initial env so the first process and its
	// descendants inherit the mapped value.
	if d.cwdTo != "" {
		out = append(out, "MPROXY_CWD_FROM="+d.cwdFrom, "MPROXY_CWD_TO="+d.cwdTo)
		out = remapPWDEnv(out, d.cwdFrom, d.cwdTo)
		d.log.Log(ctx, logging.LevelTrace, "cwd remap enabled for interposer",
			"from", d.cwdFrom, "to", d.cwdTo)
	}

	return cmdline, out, nil
}

// remapPWDEnv rewrites a "PWD=" entry in env using the CwdFrom→CwdTo prefix
// rule, so the first process's logical working directory matches the
// interposer's getcwd remap. The first PWD entry wins; the slice is returned
// unchanged when no remap applies.
func remapPWDEnv(env []string, from, to string) []string {
	for i, e := range env {
		v, ok := strings.CutPrefix(e, "PWD=")
		if !ok {
			continue
		}
		mapped, matched := config.RewritePathPrefix(v, from, to)
		if !matched || mapped == v {
			return env
		}
		out := append([]string(nil), env...)
		out[i] = "PWD=" + mapped
		return out
	}
	return env
}
