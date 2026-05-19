package ns

import (
	"context"
	"log/slog"
	"os"
	"os/exec"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// Namespace builds and executes a bubblewrap (bwrap) sandbox that
// bind-mounts the host filesystem and overlays a FUSE-backed workspace.
type Namespace struct {
	deps     Deps
	log      *slog.Logger
	bwrapBin string
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

// Prepare locates the bwrap binary. Must be called before Run.
func (n *Namespace) Prepare(ctx context.Context) error {
	_ = ctx
	bin, err := n.deps.LookPath("bwrap")
	if err != nil {
		return err
	}
	n.bwrapBin = bin
	return nil
}

// Run executes cmdline inside a bwrap sandbox. The host root is
// bind-mounted read-write, and each entry in binds is bind-mounted onto
// its Dst path so FUSE-backed directories (workspace, path-stub) appear
// at the expected container locations. workingDir sets the initial
// working directory inside the container.
func (n *Namespace) Run(ctx context.Context, workingDir string, cmdline []string, env []string, binds []Bind) error {
	args := buildBwrapArgs(workingDir, cmdline, binds)

	n.log.Debug("running in namespace", "bwrap", n.bwrapBin, "binds", len(binds))
	n.log.Log(ctx, logging.LevelTrace, "bwrap full args", "args", args)

	cmd := exec.CommandContext(ctx, n.bwrapBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	return cmd.Run()
}

// Command returns the bwrap binary path and full argument list without
// executing anything. Useful for inspection and testing.
func (n *Namespace) Command(workingDir string, cmdline []string, binds []Bind) (string, []string) {
	return n.bwrapBin, buildBwrapArgs(workingDir, cmdline, binds)
}

func buildBwrapArgs(workingDir string, cmdline []string, binds []Bind) []string {
	args := []string{"--dev-bind", "/", "/"}
	for _, b := range binds {
		args = append(args, "--bind", b.Src, b.Dst)
	}
	args = append(args, "--chdir", workingDir, "--die-with-parent")
	args = append(args, cmdline...)
	return args
}

// Leave is a no-op retained for interface compatibility.
// Bwrap cleans up its own namespaces on exit.
func (n *Namespace) Leave() {}
