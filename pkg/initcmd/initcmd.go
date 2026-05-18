// Package initcmd resolves the entrypoint command passed to machineproxy
// and derives the local_commands entries that should accompany it.
//
// Two concerns live together here because they share inputs:
//
//  1. LookPath performs a PATH-style lookup that can skip arbitrary
//     directories. It is used to resolve the entrypoint without
//     consulting the FUSE-backed path-stub directory, which serves
//     remote ELFs that the local kernel cannot load.
//  2. DeriveLocalCommands inspects the resolved entrypoint for a
//     #!-shebang and returns the chain of paths (and, for env-style
//     interpreters, the env target name) that should be added to
//     local_commands so the tracer allows them to run locally.
package initcmd

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// LookPath resolves name to an absolute, executable path by searching
// pathEnv (a colon-separated PATH-style string), skipping any directory
// listed in skipDirs. Behavior mirrors exec.LookPath with one addition:
// directories whose cleaned form matches an entry in skipDirs are
// transparently bypassed.
//
// If name contains a slash, PATH is not consulted; the path is stat'd
// directly and converted to its absolute form against the current
// working directory.
//
// log must be non-nil; the search emits trace-level diagnostics for
// every PATH entry inspected and for the final hit, so callers can see
// which $PATH segment served the resolution.
func LookPath(ctx context.Context, log *slog.Logger, name, pathEnv string, skipDirs ...string) (string, error) {
	if name == "" {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	if strings.ContainsRune(name, '/') {
		if err := checkExec(name); err != nil {
			return "", &exec.Error{Name: name, Err: err}
		}
		abs, err := filepath.Abs(name)
		if err != nil {
			return "", &exec.Error{Name: name, Err: err}
		}
		log.Log(ctx, logging.LevelTrace, "LookPath: direct path", "name", name, "resolved", abs)
		return abs, nil
	}
	skip := make(map[string]struct{}, len(skipDirs))
	for _, d := range skipDirs {
		if d == "" {
			continue
		}
		skip[filepath.Clean(d)] = struct{}{}
	}
	for _, dir := range strings.Split(pathEnv, ":") {
		if dir == "" {
			continue
		}
		if _, dropped := skip[filepath.Clean(dir)]; dropped {
			log.Log(ctx, logging.LevelTrace, "LookPath: skipping PATH segment", "name", name, "dir", dir)
			continue
		}
		candidate := filepath.Join(dir, name)
		if err := checkExec(candidate); err != nil {
			log.Log(ctx, logging.LevelTrace, "LookPath: miss", "name", name, "dir", dir, "candidate", candidate, "error", err)
			continue
		}
		log.Log(ctx, logging.LevelTrace, "LookPath: hit", "name", name, "dir", dir, "resolved", candidate)
		return candidate, nil
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// checkExec returns nil if path is a regular file with at least one
// execute bit set. The error categories match what exec.LookPath would
// return for the same situation.
func checkExec(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	mode := st.Mode()
	if mode.IsDir() {
		return fs.ErrPermission
	}
	if mode.Perm()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}

// DeriveLocalCommands returns local_commands entries that describe the
// initial executable absPath and, if it is a #!-script, the chain of
// helpers the kernel will invoke to run it.
//
// The result always begins with absPath. If absPath starts with "#!",
// the interpreter is appended as an absolute-path rule. When the
// interpreter is env-like (basename "env"), the first non-flag token in
// the shebang argument is appended as a basename rule so the actual
// language runtime is also whitelisted.
//
// Errors reading the file are non-fatal; the function returns the entries
// it could derive plus the error so the caller can log it. log must be
// non-nil; the shebang outcome is recorded at trace level (whether a
// shebang was found, its interpreter, and whether the env-target branch
// fired).
func DeriveLocalCommands(ctx context.Context, log *slog.Logger, absPath string) ([]string, error) {
	out := []string{absPath}
	interp, arg, ok, err := readShebang(absPath)
	if !ok {
		log.Log(ctx, logging.LevelTrace, "DeriveLocalCommands: no shebang", "path", absPath, "error", err)
		return out, err
	}
	if interp != "" {
		out = append(out, interp)
	}
	if filepath.Base(interp) == "env" {
		if target := envTarget(arg); target != "" {
			log.Log(ctx, logging.LevelTrace, "DeriveLocalCommands: env-target", "path", absPath, "interpreter", interp, "arg", arg, "target", target)
			out = append(out, target)
		} else {
			log.Log(ctx, logging.LevelTrace, "DeriveLocalCommands: env shebang without target", "path", absPath, "interpreter", interp, "arg", arg)
		}
	} else {
		log.Log(ctx, logging.LevelTrace, "DeriveLocalCommands: shebang", "path", absPath, "interpreter", interp, "arg", arg)
	}
	return out, err
}

// readShebang reads up to the first newline of path and returns the
// interpreter and remaining argument. ok is false if the file is not a
// shebang script. err is returned alongside ok=false so the caller can
// distinguish "no shebang" (err nil) from "could not read" (err set).
//
// The 256-byte read window matches the Linux kernel's BINPRM_BUF_SIZE,
// which bounds how much of the shebang line the kernel itself parses.
func readShebang(path string) (interpreter, arg string, ok bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256)
	head, err := br.Peek(2)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if string(head) != "#!" {
		return "", "", false, nil
	}
	if _, err := br.Discard(2); err != nil {
		return "", "", false, err
	}
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", false, err
	}
	line = strings.TrimRight(line, "\r\n")
	line = strings.TrimLeft(line, " \t")
	if line == "" {
		return "", "", false, nil
	}
	if idx := strings.IndexAny(line, " \t"); idx >= 0 {
		interpreter = line[:idx]
		arg = strings.TrimLeft(line[idx:], " \t")
	} else {
		interpreter = line
	}
	return interpreter, arg, true, nil
}

// envTarget extracts the program name from an env-style shebang argument.
//
// The Linux kernel passes everything after the interpreter to env as a
// single argv element. GNU coreutils env then either treats that element
// as a literal command name (the common case) or, when it begins with
// "-S", re-splits the remainder on whitespace and parses option flags.
// We replicate just enough of that flag handling to find the first
// non-flag token; anything weirder falls through to "".
func envTarget(arg string) string {
	if arg == "" {
		return ""
	}
	fields := strings.Fields(arg)
	skipNext := false
	for _, f := range fields {
		if skipNext {
			skipNext = false
			continue
		}
		if f == "" {
			continue
		}
		if !strings.HasPrefix(f, "-") {
			return f
		}
		switch f {
		// env flags that consume an additional argv element.
		case "-u", "--unset", "-C", "--chdir":
			skipNext = true
		}
	}
	return ""
}
