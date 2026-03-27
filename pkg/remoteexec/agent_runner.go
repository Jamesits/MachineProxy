package remoteexec

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
	"github.com/jamesits/machineproxy/pkg/logging"
)

// AgentRunner executes remote commands by launching the mproxy-agent
// binary on the remote host and communicating over CBOR mux.
type AgentRunner struct {
	Provider   SessionProvider
	Transferer *agenttransfer.Transferer
	Recorder   *agentproto.Recorder // nil disables recording
	Log        *slog.Logger
}

func (r *AgentRunner) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

func (r *AgentRunner) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	log := r.logger()
	if r == nil || r.Provider == nil {
		return 127, fmt.Errorf("agent runner provider is nil")
	}

	agentPath, err := r.Transferer.Ensure()
	if err != nil {
		return 127, fmt.Errorf("ensure agent binary: %w", err)
	}

	log.Debug("opening ssh session for agent")
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
	log.Debug("agent started", "agent_path", agentPath)

	// Drain agent diagnostic output (its stderr) to our stderr.
	go func() {
		_, _ = io.Copy(stderr, sessErr)
	}()

	mux := agentproto.NewMux(sessOut, sessIn)

	// Forward agent log frames to our local logger.
	mux.OnLog(func(entry *agentproto.LogEntry) {
		attrs := make([]any, 0, len(entry.Attrs)*2+2)
		attrs = append(attrs, "source", "agent")
		for _, kv := range entry.Attrs {
			attrs = append(attrs, kv)
		}
		log.Log(ctx, slog.Level(entry.Level), entry.Msg, attrs...)
	})

	// Send exec request.
	execMsg := &agentproto.ExecMsg{
		Path: req.Path,
		Argv: req.Argv,
		Env:  req.Env,
		Cwd:  req.Cwd,
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

	log.Log(ctx, logging.LevelTrace, "sending exec frame", "path", req.Path, "argv", req.Argv)
	if err := mux.Send(&agentproto.Frame{
		Type: agentproto.FrameExec,
		Exec: execMsg,
	}); err != nil {
		return 127, fmt.Errorf("send exec frame: %w", err)
	}

	// Register handlers for stdout and stderr from the agent.
	mux.RegisterStream(1, agentproto.WriterHandler(nopWriteCloser{stdout}))
	mux.RegisterStream(2, agentproto.WriterHandler(nopWriteCloser{stderr}))

	// Bridge local stdin → agent stream 0.
	var stdinWG sync.WaitGroup
	stdinWG.Add(1)
	go func() {
		defer stdinWG.Done()
		bridgeReaderToMux(mux, 0, stdin)
	}()

	// Run the mux read loop. It returns when the agent sends FrameExit.
	exitCode, muxErr := mux.ReadLoop(ctx)
	log.Log(ctx, logging.LevelTrace, "mux read loop finished", "exit_code", exitCode, "error", muxErr)

	// Close the session stdin to signal the agent that we're done.
	_ = sessIn.Close()
	// Unblock the stdin bridge goroutine: close the pipe reader so
	// bridgeReaderToMux's Read returns instead of blocking forever.
	if c, ok := stdin.(io.Closer); ok {
		_ = c.Close()
	}
	stdinWG.Wait()

	// Wait for the SSH session to finish.
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

	agentPath, err := r.Transferer.Ensure()
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
			attrs = append(attrs, kv)
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

	log.Log(ctx, logging.LevelTrace, "sending exec frame (with control)", "path", req.Path, "argv", req.Argv, "extra_fds", req.ExtraFDs)
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

// Verify interface compliance.
var (
	_ Runner          = (*AgentRunner)(nil)
	_ SignalableRunner = (*AgentRunner)(nil)
)
