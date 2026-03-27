package tracer

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/logging"
	"golang.org/x/sys/unix"
)

// Config holds the tracer configuration.
type Config struct {
	ShimPath   string       // Absolute path to mproxy-shim.
	Whitelist  []string     // Absolute paths that should execute locally.
	BrokerSock string       // Path to broker Unix socket.
	Log        *slog.Logger // Optional logger; defaults to slog.Default.
}

// pidState tracks per-process tracing state.
type pidState struct {
	inSyscall   bool // true = next syscall-stop is exit, false = entry
	expectStop  bool // true = expecting initial SIGSTOP from ptrace auto-attach
}

// Tracer manages ptrace-based exec interception for all descendants of a
// traced process, redirecting non-whitelisted exec calls through mproxy-shim.
type Tracer struct {
	cfg      Config
	log      *slog.Logger
	baseline *EnvBaseline
	pids     map[int]*pidState
}

// New creates a tracer with the given configuration.
func New(cfg Config) *Tracer {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &Tracer{
		cfg:  cfg,
		log:  log,
		pids: make(map[int]*pidState),
	}
}

// Start forks the target command under ptrace, then runs the trace loop
// intercepting execve/execveat calls. Returns the exit code.
// The optional onStart callback is invoked with the child pid after ptrace
// is configured but before the trace loop begins (useful for signal forwarding).
func (t *Tracer) Start(argv []string, env []string, onStart func(childPid int)) (int, error) {
	runtime.LockOSThread() // ptrace is per-thread; never unlock

	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return 127, fmt.Errorf("%s: %w", argv[0], err)
	}

	// Ignore job-control signals so the tracer doesn't get stopped when
	// the traced shell manipulates foreground process groups.
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)

	// Fork+exec the target directly with PTRACE_TRACEME. The child
	// inherits our process group (the terminal foreground group), so
	// interactive shells like bash can manage job control normally.
	pid, err := syscall.ForkExec(binary, argv, &syscall.ProcAttr{
		Env:   env,
		Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()},
		Sys: &syscall.SysProcAttr{
			Ptrace: true,
		},
	})
	if err != nil {
		return 127, fmt.Errorf("forkexec: %w", err)
	}

	// Wait for the initial SIGTRAP from exec.
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		return 127, fmt.Errorf("wait4 initial stop: %w", err)
	}

	// Set ptrace options: trace descendants, mark syscall stops with bit 7.
	opts := unix.PTRACE_O_TRACESYSGOOD |
		unix.PTRACE_O_TRACEFORK |
		unix.PTRACE_O_TRACEVFORK |
		unix.PTRACE_O_TRACECLONE |
		unix.PTRACE_O_TRACEEXEC |
		unix.PTRACE_O_EXITKILL
	if err := unix.PtraceSetOptions(pid, opts); err != nil {
		return 127, fmt.Errorf("ptrace setoptions: %w", err)
	}

	t.baseline = NewEnvBaselineFromSlice(env)
	t.pids[pid] = &pidState{}

	t.log.Debug("tracer started", "child_pid", pid)

	if onStart != nil {
		onStart(pid)
	}

	// Continue with PTRACE_SYSCALL so we stop on every syscall boundary.
	if err := unix.PtraceSyscall(pid, 0); err != nil {
		return 127, fmt.Errorf("ptrace syscall: %w", err)
	}

	code := t.traceLoop(pid)
	t.log.Debug("tracer finished", "exit_code", code)
	return code, nil
}

// traceLoop processes ptrace events until all traced pids exit.
func (t *Tracer) traceLoop(childPid int) int {
	exitCode := 127

	for len(t.pids) > 0 {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WALL, nil)
		if err != nil {
			break
		}

		if ws.Exited() {
			if pid == childPid {
				exitCode = ws.ExitStatus()
			}
			delete(t.pids, pid)
			continue
		}
		if ws.Signaled() {
			if pid == childPid {
				exitCode = 128 + int(ws.Signal())
			}
			delete(t.pids, pid)
			continue
		}
		if !ws.Stopped() {
			continue
		}

		sig := ws.StopSignal()
		event := uint32(ws >> 16)

		switch {
		// Syscall-stop: SIGTRAP | 0x80 (due to PTRACE_O_TRACESYSGOOD).
		case sig == syscall.SIGTRAP|0x80:
			t.handleSyscallStop(pid)
			_ = unix.PtraceSyscall(pid, 0)

		case event == unix.PTRACE_EVENT_FORK,
			event == unix.PTRACE_EVENT_VFORK,
			event == unix.PTRACE_EVENT_CLONE:
			if newPid, err := unix.PtraceGetEventMsg(pid); err == nil {
				np := int(newPid)
				t.log.Log(nil, logging.LevelTrace, "new child process", "parent_pid", pid, "child_pid", np, "event", event)
				if _, exists := t.pids[np]; !exists {
					t.pids[np] = &pidState{expectStop: true}
				}
			}
			_ = unix.PtraceSyscall(pid, 0)

		case event == unix.PTRACE_EVENT_EXEC:
			// Exec completed; this replaces the syscall-exit stop.
			if ps := t.pids[pid]; ps != nil {
				ps.inSyscall = false
			}
			_ = unix.PtraceSyscall(pid, 0)

		case sig == syscall.SIGTRAP:
			_ = unix.PtraceSyscall(pid, 0)

		default:
			// Suppress the initial SIGSTOP that the kernel sends to
			// newly auto-traced children; delivering it would actually
			// stop the process (visible as "[1] Stopped" in the shell).
			// The SIGSTOP may arrive before the parent's FORK/CLONE
			// event (pid not yet in our map), or after (expectStop set).
			if sig == syscall.SIGSTOP {
				ps := t.pids[pid]
				if ps == nil {
					// Child's stop arrived before parent's FORK event.
					t.pids[pid] = &pidState{}
					_ = unix.PtraceSyscall(pid, 0)
					break
				}
				if ps.expectStop {
					ps.expectStop = false
					_ = unix.PtraceSyscall(pid, 0)
					break
				}
			}
			// Real signal — deliver it.
			_ = unix.PtraceSyscall(pid, int(sig))
		}
	}

	return exitCode
}

