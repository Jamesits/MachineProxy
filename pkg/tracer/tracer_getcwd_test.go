//go:build linux && (amd64 || arm64)

package tracer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTracerRemapsGetcwd exercises the getcwd(2) interception end-to-end with
// a real child that issues the raw getcwd syscall (bypassing glibc buffer
// management so the tracer observes the caller's exact buffer size).
func TestTracerRemapsGetcwd(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skipf("gcc not available: %v", err)
	}

	tempDir := t.TempDir()
	helper := buildCProgram(t, tempDir, "getcwd-helper", getcwdHelperSource)
	base := filepath.Join(tempDir, "ws")
	sub := filepath.Join(base, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	errLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	run := func(t *testing.T, cwdTo, bufSize string) string {
		t.Helper()
		logPath := filepath.Join(t.TempDir(), "getcwd.log")
		tr := New(Config{
			Log:     errLog,
			CwdFrom: base,
			CwdTo:   cwdTo,
		})
		code, err := tr.Start(context.Background(),
			[]string{helper, sub, bufSize},
			[]string{"GETCWD_LOG=" + logPath}, nil)
		if err != nil {
			t.Fatalf("tracer start: %v", err)
		}
		if code != 0 {
			t.Fatalf("helper exit code = %d, want 0", code)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return strings.TrimSpace(string(data))
	}

	t.Run("remaps prefix preserving subpath", func(t *testing.T) {
		got := run(t, "/remote/ws", "4096")
		want := "cwd=/remote/ws/sub"
		if !strings.HasPrefix(got, want) {
			t.Fatalf("got %q, want prefix %q", got, want)
		}
	})

	t.Run("erange when remapped path does not fit", func(t *testing.T) {
		// Buffer fits the container path exactly; the longer remote path
		// must not fit, so the tracer returns ERANGE.
		cwdTo := "/" + strings.Repeat("r", len(base)+64)
		bufSize := fmt.Sprintf("%d", len(sub)+1)
		got := run(t, cwdTo, bufSize)
		if got != "errno=ERANGE" {
			t.Fatalf("got %q, want %q", got, "errno=ERANGE")
		}
	})

	// A traced execve skews the entry/exit parity; getcwd must still be
	// remapped for a process that calls it after an exec.
	t.Run("remaps after a traced execve", func(t *testing.T) {
		reexecHelper := buildCProgram(t, tempDir, "getcwd-reexec-helper", getcwdReexecHelperSource)
		logPath := filepath.Join(t.TempDir(), "getcwd.log")
		tr := New(Config{
			Log:       errLog,
			Whitelist: []string{reexecHelper}, // allow the self-exec to run locally
			CwdFrom:   base,
			CwdTo:     "/remote/ws",
		})
		code, err := tr.Start(context.Background(),
			[]string{reexecHelper, sub, "4096"},
			[]string{"GETCWD_LOG=" + logPath}, nil)
		if err != nil {
			t.Fatalf("tracer start: %v", err)
		}
		if code != 0 {
			t.Fatalf("helper exit code = %d, want 0", code)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if got := strings.TrimSpace(string(data)); got != "cwd=/remote/ws/sub" {
			t.Fatalf("got %q, want %q", got, "cwd=/remote/ws/sub")
		}
	})
}

// TestTracerRemapsPWD verifies the $PWD environment variable is rewritten by
// the same prefix rule as getcwd: once for the initial process env, and again
// for a whitelisted exec whose envp carries a container-side PWD.
func TestTracerRemapsPWD(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skipf("gcc not available: %v", err)
	}
	tempDir := t.TempDir()
	helper := buildCProgram(t, tempDir, "pwd-helper", pwdHelperSource)
	base := filepath.Join(tempDir, "ws")
	errLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("remaps PWD in the initial env", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "pwd.log")
		tr := New(Config{Log: errLog, CwdFrom: base, CwdTo: "/remote/ws"})
		// REEXEC_DONE set => the helper skips the re-exec and just prints PWD.
		code, err := tr.Start(context.Background(),
			[]string{helper, "unused"},
			[]string{"PWD_LOG=" + logPath, "PWD=" + base + "/sub", "REEXEC_DONE=1"}, nil)
		if err != nil {
			t.Fatalf("tracer start: %v", err)
		}
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		got := readTrimmed(t, logPath)
		if got != "PWD=/remote/ws/sub" {
			t.Fatalf("got %q, want %q", got, "PWD=/remote/ws/sub")
		}
	})

	t.Run("remaps PWD set before a whitelisted exec", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "pwd.log")
		tr := New(Config{Log: errLog, Whitelist: []string{helper}, CwdFrom: base, CwdTo: "/remote/ws"})
		// No PWD in the initial env (so the init-path remap is a no-op); the
		// helper sets PWD=<base>/sub itself, then re-execs. The tracer must
		// remap PWD on that whitelisted exec.
		code, err := tr.Start(context.Background(),
			[]string{helper, base + "/sub"},
			[]string{"PWD_LOG=" + logPath}, nil)
		if err != nil {
			t.Fatalf("tracer start: %v", err)
		}
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		got := readTrimmed(t, logPath)
		if got != "PWD=/remote/ws/sub" {
			t.Fatalf("got %q, want %q", got, "PWD=/remote/ws/sub")
		}
	})
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// pwdHelperSource sets PWD and re-execs itself on the first pass, then prints
// $PWD on the second. With REEXEC_DONE already set it prints immediately.
const pwdHelperSource = `
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc != 2) return 2;
    const char *log_path = getenv("PWD_LOG");
    if (log_path == NULL) return 3;
    if (getenv("REEXEC_DONE") == NULL) {
        setenv("PWD", argv[1], 1);
        setenv("REEXEC_DONE", "1", 1);
        execv(argv[0], argv);
        perror("execv");
        return 90;
    }
    FILE *f = fopen(log_path, "w");
    if (f == NULL) return 7;
    const char *pwd = getenv("PWD");
    fprintf(f, "PWD=%s\n", pwd != NULL ? pwd : "<unset>");
    fclose(f);
    return 0;
}
`

