package supervisor

import (
	"context"
	"log/slog"
)

type SSH interface {
	StartSSH(ctx context.Context) error
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

type Deps struct {
	SSH      SSH
	NS       Namespace
	FS       WorkspaceFS
	Broker   Broker
	Launcher Launcher
	Log      *slog.Logger
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
	s.deps.Log.Debug("step 1/5: starting ssh")
	if err := s.deps.SSH.StartSSH(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 2/5: entering namespace")
	if err := s.deps.NS.EnterNamespace(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 3/5: mounting workspace")
	if err := s.deps.FS.MountWorkspace(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 4/5: starting broker")
	if err := s.deps.Broker.StartBroker(ctx); err != nil {
		return err
	}
	s.deps.Log.Debug("step 5/5: running child", "command", cmd)
	return s.deps.Launcher.RunChild(ctx, cmd)
}
