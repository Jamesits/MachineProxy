//go:build linux

package workspacefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// fuseCase exercises workspacefs through the kernel syscall surface.
// The backing directory is the oracle for the remote.FileClient view.
type fuseCase struct {
	name string
	run  func(t *testing.T, h *workspaceFuseHarness)
}

func TestWorkspaceFSPOSIXConformance(t *testing.T) {
	requireFuseDevice(t)

	cases := []fuseCase{
		{name: "FileBasic", run: testWorkspaceFuseFileBasic},
		{name: "OpenFlagMatrix", run: testWorkspaceFuseOpenFlagMatrix},
		{name: "PartialReadWrite", run: testWorkspaceFusePartialReadWrite},
		{name: "DirectoryEntries", run: testWorkspaceFuseDirectoryEntries},
		{name: "RenameLinkSymlink", run: testWorkspaceFuseRenameLinkSymlink},
		{name: "MetadataTruncateStatfs", run: testWorkspaceFuseMetadataTruncateStatfs},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWorkspaceFuseHarness(t)
			tc.run(t, h)
		})
	}
}

type workspaceFuseHarness struct {
	backing string
	mount   string
}

func newWorkspaceFuseHarness(t *testing.T) *workspaceFuseHarness {
	t.Helper()

	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	tmp := t.TempDir()
	backing := filepath.Join(tmp, "backing")
	mount := filepath.Join(tmp, "mnt")
	if err := os.Mkdir(backing, 0o755); err != nil {
		t.Fatalf("mkdir backing: %v", err)
	}
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatalf("mkdir mount: %v", err)
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	fsys := New(localFileClient{}, backing, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server, err := Mount(ctx, fsys, mount)
	if err != nil {
		cancel()
		if isFuseUnavailable(err) {
			t.Skipf("FUSE mount unavailable: %v", err)
		}
		t.Fatalf("mount workspacefs: %v", err)
	}
	t.Cleanup(func() {
		defer cancel()
		if err := server.Unmount(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
			t.Fatalf("unmount workspacefs: %v", err)
		}
	})

	return &workspaceFuseHarness{backing: backing, mount: mount}
}

func testWorkspaceFuseFileBasic(t *testing.T, h *workspaceFuseHarness) {
	path := filepath.Join(h.mount, "hello.txt")
	want := []byte("hello through fuse")
	if err := os.WriteFile(path, want, 0o640); err != nil {
		t.Fatalf("write through mount: %v", err)
	}

	h.verifyFile(t, "hello.txt", want)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat through mount: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
}

func testWorkspaceFuseOpenFlagMatrix(t *testing.T, h *workspaceFuseHarness) {
	created := filepath.Join(h.mount, "created.txt")
	f, err := os.OpenFile(created, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("open create exclusive: %v", err)
	}
	if _, err := f.Write([]byte("first")); err != nil {
		t.Fatalf("write created file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close created file: %v", err)
	}

	if _, err := os.OpenFile(created, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("open existing with O_EXCL error = %v, want EEXIST", err)
	}

	f, err = os.OpenFile(created, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatalf("open truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close truncated file: %v", err)
	}
	h.verifyFile(t, "created.txt", nil)

	f, err = os.OpenFile(created, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.Write([]byte("a")); err != nil {
		t.Fatalf("append first byte: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek append file: %v", err)
	}
	if _, err := f.Write([]byte("b")); err != nil {
		t.Fatalf("append after seek: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append file: %v", err)
	}
	h.verifyFile(t, "created.txt", []byte("ab"))
}

func testWorkspaceFusePartialReadWrite(t *testing.T, h *workspaceFuseHarness) {
	path := filepath.Join(h.mount, "sparse.bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open sparse file: %v", err)
	}
	if _, err := f.WriteAt([]byte("abc"), 0); err != nil {
		t.Fatalf("write prefix: %v", err)
	}
	if _, err := f.WriteAt([]byte("XYZ"), 6); err != nil {
		t.Fatalf("write suffix: %v", err)
	}

	buf := make([]byte, 9)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read sparse file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close sparse file: %v", err)
	}

	want := []byte{'a', 'b', 'c', 0, 0, 0, 'X', 'Y', 'Z'}
	if string(buf) != string(want) {
		t.Fatalf("ReadAt buffer = %q, want %q", buf, want)
	}
	h.verifyFile(t, "sparse.bin", want)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read full sparse file: %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("ReadFile = %q, want %q", data, want)
	}
}