const getcwdHelperSource = `
#define _GNU_SOURCE
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

// argv[1] = directory to chdir into, argv[2] = getcwd buffer size.
int main(int argc, char **argv) {
    if (argc != 3) return 2;
    const char *log_path = getenv("GETCWD_LOG");
    if (log_path == NULL) return 3;
    if (chdir(argv[1]) != 0) { perror("chdir"); return 4; }
    long bufsize = atol(argv[2]);
    if (bufsize <= 0 || bufsize > 65536) return 5;
    char *buf = calloc(1, (size_t)bufsize);
    if (buf == NULL) return 6;
    FILE *f = fopen(log_path, "w");
    if (f == NULL) return 7;
    long ret = syscall(SYS_getcwd, buf, (unsigned long)bufsize);
    if (ret < 0) {
        fprintf(f, "errno=%s\n", errno == ERANGE ? "ERANGE" : strerror(errno));
    } else {
        fprintf(f, "cwd=%s\n", buf);
    }
    fclose(f);
    free(buf);
    return 0;
}
`

// getcwdReexecHelperSource re-execs itself once (exercising the tracer's
// execve handling), then on the second pass chdirs and issues a raw getcwd.
const getcwdReexecHelperSource = `
#define _GNU_SOURCE
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

int main(int argc, char **argv) {
    if (argc != 3) return 2;
    if (getenv("REEXEC_DONE") == NULL) {
        setenv("REEXEC_DONE", "1", 1);
        execv(argv[0], argv);
        perror("execv");
        return 90;
    }
    const char *log_path = getenv("GETCWD_LOG");
    if (log_path == NULL) return 3;
    if (chdir(argv[1]) != 0) { perror("chdir"); return 4; }
    long bufsize = atol(argv[2]);
    if (bufsize <= 0 || bufsize > 65536) return 5;
    char *buf = calloc(1, (size_t)bufsize);
    if (buf == NULL) return 6;
    FILE *f = fopen(log_path, "w");
    if (f == NULL) return 7;
    long ret = syscall(SYS_getcwd, buf, (unsigned long)bufsize);
    if (ret < 0) {
        fprintf(f, "errno=%s\n", errno == ERANGE ? "ERANGE" : strerror(errno));
    } else {
        fprintf(f, "cwd=%s\n", buf);
    }
    fclose(f);
    free(buf);
    return 0;
}
`
