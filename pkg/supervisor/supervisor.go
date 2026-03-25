package supervisor

import "context"

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
}

type Supervisor struct {
	deps Deps
}

func New(deps Deps) *Supervisor {
	return &Supervisor{deps: deps}
}

func (s *Supervisor) Run(ctx context.Context, cmd []string) error {
	if err := s.deps.SSH.StartSSH(ctx); err != nil {
		return err
	}
	if err := s.deps.NS.EnterNamespace(ctx); err != nil {
		return err
	}
	if err := s.deps.FS.MountWorkspace(ctx); err != nil {
		return err
	}
	if err := s.deps.Broker.StartBroker(ctx); err != nil {
		return err
	}
	return s.deps.Launcher.RunChild(ctx, cmd)
}
