package pathstub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/remote"
)

type fakeOpener struct {
	files      map[string][]byte
	openErr    error
	lastOpened string
}

func (f *fakeOpener) Open(path string) (remote.RemoteFile, error) {
	f.lastOpened = path
	if f.openErr != nil {
		return nil, f.openErr
	}
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &fakeRemoteFile{data: data}, nil
}

type fakeRemoteFile struct {
	data []byte
	pos  int64
}

func (f *fakeRemoteFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
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

func TestFileSystemReadProxiesOpener(t *testing.T) {
	op := &fakeOpener{files: map[string][]byte{
		"/usr/bin/bash": []byte("ELFBASHELF"),
	}}
	fs := New(op, []agentproto.PathInfoEntry{
		{Name: "bash", RemotePath: "/usr/bin/bash", Mode: 0o755, Size: 10},
	}, nil)

	data, errno := fs.ReadFile(context.Background(), "bash", 0, 10)
	if errno != 0 {
		t.Fatalf("ReadFile errno = %v", errno)
	}
	if !bytes.Equal(data, []byte("ELFBASHELF")) {
		t.Fatalf("data = %q, want ELFBASHELF", string(data))
	}
	if op.lastOpened != "/usr/bin/bash" {
		t.Fatalf("opener saw %q, want /usr/bin/bash", op.lastOpened)
	}
}

func TestFileSystemReadUnknownReturnsENOENT(t *testing.T) {
	op := &fakeOpener{files: map[string][]byte{}}
	fs := New(op, nil, nil)

	_, errno := fs.ReadFile(context.Background(), "missing", 0, 10)
	if errno != syscall.ENOENT {
		t.Fatalf("errno = %v, want ENOENT", errno)
	}
}

func TestFileSystemReadOpenerErrorMapsToErrno(t *testing.T) {
	op := &fakeOpener{
		files:   map[string][]byte{"/x": nil},
		openErr: os.ErrPermission,
	}
	fs := New(op, []agentproto.PathInfoEntry{
		{Name: "x", RemotePath: "/x", Mode: 0o755},
	}, nil)

	_, errno := fs.ReadFile(context.Background(), "x", 0, 1)
	if errno != syscall.EACCES {
		t.Fatalf("errno = %v, want EACCES", errno)
	}

	op.openErr = errors.New("boom")
	_, errno = fs.ReadFile(context.Background(), "x", 0, 1)
	if errno != syscall.EIO {
		t.Fatalf("errno = %v, want EIO", errno)
	}
}

func TestFileSystemDedupsByName(t *testing.T) {
	fs := New(nil, []agentproto.PathInfoEntry{
		{Name: "a", RemotePath: "/usr/local/bin/a"},
		{Name: "a", RemotePath: "/usr/bin/a"}, // shadowed
		{Name: "b", RemotePath: "/usr/bin/b"},
	}, nil)

	names := fs.Names()
	if len(names) != 2 {
		t.Fatalf("len(names) = %d, want 2 (got %v)", len(names), names)
	}
	if names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v, want [a b]", names)
	}
	e, ok := fs.Lookup("a")
	if !ok {
		t.Fatal("a not found")
	}
	if e.RemotePath != "/usr/local/bin/a" {
		t.Fatalf("a.RemotePath = %q, want first occurrence", e.RemotePath)
	}
}
