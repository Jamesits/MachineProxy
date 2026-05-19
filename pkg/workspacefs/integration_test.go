//go:build backend_ssh

package workspacefs

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/jamesits/machineproxy/pkg/remote"
)

func testFS(t *testing.T) *FileSystem {
	t.Helper()

	addr := os.Getenv("MPROXY_TEST_SSH_ADDR")
	keyPath := os.Getenv("MPROXY_TEST_SSH_KEY")
	if addr == "" || keyPath == "" {
		t.Skip("set MPROXY_TEST_SSH_ADDR and MPROXY_TEST_SSH_KEY to run integration tests")
	}
	user := os.Getenv("MPROXY_TEST_SSH_USER")
	if user == "" {
		user = "root"
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}

	sshClient, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = sshClient.Close() })

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	t.Cleanup(func() { _ = sftpClient.Close() })

	testDir := fmt.Sprintf("/tmp/mproxy_test_%d", rand.Int64())
	if err := sftpClient.Mkdir(testDir); err != nil {
		t.Fatalf("mkdir test dir: %v", err)
	}
	t.Cleanup(func() {
		cleanupDir(sftpClient, testDir)
	})

	return New(sftpClientAdapter{c: sftpClient}, testDir, nil)
}

// sftpClientAdapter is the test-local adapter that promotes a real
// *sftp.Client to remote.FileClient. The production code path uses the
// equivalent wrapper in pkg/remote/ssh.
type sftpClientAdapter struct{ c *sftp.Client }

func (a sftpClientAdapter) Open(p string) (remote.RemoteFile, error) { return a.c.Open(p) }
func (a sftpClientAdapter) Create(p string) (remote.RemoteWriteFile, error) {
	return a.c.Create(p)
}
func (a sftpClientAdapter) OpenFile(p string, flags int, mode os.FileMode) (remote.RemoteWriteFile, error) {
	return a.c.OpenFile(p, flags)
}
func (a sftpClientAdapter) Stat(p string) (os.FileInfo, error)        { return a.c.Stat(p) }
func (a sftpClientAdapter) Lstat(p string) (os.FileInfo, error)       { return a.c.Lstat(p) }
func (a sftpClientAdapter) ReadDir(p string) ([]os.FileInfo, error)   { return a.c.ReadDir(p) }
func (a sftpClientAdapter) Readlink(p string) (string, error)         { return a.c.ReadLink(p) }
func (a sftpClientAdapter) Mkdir(p string, mode os.FileMode) error    { return a.c.Mkdir(p) }
func (a sftpClientAdapter) MkdirAll(p string, mode os.FileMode) error { return a.c.MkdirAll(p) }
func (a sftpClientAdapter) Remove(p string) error                     { return a.c.Remove(p) }
func (a sftpClientAdapter) Rename(old, new string) error              { return a.c.Rename(old, new) }
func (a sftpClientAdapter) Symlink(target, linkpath string) error {
	return a.c.Symlink(target, linkpath)
}
func (a sftpClientAdapter) Link(old, new string) error             { return a.c.Link(old, new) }
func (a sftpClientAdapter) Chmod(p string, mode os.FileMode) error { return a.c.Chmod(p, mode) }
func (a sftpClientAdapter) Chown(p string, uid, gid int) error     { return a.c.Chown(p, uid, gid) }
func (a sftpClientAdapter) Chtimes(p string, atime, mtime time.Time) error {
	return a.c.Chtimes(p, atime, mtime)
}
func (a sftpClientAdapter) Truncate(p string, size int64) error { return a.c.Truncate(p, size) }
func (a sftpClientAdapter) Statfs(p string) (*remote.Statfs, error) {
	st, err := a.c.StatVFS(p)
	if err != nil {
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
		NameLen: uint32(st.Namemax),
	}, nil
}
func (a sftpClientAdapter) Getwd() (string, error) { return a.c.Getwd() }

func cleanupDir(c *sftp.Client, dir string) {
	entries, _ := c.ReadDir(dir)
	for _, e := range entries {
		p := dir + "/" + e.Name()
		if e.IsDir() {
			cleanupDir(c, p)
		} else {
			_ = c.Remove(p)
		}
	}
	_ = c.Remove(dir)
}

func TestIntegrationCreateAndReadFile(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	errno := fs.CreateFile(ctx, "hello.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644)
	if errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}

	n, errno := fs.WriteFile(ctx, "hello.txt", []byte("hello world"), 0)
	if errno != 0 {
		t.Fatalf("WriteFile: errno %v", errno)
	}
	if n != 11 {
		t.Fatalf("WriteFile: wrote %d bytes, want 11", n)
	}

	data, errno := fs.ReadFile(ctx, "hello.txt", 0, 128)
	if errno != 0 {
		t.Fatalf("ReadFile: errno %v", errno)
	}
	if string(data) != "hello world" {
		t.Fatalf("ReadFile = %q, want %q", string(data), "hello world")
	}
}

