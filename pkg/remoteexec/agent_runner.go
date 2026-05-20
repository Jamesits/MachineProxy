package remoteexec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
	"github.com/jamesits/machineproxy/pkg/logging"
)

// AgentRunner executes remote commands by launching the mproxy-agent
// binary on the remote host and communicating over CBOR mux.
type AgentRunner struct {
	Provider    SessionProvider
	Transferer  *agenttransfer.Transferer
	Recorder    *agentproto.Recorder    // nil disables recording
	AgentConfig *agentproto.AgentConfig // sent to agent before exec; nil skips
	Log         *slog.Logger
}

func (r *AgentRunner) logger() *slog.Logger {
	if r != nil && r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *AgentRunner) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	// The basic runner is the controlled runner without signals or extra fds;
	// delegating keeps SSH/session lifecycle and recording behavior in one path.
	return r.RunWithControl(ctx, req, &Control{
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		ExtraFDs: map[uint32]io.ReadWriteCloser{},
	})
}

// bridgeReaderToMux reads from r and sends FrameData to the mux.
func bridgeReaderToMux(mux *agentproto.Mux, stream uint32, r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := mux.SendData(stream, chunk); sendErr != nil {
				return
			}
		}
		if err != nil {
			_ = mux.SendEOF(stream)
			return
		}
	}
}

// nopWriteCloser wraps an io.Writer to satisfy io.WriteCloser.
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// RunWithControl extends Run with signal forwarding and extra fd bridging.
func (r *AgentRunner) RunWithControl(ctx context.Context, req Request, ctrl *Control) (int, error) {
	log := r.logger()
	if r == nil || r.Provider == nil {
		return 127, fmt.Errorf("agent runner provider is nil")
	}
	if r.Transferer == nil {
		return 127, fmt.Errorf("agent runner transferer is nil")
	}

	agentPath, err := r.Transferer.Ensure(ctx)
	if err != nil {
		return 127, fmt.Errorf("ensure agent binary: %w", err)
	}

	log.Debug("opening ssh session for agent (with control)")
	session, err := r.Provider.NewSession(ctx)
	if err != nil {
		return 127, err
	}
	defer session.Close()

	sessIn, err := session.StdinPipe()
	if err != nil {
		return 127, err
	}
	sessOut, err := session.StdoutPipe()
	if err != nil {
		return 127, err
	}
	sessErr, err := session.StderrPipe()
	if err != nil {
		return 127, err
	}

	if err := session.Start(shellQuote(agentPath)); err != nil {
		return 127, fmt.Errorf("start agent: %w", err)
	}
	log.Debug("agent started (with control)", "agent_path", agentPath)

	// Drain agent diagnostics.
	go func() {
		_, _ = io.Copy(ctrl.Stderr, sessErr)
	}()

	mux := agentproto.NewMux(sessOut, sessIn)

	// Forward agent log frames to our local logger.
	mux.OnLog(func(entry *agentproto.LogEntry) {
		attrs := make([]any, 0, len(entry.Attrs)*2+2)
		attrs = append(attrs, "source", "agent")
		for _, kv := range entry.Attrs {
			if k, v, ok := strings.Cut(kv, "="); ok {
				attrs = append(attrs, k, v)
			} else {
				attrs = append(attrs, kv, "")
			}
		}
		log.Log(ctx, slog.Level(entry.Level), entry.Msg, attrs...)
	})

	// Build exec message with extra fds.
	execMsg := &agentproto.ExecMsg{
		Path:     req.Path,
		Argv:     req.Argv,
		Env:      req.Env,
		Cwd:      req.Cwd,
		ExtraFDs: req.ExtraFDs,
	}

	// Enable recording if configured.
	var recSeq uint32
	if r.Recorder != nil {
		var recErr error
		recSeq, recErr = r.Recorder.WriteCommandStart(execMsg)
		if recErr == nil {
			mux.SetRecorder(r.Recorder, recSeq)
		} else {
			log.Warn("failed to write command start record", "error", recErr)
		}
	}

	// Send agent config before exec so the agent can apply its own filtering.
	if r.AgentConfig != nil {
		if err := mux.Send(&agentproto.Frame{
			Type:   agentproto.FrameConfig,
			Config: r.AgentConfig,
		}); err != nil {
			return 127, fmt.Errorf("send config frame: %w", err)
		}
	}

	log.Log(ctx, logging.LevelTrace, "sending exec frame (with control)", "path", req.Path, "argv", logging.JSONValue(req.Argv), "extra_fds", logging.JSONValue(req.ExtraFDs))
	if err := mux.Send(&agentproto.Frame{
		Type: agentproto.FrameExec,
		Exec: execMsg,
	}); err != nil {
		return 127, fmt.Errorf("send exec frame: %w", err)
	}

	// Register stdout/stderr handlers.
	mux.RegisterStream(1, agentproto.WriterHandler(nopWriteCloser{ctrl.Stdout}))
	mux.RegisterStream(2, agentproto.WriterHandler(nopWriteCloser{ctrl.Stderr}))

	// Register extra fd handlers (agent → local direction).
	for fdNum, rwc := range ctrl.ExtraFDs {
		mux.RegisterStream(fdNum, agentproto.WriterHandler(rwc))
	}

	// Bridge local stdin → agent.
	var bridgeWG sync.WaitGroup
	bridgeWG.Add(1)
	go func() {
		defer bridgeWG.Done()
		bridgeReaderToMux(mux, 0, ctrl.Stdin)
	}()

	// Bridge extra fds (local → agent direction).
	for fdNum, rwc := range ctrl.ExtraFDs {
		fdNum, rwc := fdNum, rwc
		bridgeWG.Add(1)
		go func() {
			defer bridgeWG.Done()
			bridgeReaderToMux(mux, fdNum, rwc)
		}()
	}

	// Forward signals to the agent.
	if ctrl.Signals != nil {
		go func() {
			for sig := range ctrl.Signals {
				log.Log(ctx, logging.LevelTrace, "forwarding signal to agent", "signal", sig)
				_ = mux.Send(&agentproto.Frame{
					Type:   agentproto.FrameSignal,
					Signal: sig,
				})
			}
		}()
	}

	exitCode, muxErr := mux.ReadLoop(ctx)
	log.Log(ctx, logging.LevelTrace, "mux read loop finished (with control)", "exit_code", exitCode, "error", muxErr)

	_ = sessIn.Close()
	if c, ok := ctrl.Stdin.(io.Closer); ok {
		_ = c.Close()
	}
	for _, rwc := range ctrl.ExtraFDs {
		_ = rwc.Close()
	}
	bridgeWG.Wait()
	if err := session.Wait(); err != nil {
		log.Warn("ssh session wait error", "error", err)
	}

	if r.Recorder != nil {
		if err := r.Recorder.WriteCommandEnd(recSeq, exitCode); err != nil {
			log.Warn("failed to write command end record", "error", err)
		}
	}

	if muxErr != nil && exitCode < 0 {
		return 127, muxErr
	}
	return exitCode, nil
}