func testWorkspaceFuseDirectoryEntries(t *testing.T, h *workspaceFuseHarness) {
	dir := filepath.Join(h.mount, "dir")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("mkdir through mount: %v", err)
	}
	for i := 0; i < 32; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir through mount: %v", err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	for i := 0; i < 32; i++ {
		want := fmt.Sprintf("file-%02d.txt", i)
		if got[i] != want {
			t.Fatalf("entry[%d] = %q, want %q (all: %v)", i, got[i], want, got)
		}
	}

	if err := os.Remove(filepath.Join(dir, "file-00.txt")); err != nil {
		t.Fatalf("unlink through mount: %v", err)
	}
	if err := os.Remove(dir); !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("remove non-empty dir error = %v, want ENOTEMPTY", err)
	}
	for i := 1; i < 32; i++ {
		if err := os.Remove(filepath.Join(dir, fmt.Sprintf("file-%02d.txt", i))); err != nil {
			t.Fatalf("remove child %d: %v", i, err)
		}
	}
	if err := os.Remove(dir); err != nil {
		t.Fatalf("remove empty dir: %v", err)
	}
}

func testWorkspaceFuseRenameLinkSymlink(t *testing.T, h *workspaceFuseHarness) {
	if err := os.WriteFile(filepath.Join(h.mount, "target.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Link(filepath.Join(h.mount, "target.txt"), filepath.Join(h.mount, "hard.txt")); err != nil {
		t.Fatalf("hardlink through mount: %v", err)
	}
	h.verifyFile(t, "hard.txt", []byte("content"))

	if err := os.Symlink("target.txt", filepath.Join(h.mount, "sym.txt")); err != nil {
		t.Fatalf("symlink through mount: %v", err)
	}
	target, err := os.Readlink(filepath.Join(h.mount, "sym.txt"))
	if err != nil {
		t.Fatalf("readlink through mount: %v", err)
	}
	if target != "target.txt" {
		t.Fatalf("readlink = %q, want target.txt", target)
	}

	if err := os.Rename(filepath.Join(h.mount, "target.txt"), filepath.Join(h.mount, "renamed.txt")); err != nil {
		t.Fatalf("rename through mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.mount, "target.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat old path error = %v, want ENOENT", err)
	}
	h.verifyFile(t, "renamed.txt", []byte("content"))
	if _, err := os.Stat(filepath.Join(h.backing, "renamed.txt")); err != nil {
		t.Fatalf("renamed file missing from backing store: %v", err)
	}
}

func testWorkspaceFuseMetadataTruncateStatfs(t *testing.T, h *workspaceFuseHarness) {
	path := filepath.Join(h.mount, "meta.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write meta file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod through mount: %v", err)
	}
	mtime := time.Unix(1_700_000_000, 123_000_000)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes through mount: %v", err)
	}
	if err := os.Truncate(path, 4); err != nil {
		t.Fatalf("truncate shrink through mount: %v", err)
	}
	h.verifyFile(t, "meta.txt", []byte("0123"))
	if err := os.Truncate(path, 8); err != nil {
		t.Fatalf("truncate grow through mount: %v", err)
	}
	h.verifyFile(t, "meta.txt", []byte{'0', '1', '2', '3', 0, 0, 0, 0})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat meta file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}

	var st unix.Statfs_t
	if err := unix.Statfs(h.mount, &st); err != nil {
		t.Fatalf("statfs through mount: %v", err)
	}
	if st.Bsize == 0 || st.Namelen == 0 {
		t.Fatalf("statfs returned invalid values: bsize=%d namelen=%d", st.Bsize, st.Namelen)
	}
}