func TestIntegrationReadFileAtOffset(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "offset.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}
	if _, errno := fs.WriteFile(ctx, "offset.txt", []byte("abcdefghij"), 0); errno != 0 {
		t.Fatalf("WriteFile: errno %v", errno)
	}

	data, errno := fs.ReadFile(ctx, "offset.txt", 5, 5)
	if errno != 0 {
		t.Fatalf("ReadFile: errno %v", errno)
	}
	if string(data) != "fghij" {
		t.Fatalf("ReadFile = %q, want %q", string(data), "fghij")
	}
}

func TestIntegrationStat(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "statme.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}
	if _, errno := fs.WriteFile(ctx, "statme.txt", []byte("12345"), 0); errno != 0 {
		t.Fatalf("WriteFile: errno %v", errno)
	}

	info, errno := fs.Stat(ctx, "statme.txt")
	if errno != 0 {
		t.Fatalf("Stat: errno %v", errno)
	}
	if info.Name() != "statme.txt" {
		t.Fatalf("Name = %q, want %q", info.Name(), "statme.txt")
	}
	if info.Size() != 5 {
		t.Fatalf("Size = %d, want 5", info.Size())
	}
	if info.IsDir() {
		t.Fatal("expected file, not directory")
	}
}

func TestIntegrationStatMissing(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	_, errno := fs.Stat(ctx, "nonexistent.txt")
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT, got %v", errno)
	}
}

func TestIntegrationMkDirAndReadDir(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	errno := fs.MkDir(ctx, "subdir", 0o755)
	if errno != 0 {
		t.Fatalf("MkDir: errno %v", errno)
	}

	info, errno := fs.Stat(ctx, "subdir")
	if errno != 0 {
		t.Fatalf("Stat subdir: errno %v", errno)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}

	if errno := fs.CreateFile(ctx, "subdir/inner.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}

	entries, errno := fs.ReadDir(ctx, "")
	if errno != 0 {
		t.Fatalf("ReadDir: errno %v", errno)
	}

	found := false
	for _, e := range entries {
		if e.Name() == "subdir" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ReadDir did not include 'subdir'")
	}
}

func TestIntegrationRename(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "old.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}
	if _, errno := fs.WriteFile(ctx, "old.txt", []byte("content"), 0); errno != 0 {
		t.Fatalf("WriteFile: errno %v", errno)
	}

	errno := fs.Rename(ctx, "old.txt", "new.txt")
	if errno != 0 {
		t.Fatalf("Rename: errno %v", errno)
	}

	_, errno = fs.Stat(ctx, "old.txt")
	if errno != syscall.ENOENT {
		t.Fatalf("expected old file ENOENT, got %v", errno)
	}

	data, errno := fs.ReadFile(ctx, "new.txt", 0, 128)
	if errno != 0 {
		t.Fatalf("ReadFile new: errno %v", errno)
	}
	if string(data) != "content" {
		t.Fatalf("content = %q, want %q", string(data), "content")
	}
}

func TestIntegrationUnlink(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "delete_me.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}

	errno := fs.Unlink(ctx, "delete_me.txt")
	if errno != 0 {
		t.Fatalf("Unlink: errno %v", errno)
	}

	_, errno = fs.Stat(ctx, "delete_me.txt")
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT after Unlink, got %v", errno)
	}
}

func TestIntegrationRmdir(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.MkDir(ctx, "empty_dir", 0o755); errno != 0 {
		t.Fatalf("MkDir: errno %v", errno)
	}

	errno := fs.Rmdir(ctx, "empty_dir")
	if errno != 0 {
		t.Fatalf("Rmdir: errno %v", errno)
	}

	_, errno = fs.Stat(ctx, "empty_dir")
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT after Rmdir, got %v", errno)
	}
}

func TestIntegrationChmod(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "chmod_me.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}

	errno := fs.Chmod(ctx, "chmod_me.txt", 0o755)
	if errno != 0 {
		t.Fatalf("Chmod: errno %v", errno)
	}

	info, errno := fs.Stat(ctx, "chmod_me.txt")
	if errno != 0 {
		t.Fatalf("Stat: errno %v", errno)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %o, want 755", info.Mode().Perm())
	}
}

func TestIntegrationTruncate(t *testing.T) {
	fs := testFS(t)
	ctx := context.Background()

	if errno := fs.CreateFile(ctx, "trunc.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644); errno != 0 {
		t.Fatalf("CreateFile: errno %v", errno)
	}
	if _, errno := fs.WriteFile(ctx, "trunc.txt", []byte("long content here"), 0); errno != 0 {
		t.Fatalf("WriteFile: errno %v", errno)
	}

	errno := fs.Truncate(ctx, "trunc.txt", 4)
	if errno != 0 {
		t.Fatalf("Truncate: errno %v", errno)
	}

	info, errno := fs.Stat(ctx, "trunc.txt")
	if errno != 0 {
		t.Fatalf("Stat: errno %v", errno)
	}
	if info.Size() != 4 {
		t.Fatalf("size after truncate = %d, want 4", info.Size())
	}

	data, errno := fs.ReadFile(ctx, "trunc.txt", 0, 128)
	if errno != 0 {
		t.Fatalf("ReadFile: errno %v", errno)
	}
	if string(data) != "long" {
		t.Fatalf("data = %q, want %q", string(data), "long")
	}
}
