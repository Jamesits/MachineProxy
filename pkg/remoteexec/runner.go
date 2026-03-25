package remoteexec

import (
	"context"
	"io"
)

type Request struct {
	Path string   `json:"path"`
	Argv []string `json:"argv"`
	Env  []string `json:"env"`
	Cwd  string   `json:"cwd"`
}

// Runner executes a request and writes command output to stdout/stderr.
// It returns the process exit code and an error if the request could not be started.
type Runner interface {
	Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error)
}

type RunnerFunc func(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error)

func (f RunnerFunc) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	return f(ctx, req, stdin, stdout, stderr)
}
