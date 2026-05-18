package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/remote"
)

// backendFactory is the constructor signature each backend's
// registration file exposes to the runtime.
type backendFactory func(context.Context, *config.Config, *slog.Logger) (remote.Backend, error)

// backendFactories is populated by init() functions in per-backend
// files (backend_ssh.go, backend_docker.go). Each registration file
// is guarded by a positive build tag (backend_ssh, backend_docker);
// backends omitted from -tags leave no entry behind.
var backendFactories = map[string]backendFactory{}

func registerBackend(name string, f backendFactory) {
	backendFactories[name] = f
}

// availableBackends lists the registered backend names in a stable
// order, for use in error messages.
func availableBackends() []string {
	names := make([]string, 0, len(backendFactories))
	for n := range backendFactories {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// buildBackend constructs the backend implementation indicated by
// cfg.Remote.Type. Returns a clear error when the requested backend
// was compiled out.
func buildBackend(ctx context.Context, cfg *config.Config, log *slog.Logger) (remote.Backend, error) {
	f, ok := backendFactories[cfg.Remote.Type]
	if !ok {
		if len(backendFactories) == 0 {
			return nil, fmt.Errorf("backend %q is not compiled into this binary (no backends available; rebuild with -tags backend_%s)", cfg.Remote.Type, cfg.Remote.Type)
		}
		return nil, fmt.Errorf("backend %q is not compiled into this binary (available: %v; rebuild with -tags backend_%s)", cfg.Remote.Type, availableBackends(), cfg.Remote.Type)
	}
	return f(ctx, cfg, log)
}
