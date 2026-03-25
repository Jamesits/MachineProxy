package remoteexec

import (
	"context"
	"io"
)

type Request struct {
	Path     string   `json:"path"`
	Argv     []string `json:"argv"`
	Env      []string `json:"env"`
	Cwd      string   `json:"cwd"`
	ExtraFDs []uint32 `json:"extra_fds,omitempty"`
}

// Runner executes a request and writes command output to stdout/stderr.
// It returns the process exit code and an error if the request could not be started.
type Runner interface {
	Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error)
}

// Control provides additional channels for signal delivery and extra
// file descriptor bridging beyond basic stdin/stdout/stderr.
type Control struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Signals  <-chan int
	ExtraFDs map[uint32]io.ReadWriteCloser
}

// SignalableRunner extends Runner with signal and extra fd support.
type SignalableRunner interface {
	Runner
	RunWithControl(ctx context.Context, req Request, ctrl *Control) (int, error)
}

type RunnerFunc func(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error)

func (f RunnerFunc) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	return f(ctx, req, stdin, stdout, stderr)
}
