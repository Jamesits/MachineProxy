package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/logging"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Bootstrap logger writes to stderr until the mux is ready.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logging.LevelTrace,
	}))

	// Become a subreaper so orphaned grandchildren are reparented to us.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 36 /* PR_SET_CHILD_SUBREAPER */, 1, 0); errno != 0 {
		log.Warn("prctl(PR_SET_CHILD_SUBREAPER) failed", "error", errno)
		// Non-fatal: zombie reaping still works for direct children.
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go reapZombies(ctx)

	// The mux reads from our stdin and writes to our stdout.
	// Our stderr goes to the SSH session's stderr channel for diagnostics.
	mux := agentproto.NewMux(os.Stdin, os.Stdout)

	// Switch to mux-backed logger so logs are forwarded to machineproxy.
	log = slog.New(agentproto.NewMuxLogHandler(mux, logging.LevelTrace))
	slog.SetDefault(log)

	// Read the first frame through the mux's decoder — must be FrameExec.
	f, err := mux.DecodeOne()
	if err != nil {
		log.Error("reading exec frame failed", "error", err)
		return 127
	}
	if f.Type != agentproto.FrameExec || f.Exec == nil {
		log.Error("first frame must be exec", "type", f.Type)
		return 127
	}

	log.Log(ctx, logging.LevelTrace, "building command", "path", f.Exec.Path, "argv", f.Exec.Argv, "cwd", f.Exec.Cwd)

	cmd, pipes, err := buildCmd(f.Exec)
	if err != nil {
		log.Error("build command failed", "error", err)
		_ = mux.Send(&agentproto.Frame{
			Type:  agentproto.FrameExit,
			Code:  127,
			Error: err.Error(),
		})
		return 127
	}

	if err := cmd.Start(); err != nil {
		closePipes(pipes)
		log.Error("start command failed", "error", err)
		_ = mux.Send(&agentproto.Frame{
			Type:  agentproto.FrameExit,
			Code:  127,
			Error: err.Error(),
		})
		return 0 // Agent itself exits cleanly; error is in the exit frame.
	}

	log.Log(ctx, logging.LevelTrace, "child started", "pid", cmd.Process.Pid)

	// Close child-side fds so the child is the only holder.
	closeChildFDs(cmd)

	// Register mux stream handlers for writing to child's stdin and extra fds.
	mux.RegisterStream(0, agentproto.WriterHandler(pipes.stdin))
	for fdNum, f := range pipes.extra {
		mux.RegisterStream(fdNum, agentproto.WriterHandler(f))
	}

	// Forward signals to the child's process group.
	mux.OnSignal(func(sig int) {
		log.Log(ctx, logging.LevelTrace, "forwarding signal to child", "signal", sig)
		if err := sendSignalToGroup(cmd, sig); err != nil {
			log.Warn("failed to send signal to child group", "signal", sig, "error", err)
		}
	})

	// Bridge child stdout/stderr/extra fds → mux.
	var outWG sync.WaitGroup

	bridgeToMux := func(stream uint32, r *os.File) {
		outWG.Add(1)
		go func() {
			defer outWG.Done()
			buf := make([]byte, 32*1024)
			for {
				n, err := r.Read(buf)
				if n > 0 {
					chunk := make([]byte, n)
					copy(chunk, buf[:n])
					if sendErr := mux.SendData(stream, chunk); sendErr != nil {
						log.Warn("mux send data failed", "stream", stream, "error", sendErr)
					}
				}
				if err != nil {
					if sendErr := mux.SendEOF(stream); sendErr != nil {
						log.Warn("mux send eof failed", "stream", stream, "error", sendErr)
					}
					r.Close()
					return
				}
			}
		}()
	}

	bridgeToMux(1, pipes.stdout)
	bridgeToMux(2, pipes.stderr)
	for fdNum, f := range pipes.extra {
		bridgeToMux(fdNum, f)
	}

	// Run the mux read loop in a goroutine to process incoming
	// stdin data and signal frames while we wait for the child.
	muxDone := make(chan struct{})
	go func() {
		defer close(muxDone)
		mux.ReadLoop(ctx)
	}()

	// Wait for child to exit.
	waitErr := cmd.Wait()
	code := 0
	if waitErr != nil {
		if cmd.ProcessState != nil {
			code = exitCode(cmd.ProcessState)
		} else {
			code = 127
		}
	}

	log.Debug("child exited", "code", code)

	// Wait for all output streams to drain.
	outWG.Wait()

	// Send exit frame.
	exitFrame := &agentproto.Frame{Type: agentproto.FrameExit, Code: code}
	if waitErr != nil && code == 127 {
		exitFrame.Error = waitErr.Error()
	}
	if err := mux.Send(exitFrame); err != nil {
		// Can't log over mux anymore, fall back to stderr.
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Warn("failed to send exit frame", "error", err)
	}

	// Cancel context to stop mux read loop and reaper.
	cancel()
	<-muxDone

	return 0
}
