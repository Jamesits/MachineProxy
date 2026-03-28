package ns

import (
	"context"
	"log/slog"
	"os"
	"os/exec"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// Deps allows dependency injection for testing.
type Deps struct {
	// LookPath resolves the bwrap binary. Defaults to exec.LookPath.
	LookPath func(file string) (string, error)
	Log      *slog.Logger
}

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
// bind-mounted read-write, and fuseMountDir is bound over containerPath
// so the FUSE workspace is visible at the expected location.
// workingDir sets the initial working directory inside the container.
func (n *Namespace) Run(ctx context.Context, fuseMountDir, containerPath, workingDir string, cmdline []string, env []string) error {
	args := []string{
		"--dev-bind", "/", "/",
		"--bind", fuseMountDir, containerPath,
		"--chdir", workingDir,
		"--die-with-parent",
	}
	args = append(args, cmdline...)

	n.log.Debug("running in namespace", "bwrap", n.bwrapBin, "container_path", containerPath)
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
func (n *Namespace) Command(fuseMountDir, containerPath, workingDir string, cmdline []string) (string, []string) {
	args := []string{
		"--dev-bind", "/", "/",
		"--bind", fuseMountDir, containerPath,
		"--chdir", workingDir,
		"--die-with-parent",
	}
	args = append(args, cmdline...)
	return n.bwrapBin, args
}

// Leave is a no-op retained for interface compatibility.
// Bwrap cleans up its own namespaces on exit.
func (n *Namespace) Leave() {}

// FormatEnv builds the environment slice for the child process,
// injecting machineproxy-specific variables needed by the shim.
func FormatEnv(base []string, brokerSock, shimPath string) []string {
	env := append([]string{}, base...)
	env = append(env,
		"MPROXY_BROKER_SOCK="+brokerSock,
		"MPROXY_SHIM_PATH="+shimPath,
	)
	return env
}
