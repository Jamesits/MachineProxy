package supervisor

import (
	"context"
	"log/slog"
)

// BackendStarter opens the remote connection (SSH dial, Docker
// attach, …). Kept as an interface so runtime wiring stays in main.
type BackendStarter interface {
	StartBackend(ctx context.Context) error
}

type Namespace interface {
	EnterNamespace(ctx context.Context) error
}

type WorkspaceFS interface {
	MountWorkspace(ctx context.Context) error
}

type Broker interface {
	StartBroker(ctx context.Context) error
}

type Launcher interface {
	RunChild(ctx context.Context, cmd []string) error
}

// PathStubs is optional. When provided it runs between the workspace
// FUSE mount and the broker start, building the read-only stub directory
// that fronts remote-PATH executables.
type PathStubs interface {
	BuildPathStubs(ctx context.Context) error
}

type Deps struct {
	Backend   BackendStarter
	NS        Namespace
	FS        WorkspaceFS
	PathStubs PathStubs // optional
	Broker    Broker
	Launcher  Launcher
	Log       *slog.Logger
}

type Supervisor struct {
	deps Deps
}

func New(deps Deps) *Supervisor {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Supervisor{deps: deps}
}

func (s *Supervisor) Run(ctx context.Context, cmd []string) error {
	s.deps.Log.Debug("step 1/6: starting backend")
	if err := s.deps.Backend.StartBackend(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 2/6: entering namespace")
	if err := s.deps.NS.EnterNamespace(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 3/6: mounting workspace")
	if err := s.deps.FS.MountWorkspace(ctx); err != nil {
		return err
	}
	if s.deps.PathStubs != nil {
		s.deps.Log.Debug("step 4/6: building path stubs")
		if err := s.deps.PathStubs.BuildPathStubs(ctx); err != nil {
			return err
		}
	}
	s.deps.Log.Debug("step 5/6: starting broker")
	if err := s.deps.Broker.StartBroker(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 6/6: running child", "command", cmd)
	return s.deps.Launcher.RunChild(ctx, cmd)
}
