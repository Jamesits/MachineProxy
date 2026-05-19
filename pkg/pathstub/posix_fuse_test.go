//go:build linux

package pathstub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/remote"
)

func TestPathstubFUSEReadOnlyConformance(t *testing.T) {
	requirePathstubFuseDevice(t)

	h := newPathstubFuseHarness(t)

	rootInfo, err := os.Stat(h.mount)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !rootInfo.IsDir() {
		t.Fatalf("root is not a directory: mode=%v", rootInfo.Mode())
	}
	if got := rootInfo.Mode().Perm(); got != 0o555 {
		t.Fatalf("root mode = %o, want 555", got)
	}

	entries, err := os.ReadDir(h.mount)
	if err != nil {
		t.Fatalf("readdir root: %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	if !sort.StringsAreSorted(gotNames) {
		t.Fatalf("entries are not sorted: %v", gotNames)
	}
	if strings.Join(gotNames, ",") != "env,tool" {
		t.Fatalf("entries = %v, want [env tool]", gotNames)
	}

	toolPath := filepath.Join(h.mount, "tool")
	info, err := os.Stat(toolPath)
	if err != nil {
		t.Fatalf("stat tool: %v", err)
	}
	if info.IsDir() {
		t.Fatal("tool unexpectedly reported as directory")
	}
	if got := info.Mode().Perm(); got != 0o555 {
		t.Fatalf("tool mode = %o, want 555", got)
	}
	if info.Size() != int64(len("tool-content")) {
		t.Fatalf("tool size = %d, want %d", info.Size(), len("tool-content"))
	}

	data, err := os.ReadFile(toolPath)
	if err != nil {
		t.Fatalf("read tool through mount: %v", err)
	}
	if string(data) != "tool-content" {
		t.Fatalf("tool contents = %q, want tool-content", data)
	}
	if got := h.opener.LastOpened(); got != h.remotePath("tool") {
		t.Fatalf("opener path = %q, want %q", got, h.remotePath("tool"))
	}

	if _, err := os.Stat(filepath.Join(h.mount, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat missing error = %v, want ENOENT", err)
	}

	assertReadOnlyError(t, "write existing", os.WriteFile(toolPath, []byte("overwrite"), 0o555))
	assertReadOnlyError(t, "create file", os.WriteFile(filepath.Join(h.mount, "new"), []byte("new"), 0o555))
	assertReadOnlyError(t, "remove file", os.Remove(toolPath))
	assertReadOnlyError(t, "mkdir", os.Mkdir(filepath.Join(h.mount, "dir"), 0o755))
	assertReadOnlyError(t, "rename", os.Rename(toolPath, filepath.Join(h.mount, "renamed")))
	assertReadOnlyError(t, "chmod", os.Chmod(toolPath, 0o755))
	assertReadOnlyError(t, "truncate", os.Truncate(toolPath, 1))
}

type pathstubFuseHarness struct {
	backing string
	mount   string
	opener  *pathstubLocalOpener
}

func newPathstubFuseHarness(t *testing.T) *pathstubFuseHarness {
	t.Helper()

	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	tmp := t.TempDir()
	backing := filepath.Join(tmp, "remote")
	mount := filepath.Join(tmp, "mnt")
	if err := os.Mkdir(backing, 0o755); err != nil {
		t.Fatalf("mkdir backing: %v", err)
	}
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatalf("mkdir mount: %v", err)
	}

	files := map[string]string{
		"tool": "tool-content",
		"env":  "env-content",
	}
	entries := make([]agentproto.PathInfoEntry, 0, len(files))
	mtime := time.Unix(1_700_000_000, 123_000_000)
	for name, content := range files {
		remotePath := filepath.Join(backing, name)
		if err := os.WriteFile(remotePath, []byte(content), 0o755); err != nil {
			t.Fatalf("write remote %s: %v", name, err)
		}
		entries = append(entries, agentproto.PathInfoEntry{
			Name:       name,
			RemotePath: remotePath,
			Mode:       0o755,
			Size:       int64(len(content)),
			MTimeNanos: mtime.UnixNano(),
		})
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	opener := &pathstubLocalOpener{}
	fsys := New(opener, entries, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server, err := Mount(ctx, fsys, mount)
	if err != nil {
		cancel()
		if isPathstubFuseUnavailable(err) {
			t.Skipf("FUSE mount unavailable: %v", err)
		}
		t.Fatalf("mount pathstub: %v", err)
	}
	t.Cleanup(func() {
		defer cancel()
		if err := server.Unmount(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
			t.Fatalf("unmount pathstub: %v", err)
		}
	})

	return &pathstubFuseHarness{backing: backing, mount: mount, opener: opener}
}

func (h *pathstubFuseHarness) remotePath(name string) string {
	return filepath.Join(h.backing, name)
}

type pathstubLocalOpener struct {
	mu         sync.Mutex
	lastOpened string
}

func (o *pathstubLocalOpener) Open(path string) (remote.RemoteFile, error) {
	o.mu.Lock()
	o.lastOpened = path
	o.mu.Unlock()
	return os.Open(path)
}

func (o *pathstubLocalOpener) LastOpened() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastOpened
}

func assertReadOnlyError(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s succeeded, want read-only error", op)
	}
	if errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return
	}
	t.Fatalf("%s error = %v, want EROFS/EACCES/EPERM", op, err)
}

func requirePathstubFuseDevice(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("/dev/fuse unavailable: %v", err)
	}
}

func isPathstubFuseUnavailable(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.EPERM)
}
