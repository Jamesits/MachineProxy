//go:build linux && (amd64 || arm64)

package tracer

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTracerCapturesLinuxExecVariants(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skipf("gcc not available: %v", err)
	}

	tempDir := t.TempDir()
	shimPath := buildCProgram(t, tempDir, "capture-shim", captureShimSource)
	helperPath := buildCProgram(t, tempDir, "exec-helper", execHelperSource)
	targetPath := buildCProgram(t, tempDir, "exec-target", execTargetSource)
	logPath := filepath.Join(tempDir, "capture.log")

	variants := []string{
		"execve",
		"execveat-absolute",
		"execveat-relative",
	}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			if err := os.Remove(logPath); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove log: %v", err)
			}

			tr := New(Config{
				ShimPath: shimPath,
				Log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			})
			code, err := tr.Start(context.Background(), []string{helperPath, variant, targetPath}, []string{"CAPTURE_LOG=" + logPath}, nil)
			if err != nil {
				t.Fatalf("tracer start: %v", err)
			}
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}

			logData, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read capture log: %v", err)
			}
			if !strings.Contains(string(logData), "shim invoked") {
				t.Fatalf("variant %s was not captured by shim; log=%q", variant, string(logData))
			}
		})
	}
}

func TestTracerWarnsAndLeaksFDExecVariants(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skipf("gcc not available: %v", err)
	}

	tempDir := t.TempDir()
	shimPath := buildCProgram(t, tempDir, "capture-shim", captureShimSource)
	helperPath := buildCProgram(t, tempDir, "exec-helper", execHelperSource)
	targetPath := buildCProgram(t, tempDir, "exec-target", execTargetSource)
	logPath := filepath.Join(tempDir, "capture.log")

	variants := []string{"execveat-empty", "fexecve"}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			var logBuf bytes.Buffer
			tr := New(Config{
				ShimPath: shimPath,
				Log:      slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})),
			})
			code, err := tr.Start(context.Background(), []string{helperPath, variant, targetPath}, []string{"CAPTURE_LOG=" + logPath}, nil)
			if err != nil {
				t.Fatalf("tracer start: %v", err)
			}
			if code != 77 {
				t.Fatalf("exit code = %d, want leaked target exit 77", code)
			}

			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("shim log exists or unexpected stat error after leaked exec: %v", err)
			}
			if !strings.Contains(logBuf.String(), "leaked execveat call") {
				t.Fatalf("warning log %q does not mention leaked execveat call", logBuf.String())
			}
		})
	}
}

func buildCProgram(t *testing.T, dir, name, source string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.Command("gcc", "-x", "c", "-O2", "-Wall", "-Wextra", "-o", path, "-")
	cmd.Stdin = strings.NewReader(source)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s for %s: %v\n%s", name, runtime.GOARCH, err, stderr.String())
	}
	return path
}

const captureShimSource = `
#include <stdio.h>
#include <stdlib.h>

int main(int argc, char **argv) {
    const char *log_path = getenv("CAPTURE_LOG");
    if (log_path == NULL) return 101;
    FILE *f = fopen(log_path, "a");
    if (f == NULL) return 102;
    fprintf(f, "shim invoked argc=%d original=%s\n", argc, argc > 1 ? argv[1] : "<missing>");
    fclose(f);
    return 0;
}
`

const execTargetSource = `
int main(void) { return 77; }
`

var execHelperSource = fmt.Sprintf(`
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <libgen.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

#ifndef AT_EMPTY_PATH
#define AT_EMPTY_PATH 0x1000
#endif

extern char **environ;

int main(int argc, char **argv) {
    if (argc != 3) return 2;
    const char *variant = argv[1];
    const char *target = argv[2];
    char *child_argv[] = {"exec-target", NULL};

    if (strcmp(variant, "execve") == 0) {
        execve(target, child_argv, environ);
    } else if (strcmp(variant, "execveat-absolute") == 0) {
        syscall(%d, AT_FDCWD, target, child_argv, environ, 0);
    } else if (strcmp(variant, "execveat-relative") == 0) {
        char *copy = strdup(target);
        if (copy == NULL) return 3;
        int dirfd = open(dirname(copy), O_RDONLY | O_DIRECTORY);
        free(copy);
        if (dirfd < 0) return 4;
        copy = strdup(target);
        if (copy == NULL) return 5;
        const char *base = basename(copy);
        syscall(%d, dirfd, base, child_argv, environ, 0);
        free(copy);
    } else if (strcmp(variant, "execveat-empty") == 0) {
        int fd = open(target, O_PATH);
        if (fd < 0) return 6;
        syscall(%d, fd, "", child_argv, environ, AT_EMPTY_PATH);
    } else if (strcmp(variant, "fexecve") == 0) {
        int fd = open(target, O_RDONLY);
        if (fd < 0) return 7;
        fexecve(fd, child_argv, environ);
    } else {
        return 8;
    }

    perror("exec variant failed");
    return errno == ENOSYS ? 99 : 98;
}
`, SysExecveat(), SysExecveat(), SysExecveat())
