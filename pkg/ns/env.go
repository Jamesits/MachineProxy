package ns

import (
	"log/slog"
	"strings"
)

// Bind describes one bind mount entry. Src is a path on the host; Dst
// is the path it appears at inside the container. Linux honours this
// via bwrap --bind; darwin (no containment) ignores it.
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

// Deps allows dependency injection for testing.
type Deps struct {
	// LookPath resolves external helpers (e.g. bwrap on Linux). Defaults
	// to exec.LookPath when nil. Unused on platforms with no helper.
	LookPath func(file string) (string, error)
	Log      *slog.Logger
}

// defaultPath is used when the inherited environment has no PATH at all
// but we still need to inject a stub directory. Matches typical Linux
// distributions; darwin shells get the same fallback for consistency.
const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

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