func (h *workspaceFuseHarness) verifyFile(t *testing.T, rel string, want []byte) {
	t.Helper()
	mountPath := filepath.Join(h.mount, rel)
	backingPath := filepath.Join(h.backing, rel)

	got, err := os.ReadFile(mountPath)
	if err != nil {
		t.Fatalf("read %s through mount: %v", rel, err)
	}
	if string(got) != string(want) {
		t.Fatalf("mount contents for %s = %q, want %q", rel, got, want)
	}

	backing, err := os.ReadFile(backingPath)
	if err != nil {
		t.Fatalf("read %s from backing store: %v", rel, err)
	}
	if string(backing) != string(want) {
		t.Fatalf("backing contents for %s = %q, want %q", rel, backing, want)
	}

	info, err := os.Stat(mountPath)
	if err != nil {
		t.Fatalf("stat %s through mount: %v", rel, err)
	}
	if info.Size() != int64(len(want)) {
		t.Fatalf("stat size for %s = %d, want %d", rel, info.Size(), len(want))
	}
	f, err := os.Open(mountPath)
	if err != nil {
		t.Fatalf("open %s through mount: %v", rel, err)
	}
	defer f.Close()
	finfo, err := f.Stat()
	if err != nil {
		t.Fatalf("fstat %s through mount: %v", rel, err)
	}
	if finfo.Size() != int64(len(want)) {
		t.Fatalf("fstat size for %s = %d, want %d", rel, finfo.Size(), len(want))
	}
}

type localFileClient struct{}

var _ remote.FileClient = localFileClient{}

func (localFileClient) Open(p string) (remote.RemoteFile, error) { return os.Open(p) }
func (localFileClient) Create(p string) (remote.RemoteWriteFile, error) {
	return os.Create(p)
}
func (localFileClient) OpenFile(p string, flags int, mode os.FileMode) (remote.RemoteWriteFile, error) {
	return os.OpenFile(p, flags, mode)
}
func (localFileClient) Stat(p string) (os.FileInfo, error)     { return os.Stat(p) }
func (localFileClient) Lstat(p string) (os.FileInfo, error)    { return os.Lstat(p) }
func (localFileClient) Readlink(p string) (string, error)      { return os.Readlink(p) }
func (localFileClient) Mkdir(p string, mode os.FileMode) error { return os.Mkdir(p, mode) }
func (localFileClient) MkdirAll(p string, mode os.FileMode) error {
	return os.MkdirAll(p, mode)
}
func (localFileClient) Remove(p string) error                  { return os.Remove(p) }
func (localFileClient) Rename(oldpath, newpath string) error   { return os.Rename(oldpath, newpath) }
func (localFileClient) Symlink(target, linkpath string) error  { return os.Symlink(target, linkpath) }
func (localFileClient) Link(oldpath, newpath string) error     { return os.Link(oldpath, newpath) }
func (localFileClient) Chmod(p string, mode os.FileMode) error { return os.Chmod(p, mode) }
func (localFileClient) Chown(p string, uid, gid int) error     { return os.Chown(p, uid, gid) }
func (localFileClient) Chtimes(p string, atime, mtime time.Time) error {
	return os.Chtimes(p, atime, mtime)
}
func (localFileClient) Truncate(p string, size int64) error { return os.Truncate(p, size) }
func (localFileClient) Getwd() (string, error)              { return os.Getwd() }

func (localFileClient) ReadDir(p string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (localFileClient) Statfs(p string) (*remote.Statfs, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(p, &st); err != nil {
		return nil, err
	}
	return &remote.Statfs{
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Bsize:   uint32(st.Bsize),
		Frsize:  uint32(st.Frsize),
		NameLen: uint32(st.Namelen),
	}, nil
}

func requireFuseDevice(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("/dev/fuse unavailable: %v", err)
	}
}

func isFuseUnavailable(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.EPERM)
}
