package workspacefs

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"syscall"
	"testing"
	"time"
)

func TestReadFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/hello.txt": []byte("hello"),
		},
	}

	fs := New(client, "/workspace", nil)
	data, errno := fs.ReadFile(context.Background(), "hello.txt", 0, 5)

	if errno != 0 {
		t.Fatalf("ReadFile() errno = %v", errno)
	}
	if string(data) != "hello" {
		t.Fatalf("ReadFile() = %q, want %q", string(data), "hello")
	}
	if client.lastOpened != "/workspace/hello.txt" {
		t.Fatalf("expected read from /workspace/hello.txt, got %q", client.lastOpened)
	}
}

func TestReadFileMissingReturnsENOENT(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	_, errno := fs.ReadFile(context.Background(), "missing.txt", 0, 128)
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT, got %v", errno)
	}
}

func TestWriteFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/out.txt": {},
		},
	}

	fs := New(client, "/workspace", nil)
	n, errno := fs.WriteFile(context.Background(), "out.txt", []byte("written"), 0)

	if errno != 0 {
		t.Fatalf("WriteFile() errno = %v", errno)
	}
	if n != 7 {
		t.Fatalf("WriteFile() = %d bytes, want 7", n)
	}
	if string(client.files["/workspace/out.txt"]) != "written" {
		t.Fatalf("file contents = %q, want %q", string(client.files["/workspace/out.txt"]), "written")
	}
}

func TestCreateFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.CreateFile(context.Background(), "new.txt")
	if errno != 0 {
		t.Fatalf("CreateFile() errno = %v", errno)
	}
	if _, ok := client.files["/workspace/new.txt"]; !ok {
		t.Fatal("expected file to be created")
	}
}

func TestMkDirDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{},
		dirs:  map[string]bool{},
	}
	fs := New(client, "/workspace", nil)

	errno := fs.MkDir(context.Background(), "subdir")
	if errno != 0 {
		t.Fatalf("MkDir() errno = %v", errno)
	}
	if !client.dirs["/workspace/subdir"] {
		t.Fatal("expected directory to be created")
	}
}

type fakeSFTPClient struct {
	files      map[string][]byte
	dirs       map[string]bool
	lastOpened string
}

func (f *fakeSFTPClient) Open(p string) (RemoteFile, error) {
	f.lastOpened = path.Clean(p)
	data, ok := f.files[f.lastOpened]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &fakeRemoteFile{data: data}, nil
}

func (f *fakeSFTPClient) Create(p string) (RemoteWriteFile, error) {
	clean := path.Clean(p)
	f.files[clean] = []byte{}
	return &fakeRemoteWriteFile{client: f, path: clean}, nil
}

func (f *fakeSFTPClient) OpenFile(p string, flags int) (RemoteWriteFile, error) {
	clean := path.Clean(p)
	if _, ok := f.files[clean]; !ok {
		return nil, os.ErrNotExist
	}
	return &fakeRemoteWriteFile{client: f, path: clean}, nil
}

func (f *fakeSFTPClient) Stat(string) (os.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeSFTPClient) ReadDir(string) ([]os.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeSFTPClient) Mkdir(p string) error {
	clean := path.Clean(p)
	if f.dirs == nil {
		f.dirs = map[string]bool{}
	}
	f.dirs[clean] = true
	return nil
}

func (f *fakeSFTPClient) Remove(p string) error {
	clean := path.Clean(p)
	delete(f.files, clean)
	delete(f.dirs, clean)
	return nil
}

func (f *fakeSFTPClient) Rename(old, new string) error {
	data, ok := f.files[path.Clean(old)]
	if !ok {
		return os.ErrNotExist
	}
	f.files[path.Clean(new)] = data
	delete(f.files, path.Clean(old))
	return nil
}

func (f *fakeSFTPClient) Chmod(string, os.FileMode) error { return nil }
func (f *fakeSFTPClient) Truncate(p string, size int64) error {
	clean := path.Clean(p)
	data, ok := f.files[clean]
	if !ok {
		return os.ErrNotExist
	}
	if int64(len(data)) > size {
		f.files[clean] = data[:size]
	}
	return nil
}

type fakeRemoteFile struct {
	data []byte
}

type fakeRemoteWriteFile struct {
	client *fakeSFTPClient
	path   string
}

func (f *fakeRemoteFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if int(off)+n >= len(f.data) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeRemoteFile) Close() error { return nil }

func (f *fakeRemoteWriteFile) WriteAt(p []byte, off int64) (int, error) {
	existing := f.client.files[f.path]
	needed := int(off) + len(p)
	if needed > len(existing) {
		grown := make([]byte, needed)
		copy(grown, existing)
		existing = grown
	}
	copy(existing[off:], p)
	f.client.files[f.path] = existing
	return len(p), nil
}

func (f *fakeRemoteWriteFile) Close() error { return nil }

type fakeFileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mod   time.Time
	isDir bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return f.mod }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }
