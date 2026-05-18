//go:build backend_docker

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// dockerSession turns a docker exec invocation into a remote.Session.
// Lifecycle:
//   - NewSession() (in Backend) constructs an unstarted session.
//   - StdinPipe/StdoutPipe/StderrPipe set up pipes connected to the
//     hijacked exec stream (which doesn't exist yet — they will be
//     wired up at Start).
//   - Setenv buffers env vars; applied when Start creates the exec.
//   - Start launches the exec and begins demuxing the hijacked stream.
//   - Wait polls ContainerExecInspect for completion.
type dockerSession struct {
	cli         *client.Client
	containerID string

	env []string

	stdinPipeR *io.PipeReader
	stdinPipeW *io.PipeWriter
	stdoutR    *io.PipeReader
	stdoutW    *io.PipeWriter
	stderrR    *io.PipeReader
	stderrW    *io.PipeWriter

	startMu sync.Mutex
	started bool
	closed  bool
	execID  string
	hijack  io.Closer

	doneCh   chan struct{}
	waitErr  error
	exitCode int

	bgCtx    context.Context
	cancelBg context.CancelFunc
}

func newSession(cli *client.Client, containerID string) *dockerSession {
	s := &dockerSession{cli: cli, containerID: containerID}
	s.stdinPipeR, s.stdinPipeW = io.Pipe()
	s.stdoutR, s.stdoutW = io.Pipe()
	s.stderrR, s.stderrW = io.Pipe()
	s.doneCh = make(chan struct{})
	return s
}

func (s *dockerSession) StdinPipe() (io.WriteCloser, error) {
	return s.stdinPipeW, nil
}

func (s *dockerSession) StdoutPipe() (io.Reader, error) {
	return s.stdoutR, nil
}

func (s *dockerSession) StderrPipe() (io.Reader, error) {
	return s.stderrR, nil
}

func (s *dockerSession) Setenv(name, value string) error {
	s.env = append(s.env, name+"="+value)
	return nil
}

// Start parses cmdline as "sh -c cmdline" so callers that pass shell
// quoting (matching the SSH runner's buildRemoteCommand output) keep
// working unchanged.
func (s *dockerSession) Start(cmdline string) error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if s.started {
		return errors.New("docker session already started")
	}
	s.started = true

	bgCtx, cancel := context.WithCancel(context.Background())
	s.bgCtx = bgCtx
	s.cancelBg = cancel

	created, err := s.cli.ContainerExecCreate(bgCtx, s.containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Env:          s.env,
		Cmd:          []string{"sh", "-c", cmdline},
	})
	if err != nil {
		s.closePipes()
		return fmt.Errorf("docker exec create: %w", err)
	}
	s.execID = created.ID

	attach, err := s.cli.ContainerExecAttach(bgCtx, s.execID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		s.closePipes()
		return fmt.Errorf("docker exec attach: %w", err)
	}
	s.hijack = attach.Conn

	// Caller stdin → hijack stdin.
	go func() {
		_, _ = io.Copy(attach.Conn, s.stdinPipeR)
		_ = attach.CloseWrite()
	}()

	// Hijack stream → demux into stdout/stderr pipes for the caller.
	go func() {
		_, err := stdcopy.StdCopy(s.stdoutW, s.stderrW, attach.Reader)
		_ = s.stdoutW.Close()
		_ = s.stderrW.Close()
		s.markDone(err)
	}()
	return nil
}

// markDone is called once when the exec output stream finishes. It
// polls Inspect to capture the exit code (the inspect call may need a
// brief retry because the server can lag behind the close).
func (s *dockerSession) markDone(copyErr error) {
	// The output stream is closed; poll inspect briefly until ExitCode
	// is reported and the exec is not running.
	deadline := time.Now().Add(5 * time.Second)
	var inspectErr error
	for time.Now().Before(deadline) {
		insp, err := s.cli.ContainerExecInspect(s.bgCtx, s.execID)
		if err != nil {
			inspectErr = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if !insp.Running {
			s.exitCode = insp.ExitCode
			s.waitErr = nil
			close(s.doneCh)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if inspectErr != nil {
		s.waitErr = fmt.Errorf("docker exec inspect: %w", inspectErr)
	} else if copyErr != nil {
		s.waitErr = copyErr
	} else {
		s.waitErr = errors.New("docker exec did not report completion in time")
	}
	s.exitCode = 127
	close(s.doneCh)
}

// Wait blocks until the exec exits.
func (s *dockerSession) Wait() error {
	s.startMu.Lock()
	if !s.started {
		s.startMu.Unlock()
		return errors.New("docker session: Wait before Start")
	}
	s.startMu.Unlock()
	<-s.doneCh
	if s.waitErr != nil {
		return s.waitErr
	}
	if s.exitCode != 0 {
		return &execExitError{code: s.exitCode}
	}
	return nil
}

// Close tears down the session.
func (s *dockerSession) Close() error {
	s.startMu.Lock()
	if s.closed {
		s.startMu.Unlock()
		return nil
	}
	s.closed = true
	s.startMu.Unlock()
	if s.cancelBg != nil {
		s.cancelBg()
	}
	s.closePipes()
	if s.hijack != nil {
		_ = s.hijack.Close()
	}
	return nil
}

func (s *dockerSession) closePipes() {
	_ = s.stdinPipeR.Close()
	_ = s.stdinPipeW.Close()
	_ = s.stdoutR.Close()
	_ = s.stdoutW.Close()
	_ = s.stderrR.Close()
	_ = s.stderrW.Close()
}

// execExitError wraps a non-zero exec exit code so it satisfies
// remoteexec.ExitCoder.
type execExitError struct{ code int }

func (e *execExitError) Error() string   { return fmt.Sprintf("docker exec exit code %d", e.code) }
func (e *execExitError) ExitStatus() int { return e.code }

// Compile-time check: docker sessions satisfy remote.Session.
var _ remote.Session = (*dockerSession)(nil)
