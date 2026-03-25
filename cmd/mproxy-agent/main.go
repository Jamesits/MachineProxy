package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Become a subreaper so orphaned grandchildren are reparented to us.
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, 36 /* PR_SET_CHILD_SUBREAPER */, 1, 0); errno != 0 {
		fmt.Fprintf(os.Stderr, "mproxy-agent: prctl(PR_SET_CHILD_SUBREAPER): %v\n", errno)
		// Non-fatal: zombie reaping still works for direct children.
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go reapZombies(ctx)

	// The mux reads from our stdin and writes to our stdout.
	// Our stderr goes to the SSH session's stderr channel for diagnostics.
	mux := agentproto.NewMux(os.Stdin, os.Stdout)

	// Read the first frame through the mux's decoder — must be FrameExec.
	f, err := mux.DecodeOne()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mproxy-agent: reading exec frame: %v\n", err)
		return 127
	}
	if f.Type != agentproto.FrameExec || f.Exec == nil {
		fmt.Fprintf(os.Stderr, "mproxy-agent: first frame must be exec, got type %d\n", f.Type)
		return 127
	}

	cmd, pipes, err := buildCmd(f.Exec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mproxy-agent: build command: %v\n", err)
		_ = mux.Send(&agentproto.Frame{
			Type:  agentproto.FrameExit,
			Code:  127,
			Error: err.Error(),
		})
		return 127
	}

	if err := cmd.Start(); err != nil {
		closePipes(pipes)
		fmt.Fprintf(os.Stderr, "mproxy-agent: start command: %v\n", err)
		_ = mux.Send(&agentproto.Frame{
			Type:  agentproto.FrameExit,
			Code:  127,
			Error: err.Error(),
		})
		return 0 // Agent itself exits cleanly; error is in the exit frame.
	}

	// Close child-side fds so the child is the only holder.
	closeChildFDs(cmd)

	// Register mux stream handlers for writing to child's stdin and extra fds.
	mux.RegisterStream(0, agentproto.WriterHandler(pipes.stdin))
	for fdNum, f := range pipes.extra {
		mux.RegisterStream(fdNum, agentproto.WriterHandler(f))
	}

	// Forward signals to the child's process group.
	mux.OnSignal(func(sig int) {
		_ = sendSignalToGroup(cmd, sig)
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
					_ = mux.SendData(stream, chunk)
				}
				if err != nil {
					_ = mux.SendEOF(stream)
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

	// Wait for all output streams to drain.
	outWG.Wait()

	// Send exit frame.
	exitFrame := &agentproto.Frame{Type: agentproto.FrameExit, Code: code}
	if waitErr != nil && code == 127 {
		exitFrame.Error = waitErr.Error()
	}
	_ = mux.Send(exitFrame)

	// Cancel context to stop mux read loop and reaper.
	cancel()
	<-muxDone

	return 0
}
