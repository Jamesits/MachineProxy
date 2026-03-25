package broker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"sync"

	"github.com/jamesits/machineproxy/pkg/remoteexec"
)

type Deps struct {
	Remote remoteexec.Runner
}

type Server struct {
	deps   Deps
	connWG sync.WaitGroup
}

func NewServer(deps Deps) *Server {
	return &Server{deps: deps}
}

func (s *Server) Start(ctx context.Context, socketPath string) error {
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Wait for in-flight connections to finish before returning.
			s.connWG.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
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
		_ = enc.Encode(Frame{Stream: StreamExit, Code: 127, Error: err.Error()})
		return
	}

	stdinReader, stdinWriter := io.Pipe()
	defer stdinReader.Close()

	go s.readStdinFrames(dec, stdinWriter)

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	var encMu sync.Mutex
	send := func(frame Frame) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = enc.Encode(frame)
	}

	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go s.streamPipe(&streamWG, stdoutReader, StreamStdout, send)
	go s.streamPipe(&streamWG, stderrReader, StreamStderr, send)

	exitCode, runErr := s.runRemote(ctx, req, stdinReader, stdoutWriter, stderrWriter)
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	streamWG.Wait()

	exitFrame := Frame{Stream: StreamExit, Code: exitCode}
	if runErr != nil {
		exitFrame.Error = runErr.Error()
	}
	send(exitFrame)
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

func (s *Server) readStdinFrames(dec *json.Decoder, w *io.PipeWriter) {
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
