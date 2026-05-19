package ns

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// Namespace is the darwin runner. There is no kernel-level containment
// on macOS, so this struct holds only the bookkeeping needed to launch
// the child with the caller-supplied env and working directory.
// Exec interception is delivered by the DYLD interposer dylib whose
// path the caller injects into env as DYLD_INSERT_LIBRARIES.
type Namespace struct {
	deps Deps
	log  *slog.Logger
}

// New creates a Namespace. Call Prepare before Run.
func New(deps Deps) *Namespace {
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Namespace{deps: deps, log: log}
}

// Prepare is a no-op on darwin. No external helper to locate.
func (n *Namespace) Prepare(ctx context.Context) error {
	_ = ctx
	return nil
}

// Run executes cmdline directly with the caller-supplied env. binds are
// ignored: macOS has no unprivileged bind-mount facility. The caller is
// expected to point workingDir at the real FUSE mount path (no path
// translation happens here).
func (n *Namespace) Run(ctx context.Context, workingDir string, cmdline []string, env []string, binds []Bind) error {
	if len(cmdline) == 0 {
		return errors.New("ns: empty cmdline")
	}
	if len(binds) > 0 {
		n.log.Log(ctx, logging.LevelTrace, "ignoring bind mounts on darwin", "count", len(binds))
	}

	n.log.Debug("running on darwin (no containment)", "argv0", cmdline[0])

	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Dir = workingDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	return cmd.Run()
}

// Command returns the resolved binary and full argument list without
// executing anything. Useful for inspection and testing. On darwin this
// is effectively a pass-through.
func (n *Namespace) Command(workingDir string, cmdline []string, binds []Bind) (string, []string) {
	_ = workingDir
	_ = binds
	if len(cmdline) == 0 {
		return "", nil
	}
	return cmdline[0], cmdline[1:]
}

// Leave is a no-op on darwin.
func (n *Namespace) Leave() {}
