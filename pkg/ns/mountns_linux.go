package ns

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Deps allows dependency injection for testing.
type Deps struct {
	// LookPath resolves the bwrap binary. Defaults to exec.LookPath.
	LookPath func(file string) (string, error)
}

// Namespace builds and executes a bubblewrap (bwrap) sandbox that
// bind-mounts the host filesystem and overlays a FUSE-backed workspace.
type Namespace struct {
	deps     Deps
	bwrapBin string
}

// New creates a Namespace. Call Prepare before Run.
func New(deps Deps) *Namespace {
	if deps.LookPath == nil {
		deps.LookPath = exec.LookPath
	}
	return &Namespace{deps: deps}
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
func (n *Namespace) Run(ctx context.Context, fuseMountDir, containerPath string, cmdline []string, env []string) error {
	args := []string{
		"--dev-bind", "/", "/",
		"--bind", fuseMountDir, containerPath,
		"--die-with-parent",
	}
	args = append(args, cmdline...)

	cmd := exec.CommandContext(ctx, n.bwrapBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	return cmd.Run()
}

// Command returns the bwrap binary path and full argument list without
// executing anything. Useful for inspection and testing.
func (n *Namespace) Command(fuseMountDir, containerPath string, cmdline []string) (string, []string) {
	args := []string{
		"--dev-bind", "/", "/",
		"--bind", fuseMountDir, containerPath,
		"--die-with-parent",
	}
	args = append(args, cmdline...)
	return n.bwrapBin, args
}

// Leave is a no-op retained for interface compatibility.
// Bwrap cleans up its own namespaces on exit.
func (n *Namespace) Leave() {}

// FormatEnv builds the environment slice for the child process,
// injecting machineproxy-specific variables and LD_PRELOAD.
func FormatEnv(base []string, brokerSock, shimPath string, whitelist []string, hookLib string) []string {
	env := append([]string{}, base...)
	env = append(env,
		"MPROXY_BROKER_SOCK="+brokerSock,
		"MPROXY_SHIM_PATH="+shimPath,
		"MPROXY_WHITELIST="+strings.Join(whitelist, ":"),
	)

	existing := ""
	for _, e := range base {
		if strings.HasPrefix(e, "LD_PRELOAD=") {
			existing = e[len("LD_PRELOAD="):]
			break
		}
	}
	if existing != "" {
		env = append(env, "LD_PRELOAD="+hookLib+":"+existing)
	} else {
		env = append(env, "LD_PRELOAD="+hookLib)
	}
	return env
}