// EnumeratePaths asks the remote agent to enumerate executables
// reachable through the given PATH-style directories. An empty paths
// slice tells the agent to use its own $PATH. The session is short-
// lived: the agent sends one FramePathInfo and exits.
func (r *AgentRunner) EnumeratePaths(ctx context.Context, paths []string) ([]agentproto.PathInfoEntry, error) {
	log := r.logger()
	if r == nil || r.Provider == nil {
		return nil, fmt.Errorf("agent runner provider is nil")
	}
	if r.Transferer == nil {
		return nil, fmt.Errorf("agent runner transferer is nil")
	}

	agentPath, err := r.Transferer.Ensure(ctx)
	if err != nil {
		return nil, fmt.Errorf("ensure agent binary: %w", err)
	}

	log.Debug("opening ssh session for path enumeration")
	session, err := r.Provider.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	sessIn, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	sessOut, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	sessErr, err := session.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := session.Start(shellQuote(agentPath)); err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}

	// Drain stderr so the agent process doesn't block on a full pipe.
	// It's only used for early bootstrap diagnostics; once the mux is up
	// log frames flow through stdout instead.
	go func() { _, _ = io.Copy(io.Discard, sessErr) }()

	mux := agentproto.NewMux(sessOut, sessIn)
	mux.OnLog(func(entry *agentproto.LogEntry) {
		attrs := make([]any, 0, len(entry.Attrs)*2+2)
		attrs = append(attrs, "source", "agent")
		for _, kv := range entry.Attrs {
			if k, v, ok := strings.Cut(kv, "="); ok {
				attrs = append(attrs, k, v)
			} else {
				attrs = append(attrs, kv, "")
			}
		}
		log.Log(ctx, slog.Level(entry.Level), entry.Msg, attrs...)
	})

	if r.AgentConfig != nil {
		if err := mux.Send(&agentproto.Frame{
			Type:   agentproto.FrameConfig,
			Config: r.AgentConfig,
		}); err != nil {
			return nil, fmt.Errorf("send config frame: %w", err)
		}
	}

	log.Log(ctx, logging.LevelTrace, "sending path-query frame", "paths", logging.JSONValue(paths))
	if err := mux.Send(&agentproto.Frame{
		Type:  agentproto.FramePathQuery,
		Query: &agentproto.PathQuery{Paths: paths},
	}); err != nil {
		return nil, fmt.Errorf("send path query: %w", err)
	}

	// Loop until we see the response, draining log frames in between.
	var entries []agentproto.PathInfoEntry
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f, decErr := mux.DecodeOne()
		if decErr != nil {
			return nil, fmt.Errorf("read path info: %w", decErr)
		}
		switch f.Type {
		case agentproto.FramePathInfo:
			if f.Info != nil {
				entries = f.Info.Entries
			}
			// Close our side; the agent will exit after sending.
			_ = sessIn.Close()
			_ = session.Wait()
			log.Log(ctx, logging.LevelTrace, "received path info", "count", len(entries))
			return entries, nil
		case agentproto.FrameLog:
			if f.Log != nil {
				attrs := make([]any, 0, len(f.Log.Attrs)*2+2)
				attrs = append(attrs, "source", "agent")
				for _, kv := range f.Log.Attrs {
					if k, v, ok := strings.Cut(kv, "="); ok {
						attrs = append(attrs, k, v)
					} else {
						attrs = append(attrs, kv, "")
					}
				}
				log.Log(ctx, slog.Level(f.Log.Level), f.Log.Msg, attrs...)
			}
		case agentproto.FrameError:
			return nil, fmt.Errorf("agent error: %s", f.Error)
		default:
			log.Warn("unexpected frame during enumeration", "type", f.Type)
		}
	}
}

// Verify interface compliance.
var (
	_ Runner           = (*AgentRunner)(nil)
	_ SignalableRunner = (*AgentRunner)(nil)
)
