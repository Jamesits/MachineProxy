package remoteexec

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/agenttransfer"
)

// AgentRunner executes remote commands by launching the mproxy-agent
// binary on the remote host and communicating over CBOR mux.
type AgentRunner struct {
	Provider   SessionProvider
	Transferer *agenttransfer.Transferer
	Recorder   *agentproto.Recorder // nil disables recording
}

func (r *AgentRunner) Run(ctx context.Context, req Request, stdin io.Reader, stdout io.Writer, stderr io.Writer) (int, error) {
	if r == nil || r.Provider == nil {
		return 127, fmt.Errorf("agent runner provider is nil")
	}

	agentPath, err := r.Transferer.Ensure()
	if err != nil {
		return 127, fmt.Errorf("ensure agent binary: %w", err)
	}

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

	// Drain agent diagnostic output (its stderr) to our stderr.
	go func() {
		_, _ = io.Copy(stderr, sessErr)
	}()

	mux := agentproto.NewMux(sessOut, sessIn)

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
		}
	}

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

	// Close the session stdin to signal the agent that we're done.
	_ = sessIn.Close()
	// Unblock the stdin bridge goroutine: close the pipe reader so
	// bridgeReaderToMux's Read returns instead of blocking forever.
	if c, ok := stdin.(io.Closer); ok {
		_ = c.Close()
	}
	stdinWG.Wait()

	// Wait for the SSH session to finish.
	_ = session.Wait()

	if r.Recorder != nil {
		_ = r.Recorder.WriteCommandEnd(recSeq, exitCode)
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
	if r == nil || r.Provider == nil {
		return 127, fmt.Errorf("agent runner provider is nil")
	}

	agentPath, err := r.Transferer.Ensure()
	if err != nil {
		return 127, fmt.Errorf("ensure agent binary: %w", err)
	}

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

	// Drain agent diagnostics.
	go func() {
		_, _ = io.Copy(ctrl.Stderr, sessErr)
	}()

	mux := agentproto.NewMux(sessOut, sessIn)

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
		}
	}

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
				_ = mux.Send(&agentproto.Frame{
					Type:   agentproto.FrameSignal,
					Signal: sig,
				})
			}
		}()
	}

	exitCode, muxErr := mux.ReadLoop(ctx)

	_ = sessIn.Close()
	// Unblock bridge goroutines: close stdin and extra FD readers so
	// bridgeReaderToMux's Read returns instead of blocking forever.
	if c, ok := ctrl.Stdin.(io.Closer); ok {
		_ = c.Close()
	}
	for _, rwc := range ctrl.ExtraFDs {
		_ = rwc.Close()
	}
	bridgeWG.Wait()
	_ = session.Wait()

	if r.Recorder != nil {
		_ = r.Recorder.WriteCommandEnd(recSeq, exitCode)
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
