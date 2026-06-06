//go:build linux

package tracer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/logging"
	"golang.org/x/sys/unix"
)

// Config holds the tracer configuration.
type Config struct {
	ShimPath   string       // Absolute path to mproxy-shim.
	Whitelist  []string     // Absolute paths that should execute locally.
	BrokerSock string       // Path to broker Unix socket.
	Log        *slog.Logger // Optional logger; defaults to slog.Default.

	// CwdFrom/CwdTo, when both non-empty, enable getcwd(2) remapping: a
	// getcwd result under CwdFrom has that prefix rewritten to CwdTo (the
	// sub-path is preserved). See container.cwd_mode.
	CwdFrom string
	CwdTo   string
}

// pidState tracks per-process tracing state.
type pidState struct {
	inSyscall  bool // true = next syscall-stop is exit, false = entry
	expectStop bool // true = expecting initial SIGSTOP from ptrace auto-attach

	// getcwd interception: set on a getcwd syscall-entry, consumed on exit.
	cwdPending bool
	cwdBuf     uintptr // user buffer pointer (arg0)
	cwdSize    uint64  // user buffer size (arg1)
}

// Tracer manages ptrace-based exec interception for all descendants of a
// traced process, redirecting non-whitelisted exec calls through mproxy-shim.
type Tracer struct {
	cfg      Config
	log      *slog.Logger
	ctx      context.Context
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
func (t *Tracer) Start(ctx context.Context, argv []string, env []string, onStart func(childPid int)) (int, error) {
	t.ctx = ctx
	runtime.LockOSThread() // ptrace is per-thread; never unlock

	binary, err := exec.LookPath(argv[0])
	if err != nil {
		return 127, fmt.Errorf("%s: %w", argv[0], err)
	}

	// Ignore job-control signals so the tracer doesn't get stopped when
	// the traced shell manipulates foreground process groups.
	signal.Ignore(syscall.SIGTTOU, syscall.SIGTTIN)

	// Remap PWD for the initial process so the logical working directory it
	// (and the tree it spawns) inherits matches the remapped getcwd view.
	if t.cwdMapEnabled() {
		env, _ = remapPWDEnv(env, t.cfg.CwdFrom, t.cfg.CwdTo)
	}

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
				t.log.Log(t.ctx, logging.LevelTrace, "new child process", "parent_pid", pid, "child_pid", np, "event", event)
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

	// getcwd(2) remapping keys off the syscall number and a pending flag
	// rather than the entry/exit toggle below. A traced execve perturbs that
	// toggle (the kernel delivers a lingering execve syscall-exit stop after
	// PTRACE_EVENT_EXEC), but getcwd's own entry and exit stops always arrive
	// as a consecutive pair: the first carries the buffer, the second the
	// result. Keying off the syscall number is immune to the skew.
	if t.cwdMapEnabled() {
		if regs, err := GetRegs(pid); err == nil && regs.SyscallNum() == uint64(SysGetcwd()) {
			if !ps.cwdPending {
				// Entry: capture buf/size. getcwd(buf, size) places arg0/arg1
				// in the same registers as execve's path/argv pointers.
				ps.cwdBuf = regs.PathAddr(false)
				ps.cwdSize = uint64(regs.ArgvAddr(false))
				ps.cwdPending = true
			} else {
				// Exit: rewrite the result the kernel just wrote.
				ps.cwdPending = false
				t.rewriteGetcwd(pid, ps, regs)
			}
			ps.inSyscall = !ps.inSyscall
			return
		}
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
	if err != nil {
		t.blockExec(pid, regs, "read exec path", err)
		return
	}
	if isExecveat && pathname == "" {
		// FD-based execveat/fexecve cannot be represented as a remote path yet.
		// Let it proceed locally, but warn because this escapes remote execution.
		t.log.Warn("leaked execveat call", "pid", pid, "reason", "empty path")
		return
	}
	if t.shouldAllow(pathname) {
		t.log.Log(t.ctx, logging.LevelTrace, "exec allowed (whitelisted)", "pid", pid, "path", pathname)
		if t.cwdMapEnabled() {
			t.remapAllowedExecPWD(pid, regs, isExecveat)
		}
		return
	}

	t.log.Log(t.ctx, logging.LevelTrace, "exec intercepted, rewriting to shim", "pid", pid, "path", pathname)

	argv, err := ReadStringArray(pid, regs.ArgvAddr(isExecveat))
	if err != nil {
		t.blockExec(pid, regs, "read argv", err)
		return
	}
	envp, err := ReadStringArray(pid, regs.EnvpAddr(isExecveat))
	if err != nil {
		t.blockExec(pid, regs, "read envp", err)
		return
	}

	// Rewritten argv: [shim, original_path, original_args...]
	newArgv := make([]string, 0, len(argv)+2)
	newArgv = append(newArgv, t.cfg.ShimPath)
	newArgv = append(newArgv, pathname)
	newArgv = append(newArgv, argv...)

	// Inject MPROXY_CHANGED_ENVS into envp.
	newEnvp := t.baseline.InjectEnvVars(envp)

	// Remap PWD so the remote command sees the mapped working directory.
	if t.cwdMapEnabled() {
		newEnvp, _ = remapPWDEnv(newEnvp, t.cfg.CwdFrom, t.cfg.CwdTo)
	}

	// Write rewritten data into the tracee's stack below SP. The kernel
	// reads execve pointers from userspace before tearing down the old
	// address space, so this region is safe to use as scratch.
	totalSize := estimateWriteSize(t.cfg.ShimPath, newArgv, newEnvp)
	sp := regs.StackPointer()
	// Place data below SP minus 128-byte red zone (amd64 ABI), aligned.
	cursor := (sp - 256 - uintptr(totalSize)) &^ (uintptr(ptrSize) - 1)

	shimPathBytes := append([]byte(t.cfg.ShimPath), 0)
	if err := WriteBytes(pid, cursor, shimPathBytes); err != nil {
		t.blockExec(pid, regs, "write shim path", err)
		return
	}
	newPathAddr := cursor
	cursor += uintptr(len(shimPathBytes))
	cursor = alignUp(cursor)

	newArgvAddr := cursor
	n, err := WriteStringArray(pid, cursor, newArgv)
	if err != nil {
		t.blockExec(pid, regs, "write argv", err)
		return
	}
	cursor = alignUp(cursor + uintptr(n))

	newEnvpAddr := cursor
	if _, err := WriteStringArray(pid, cursor, newEnvp); err != nil {
		t.blockExec(pid, regs, "write envp", err)
		return
	}

	regs.SetPathAddr(isExecveat, newPathAddr)
	regs.SetArgvAddr(isExecveat, newArgvAddr)
	regs.SetEnvpAddr(isExecveat, newEnvpAddr)
	if err := regs.Set(pid); err != nil {
		t.log.Warn("failed to commit exec rewrite; killing tracee", "pid", pid, "path", pathname, "error", err)
		_ = unix.Kill(pid, syscall.SIGKILL)
	}
}

func (t *Tracer) blockExec(pid int, regs *SyscallRegs, step string, err error) {
	t.log.Warn("blocking exec after rewrite failure", "pid", pid, "step", step, "error", err)
	regs.BlockSyscall()
	if setErr := regs.Set(pid); setErr != nil {
		t.log.Warn("failed to block syscall; killing tracee", "pid", pid, "error", setErr)
		_ = unix.Kill(pid, syscall.SIGKILL)
	}
}

// cwdMapEnabled reports whether getcwd(2) / PWD remapping is configured.
func (t *Tracer) cwdMapEnabled() bool {
	return t.cfg.CwdFrom != "" && t.cfg.CwdTo != ""
}

// remapPWDEnv rewrites a "PWD=" entry in env using the same CwdFrom→CwdTo
// prefix rule as the getcwd(2) hijack, so the logical working directory that
// shells and tools read from $PWD matches what getcwd reports. It returns the
// (possibly new) slice and whether anything changed. The first PWD entry wins,
// mirroring how the kernel resolves duplicate env vars.
func remapPWDEnv(env []string, from, to string) ([]string, bool) {
	for i, e := range env {
		v, ok := strings.CutPrefix(e, "PWD=")
		if !ok {
			continue
		}
		mapped, matched := config.RewritePathPrefix(v, from, to)
		if !matched || mapped == v {
			return env, false
		}
		out := make([]string, len(env))
		copy(out, env)
		out[i] = "PWD=" + mapped
		return out, true
	}
	return env, false
}

// rewriteGetcwd rewrites the result of a getcwd(2) syscall so the traced
// process observes its working directory under CwdTo instead of CwdFrom.
// It runs on the syscall-exit stop using the buffer pointer/size captured at
// entry and the exit-stop registers. Paths outside CwdFrom and failed calls
// are left untouched; a remapped path that no longer fits the caller's buffer
// yields -ERANGE.
func (t *Tracer) rewriteGetcwd(pid int, ps *pidState, regs *SyscallRegs) {
	if ps.cwdBuf == 0 {
		return
	}
	ret, ok := regs.Ret()
	if !ok || ret == 0 {
		return // getcwd failed; leave the result alone
	}

	cwd, err := ReadString(pid, ps.cwdBuf)
	if err != nil || cwd == "" {
		return
	}
	mapped, matched := config.RewritePathPrefix(cwd, t.cfg.CwdFrom, t.cfg.CwdTo)
	if !matched || mapped == cwd {
		return
	}

	// The getcwd syscall returns the length of the path including the NUL.
	needed := len(mapped) + 1
	if uint64(needed) > ps.cwdSize {
		t.log.Warn("getcwd remap does not fit caller buffer; returning ERANGE",
			"pid", pid, "from", cwd, "to", mapped, "size", ps.cwdSize)
		regs.SetRetError(uintptr(unix.ERANGE))
		if err := regs.Set(pid); err != nil {
			t.log.Warn("failed to set getcwd ERANGE", "pid", pid, "error", err)
		}
		return
	}

	if err := WriteBytes(pid, ps.cwdBuf, append([]byte(mapped), 0)); err != nil {
		t.log.Warn("failed to write remapped getcwd buffer", "pid", pid, "error", err)
		return
	}
	regs.SetRetSuccess(uintptr(needed))
	if err := regs.Set(pid); err != nil {
		t.log.Warn("failed to commit getcwd remap", "pid", pid, "error", err)
		return
	}
	t.log.Log(t.ctx, logging.LevelTrace, "getcwd remapped", "pid", pid, "from", cwd, "to", mapped)
}

// remapAllowedExecPWD rewrites the PWD env var of a whitelisted (locally
// executing) exec so its logical working directory matches the remapped
// getcwd view. On any failure it leaves the original environment untouched —
// the exec is allowed to proceed regardless.
func (t *Tracer) remapAllowedExecPWD(pid int, regs *SyscallRegs, isExecveat bool) {
	envp, err := ReadStringArray(pid, regs.EnvpAddr(isExecveat))
	if err != nil {
		return
	}
	newEnvp, changed := remapPWDEnv(envp, t.cfg.CwdFrom, t.cfg.CwdTo)
	if !changed {
		return
	}

	// Stage the new envp below SP (minus the amd64 red zone), mirroring the
	// shim path. The kernel reads execve's envp from userspace before
	// unmapping the old address space, so this scratch region is safe.
	size := ptrSize // NULL terminator
	for _, s := range newEnvp {
		size += len(s) + 1 + ptrSize
	}
	sp := regs.StackPointer()
	cursor := (sp - 256 - uintptr(size)) &^ (uintptr(ptrSize) - 1)
	if _, err := WriteStringArray(pid, cursor, newEnvp); err != nil {
		t.log.Warn("failed to write remapped PWD envp; leaving original", "pid", pid, "error", err)
		return
	}
	regs.SetEnvpAddr(isExecveat, cursor)
	if err := regs.Set(pid); err != nil {
		t.log.Warn("failed to commit remapped PWD envp", "pid", pid, "error", err)
	}
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
	for _, entry := range t.cfg.Whitelist {
		if config.MatchLocalCommand(entry, pathname) {
			return true
		}
	}
	return false
}