// handleSyscallStop distinguishes entry from exit stops and intercepts
// execve/execveat on entry.
func (t *Tracer) handleSyscallStop(pid int) {
	ps := t.pids[pid]
	if ps == nil {
		// Unknown pid stopped in a syscall. It's a newly auto-traced
		// child whose CLONE event hasn't arrived yet. Its initial
		// SIGSTOP is still pending and must be suppressed when it comes.
		t.pids[pid] = &pidState{inSyscall: true, expectStop: true}
		return
	}

	if ps.inSyscall {
		// Syscall-exit: nothing to do.
		ps.inSyscall = false
		return
	}

	// Syscall-entry.
	ps.inSyscall = true

	regs, err := GetRegs(pid)
	if err != nil {
		return
	}

	sysno := regs.SyscallNum()
	if sysno != uint64(SysExecve()) && sysno != uint64(SysExecveat()) {
		return
	}
	isExecveat := sysno == uint64(SysExecveat())

	pathname, err := ReadString(pid, regs.PathAddr(isExecveat))
	if err != nil || t.shouldAllow(pathname) {
		if err == nil {
			t.log.Log(nil, logging.LevelTrace, "exec allowed (whitelisted)", "pid", pid, "path", pathname)
		}
		return
	}

	t.log.Log(nil, logging.LevelTrace, "exec intercepted, rewriting to shim", "pid", pid, "path", pathname)

	argv, err := ReadStringArray(pid, regs.ArgvAddr(isExecveat))
	if err != nil {
		return
	}
	envp, err := ReadStringArray(pid, regs.EnvpAddr(isExecveat))
	if err != nil {
		return
	}

	// Rewritten argv: [shim, original_path, original_args...]
	newArgv := make([]string, 0, len(argv)+2)
	newArgv = append(newArgv, t.cfg.ShimPath)
	newArgv = append(newArgv, pathname)
	newArgv = append(newArgv, argv...)

	// Inject MPROXY_CHANGED_ENVS into envp.
	newEnvp := t.baseline.InjectEnvVars(envp)

	// Write rewritten data into the tracee's stack below SP. The kernel
	// reads execve pointers from userspace before tearing down the old
	// address space, so this region is safe to use as scratch.
	totalSize := estimateWriteSize(t.cfg.ShimPath, newArgv, newEnvp)
	sp := regs.StackPointer()
	// Place data below SP minus 128-byte red zone (amd64 ABI), aligned.
	cursor := (sp - 256 - uintptr(totalSize)) &^ (uintptr(ptrSize) - 1)

	shimPathBytes := append([]byte(t.cfg.ShimPath), 0)
	if err := WriteBytes(pid, cursor, shimPathBytes); err != nil {
		return
	}
	newPathAddr := cursor
	cursor += uintptr(len(shimPathBytes))
	cursor = alignUp(cursor)

	newArgvAddr := cursor
	n, err := WriteStringArray(pid, cursor, newArgv)
	if err != nil {
		return
	}
	cursor = alignUp(cursor + uintptr(n))

	newEnvpAddr := cursor
	if _, err := WriteStringArray(pid, cursor, newEnvp); err != nil {
		return
	}

	regs.SetPathAddr(isExecveat, newPathAddr)
	regs.SetArgvAddr(isExecveat, newArgvAddr)
	regs.SetEnvpAddr(isExecveat, newEnvpAddr)
	_ = regs.Set(pid)
}

func estimateWriteSize(shimPath string, argv, envp []string) int {
	size := len(shimPath) + 1 + ptrSize
	size += (len(argv) + 1) * ptrSize
	for _, s := range argv {
		size += len(s) + 1
	}
	size += ptrSize
	size += (len(envp) + 1) * ptrSize
	for _, s := range envp {
		size += len(s) + 1
	}
	return size
}

func alignUp(addr uintptr) uintptr {
	return (addr + uintptr(ptrSize) - 1) &^ (uintptr(ptrSize) - 1)
}

func (t *Tracer) shouldAllow(pathname string) bool {
	if pathname == "" || pathname == t.cfg.ShimPath {
		return true
	}
	for _, allowed := range t.cfg.Whitelist {
		if pathname == allowed {
			return true
		}
	}
	return false
}

// WhitelistContains checks if a path is in a colon-separated whitelist string.
func WhitelistContains(whitelist, path string) bool {
	for _, entry := range strings.Split(whitelist, ":") {
		if entry == path {
			return true
		}
	}
	return false
}
