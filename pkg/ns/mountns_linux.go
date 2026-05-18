package ns

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// Bind describes one bwrap --bind entry. Src is a path on the host;
// Dst is the path it appears at inside the namespace.
type Bind struct {
	Src string
	Dst string
}

// PathInjection controls splicing an extra directory into the PATH env
// variable for the container process.
type PathInjection struct {
	// Dir is the absolute path (as seen inside the container) to splice
	// in. When empty, FormatEnv leaves PATH untouched.
	Dir string
	// Position is "prepend" or "append". Empty means prepend.
	Position string
}

// defaultPath is used when the inherited environment has no PATH at all
// but we still need to inject a stub directory. Matches typical Linux
// distributions.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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

// FormatEnv builds the environment slice for the child process,
// injecting machineproxy-specific variables needed by the shim. When
// pathInj.Dir is set, it is prepended or appended to PATH (creating a
// PATH if none exists in base) so the FUSE-backed stub directory is
// resolved by the shell's PATH search.
func FormatEnv(base []string, brokerSock, shimPath string, pathInj PathInjection) []string {
	env := make([]string, 0, len(base)+2)
	env = append(env, base...)
	if pathInj.Dir != "" {
		env = injectPath(env, pathInj.Dir, pathInj.Position)
	}
	env = append(env,
		"MPROXY_BROKER_SOCK="+brokerSock,
		"MPROXY_SHIM_PATH="+shimPath,
	)
	return env
}

// injectPath splices dir into the PATH entry of env, creating one if
// absent. position == "append" puts dir at the end; anything else
// (including "" and "prepend") puts it at the start.
func injectPath(env []string, dir, position string) []string {
	prepend := position != "append"
	for i, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		current := entry[len("PATH="):]
		if containsPathSegment(current, dir) {
			return env
		}
		if current == "" {
			env[i] = "PATH=" + dir
			return env
		}
		if prepend {
			env[i] = "PATH=" + dir + ":" + current
		} else {
			env[i] = "PATH=" + current + ":" + dir
		}
		return env
	}
	// No existing PATH; create one. Include a sane default fallback so
	// shells that rely on $PATH being non-trivial still work.
	if prepend {
		env = append(env, "PATH="+dir+":"+defaultPath)
	} else {
		env = append(env, "PATH="+defaultPath+":"+dir)
	}
	return env
}

func containsPathSegment(path, dir string) bool {
	if path == "" {
		return false
	}
	for _, p := range strings.Split(path, ":") {
		if p == dir {
			return true
		}
	}
	return false
}
