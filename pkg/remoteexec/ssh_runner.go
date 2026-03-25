package remoteexec

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/jamesits/machineproxy/pkg/sshconn"
	"golang.org/x/crypto/ssh"
)

// SessionProvider creates SSH sessions. Typically satisfied by *sshconn.Manager.
type SessionProvider interface {
	NewSession(ctx context.Context) (sshconn.Session, error)
}

type SSHRunner struct {
	Provider SessionProvider
}

func (r *SSHRunner) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	if r == nil || r.Provider == nil {
		return 127, fmt.Errorf("ssh runner provider is nil")
	}

	session, err := r.Provider.NewSession(ctx)
	if err != nil {
		return 127, err
	}
	defer session.Close()

	for _, kv := range req.Env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		_ = session.Setenv(k, v)
	}

	stdinPipe, err := session.StdinPipe()
	if err != nil {
		return 127, err
	}
	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return 127, err
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return 127, err
	}

	if err := session.Start(buildRemoteCommand(req)); err != nil {
		return 127, err
	}

	// Use a separate WaitGroup for stdout/stderr (must drain fully) vs
	// stdin (may block on the caller's reader indefinitely).
	var outWG sync.WaitGroup
	outWG.Add(2)

	go func() {
		_, _ = io.Copy(stdinPipe, stdin)
		_ = stdinPipe.Close()
	}()

	go func() {
		defer outWG.Done()
		_, _ = io.Copy(stdout, stdoutPipe)
	}()

	go func() {
		defer outWG.Done()
		_, _ = io.Copy(stderr, stderrPipe)
	}()

	waitErr := session.Wait()
	outWG.Wait()

	if waitErr == nil {
		return 0, nil
	}

	if exitErr, ok := waitErr.(*ssh.ExitError); ok {
		return exitErr.ExitStatus(), nil
	}

	return 127, waitErr
}

func buildRemoteCommand(req Request) string {
	parts := make([]string, 0, len(req.Argv))
	parts = append(parts, shellQuote(req.Path))
	// Argv[0] is the conventional program name (same as Path); actual
	// arguments start at Argv[1].
	if len(req.Argv) > 1 {
		for _, a := range req.Argv[1:] {
			parts = append(parts, shellQuote(a))
		}
	}
	cmd := strings.Join(parts, " ")

	if req.Cwd == "" {
		return cmd
	}
	return "cd " + shellQuote(req.Cwd) + " && " + cmd
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
