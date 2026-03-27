package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
)

type Deps struct {
	Remote    remoteexec.Runner
	EnvFilter func(env []string) []string // nil means pass through unfiltered
	Log       *slog.Logger
}

type Server struct {
	deps   Deps
	log    *slog.Logger
	connWG sync.WaitGroup
}

func NewServer(deps Deps) *Server {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{deps: deps, log: log}
}

func (s *Server) Start(ctx context.Context, socketPath string) error {
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	s.log.Debug("broker listening", "socket", socketPath)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.connWG.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.log.Log(ctx, logging.LevelTrace, "broker accepted connection")
		s.connWG.Add(1)
		go func() {
			defer s.connWG.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	var req ExecRequest
	if err := dec.Decode(&req); err != nil {
		s.log.Warn("broker decode exec request failed", "error", err)
		_ = enc.Encode(Frame{Stream: StreamExit, Code: 127, Error: err.Error()})
		return
	}
	s.log.Log(ctx, logging.LevelTrace, "broker exec request", "path", req.Path, "argv", req.Argv, "cwd", req.Cwd)

	if s.deps.EnvFilter != nil {
		req.Env = s.deps.EnvFilter(req.Env)
	}

	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()

	var encMu sync.Mutex
	send := func(frame Frame) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = enc.Encode(frame)
	}

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go s.streamPipe(&streamWG, stdoutReader, StreamStdout, send)
	go s.streamPipe(&streamWG, stderrReader, StreamStderr, send)

	// Check if the runner supports signals and extra fds.
	sigRunner, hasSigRunner := s.deps.Remote.(remoteexec.SignalableRunner)

	if hasSigRunner {
		sigCh := make(chan int, 8)

		// Set up extra fd pipes.
		extraFDPipes := make(map[uint32]io.ReadWriteCloser)
		for _, fdNum := range req.ExtraFDs {
			localR, localW := io.Pipe()
			remoteR, remoteW := io.Pipe()

			// Bridge remote → shim.
			streamWG.Add(1)
			fdStream := fmt.Sprintf("%s%d", StreamFDPrefix, fdNum)
			go s.streamPipe(&streamWG, localR, fdStream, send)

			extraFDPipes[fdNum] = &fdPipePair{
				Reader:     remoteR,
				Writer:     localW,
				remoteW:    remoteW,
				closeOnce:  sync.Once{},
			}

			// We store remoteW so readFrames can write to it.
			_ = remoteW // kept alive via fdPipePair
		}

		// Read frames from shim: dispatches stdin, signals, and fd data.
		go s.readFrames(dec, stdinWriter, sigCh, extraFDPipes)

		ctrl := &remoteexec.Control{
			Stdin:    stdinReader,
			Stdout:   stdoutWriter,
			Stderr:   stderrWriter,
			Signals:  sigCh,
			ExtraFDs: extraFDPipes,
		}

		s.log.Debug("broker running remote command (with control)", "path", req.Path)
		exitCode, runErr := sigRunner.RunWithControl(ctx, req, ctrl)
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		for _, rwc := range extraFDPipes {
			rwc.Close()
		}
		streamWG.Wait()

		if runErr != nil {
			s.log.Warn("broker remote command failed", "path", req.Path, "exit_code", exitCode, "error", runErr)
		} else {
			s.log.Log(ctx, logging.LevelTrace, "broker remote command finished", "path", req.Path, "exit_code", exitCode)
		}

		exitFrame := Frame{Stream: StreamExit, Code: exitCode}
		if runErr != nil {
			exitFrame.Error = runErr.Error()
		}
		send(exitFrame)
	} else {
		// Legacy path: no signal/fd support.
		go s.readFramesBasic(dec, stdinWriter)

		s.log.Debug("broker running remote command (legacy)", "path", req.Path)
		exitCode, runErr := s.runRemote(ctx, req, stdinReader, stdoutWriter, stderrWriter)
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		streamWG.Wait()

		if runErr != nil {
			s.log.Warn("broker remote command failed", "path", req.Path, "exit_code", exitCode, "error", runErr)
		}

		exitFrame := Frame{Stream: StreamExit, Code: exitCode}
		if runErr != nil {
			exitFrame.Error = runErr.Error()
		}
		send(exitFrame)
	}
}

func (s *Server) runRemote(ctx context.Context, req ExecRequest, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	if s.deps.Remote == nil {
		return 127, io.ErrClosedPipe
	}
	code, err := s.deps.Remote.Run(ctx, req, stdin, stdout, stderr)
	if err != nil && code == 0 {
		return 127, err
	}
	return code, err
}

// readFramesBasic is the legacy frame reader that only handles stdin.
func (s *Server) readFramesBasic(dec *json.Decoder, w *io.PipeWriter) {
	defer w.Close()
	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			return
		}
		if frame.Stream != StreamStdin {
			continue
		}
		if len(frame.Data) == 0 {
			continue
		}
		if _, err := w.Write(frame.Data); err != nil {
			return
		}
	}
}

// readFrames dispatches incoming shim frames to stdin, signals, and extra fd pipes.
func (s *Server) readFrames(dec *json.Decoder, stdinW *io.PipeWriter, sigCh chan<- int, extraFDs map[uint32]io.ReadWriteCloser) {
	defer stdinW.Close()
	defer close(sigCh)

	for {
		var frame Frame
		if err := dec.Decode(&frame); err != nil {
			return
		}

		switch {
		case frame.Stream == StreamStdin:
			if len(frame.Data) > 0 {
				if _, err := stdinW.Write(frame.Data); err != nil {
					return
				}
			}

		case frame.Stream == StreamSignal:
			sigCh <- frame.Signal

		case strings.HasPrefix(frame.Stream, StreamFDPrefix):
			fdNumStr := frame.Stream[len(StreamFDPrefix):]
			var fdNum uint32
			if _, err := fmt.Sscanf(fdNumStr, "%d", &fdNum); err != nil {
				continue
			}
			if p, ok := extraFDs[fdNum]; ok && len(frame.Data) > 0 {
				if pp, ok := p.(*fdPipePair); ok {
					_, _ = pp.remoteW.Write(frame.Data)
				}
			}
		}
	}
}

func (s *Server) streamPipe(wg *sync.WaitGroup, r *io.PipeReader, stream string, send func(Frame)) {
	defer wg.Done()
	defer r.Close()

	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			send(Frame{Stream: stream, Data: chunk})
		}
		if err != nil {
			return
		}
	}
}

// fdPipePair implements io.ReadWriteCloser for bidirectional fd bridging.
// Read returns data coming from the remote (agent → broker).
// Write sends data to the local shim (broker → shim, via streamPipe).
type fdPipePair struct {
	io.Reader              // remote read end (data from agent)
	io.Writer              // local write end (data to streamPipe → shim)
	remoteW   *io.PipeWriter // write end for data from shim → agent
	closeOnce sync.Once
}

func (p *fdPipePair) Close() error {
	p.closeOnce.Do(func() {
		if r, ok := p.Reader.(*io.PipeReader); ok {
			r.Close()
		}
		if w, ok := p.Writer.(*io.PipeWriter); ok {
			w.Close()
		}
		p.remoteW.Close()
	})
	return nil
}
