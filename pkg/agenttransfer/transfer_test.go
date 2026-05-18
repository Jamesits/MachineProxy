package agenttransfer

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDoesNotTrustHashMarkerWhenRemoteBinaryDiffers(t *testing.T) {
	local := filepath.Join(t.TempDir(), "mproxy-agent")
	if err := os.WriteFile(local, []byte("trusted-agent"), 0o755); err != nil {
		t.Fatalf("write local agent: %v", err)
	}
	hash, err := hashFile(local)
	if err != nil {
		t.Fatalf("hash local agent: %v", err)
	}

	remote := newFakeRemoteFiles()
	remote.files["/tmp/mproxy-agent"] = []byte("backdoor-agent")
	remote.files["/tmp/mproxy-agent.sha256"] = []byte(hash)

	transferer := newWithRemoteClient(remote, local, "/tmp/mproxy-agent", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := transferer.Ensure(); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if got := string(remote.files["/tmp/mproxy-agent"]); got != "trusted-agent" {
		t.Fatalf("remote agent = %q, want uploaded trusted agent", got)
	}
}

func TestEnsureExpandsTildeAgainstRemoteHome(t *testing.T) {
	local := filepath.Join(t.TempDir(), "mproxy-agent")
	if err := os.WriteFile(local, []byte("agent-bytes"), 0o755); err != nil {
		t.Fatalf("write local agent: %v", err)
	}

	remote := newFakeRemoteFiles()
	transferer := newWithRemoteClient(remote, local, "~/.cache/machineproxy/mproxy-agent",
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	got, err := transferer.Ensure()
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	want := "/home/fake/.cache/machineproxy/mproxy-agent"
	if got != want {
		t.Fatalf("returned remote path = %q, want %q", got, want)
	}
	if _, ok := remote.files[want]; !ok {
		t.Fatalf("agent not uploaded to expanded path %q (have %v)", want, remote.files)
	}
}

func TestExpandRemoteHomeLeavesAbsolutePathsAlone(t *testing.T) {
	remote := newFakeRemoteFiles()
	got, err := expandRemoteHome(remote, "/tmp/mproxy-agent")
	if err != nil {
		t.Fatalf("expandRemoteHome: %v", err)
	}
	if got != "/tmp/mproxy-agent" {
		t.Fatalf("got %q, want unchanged", got)
	}
}

type fakeRemoteFiles struct {
	files map[string][]byte
	modes map[string]os.FileMode
}

func newFakeRemoteFiles() *fakeRemoteFiles {
	return &fakeRemoteFiles{files: make(map[string][]byte), modes: make(map[string]os.FileMode)}
}

func (f *fakeRemoteFiles) Open(path string) (io.ReadCloser, error) {
	data, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (f *fakeRemoteFiles) OpenFile(path string, _ int) (io.WriteCloser, error) {
	return &fakeWriteFile{remote: f, path: path}, nil
}

func (f *fakeRemoteFiles) Chmod(path string, mode os.FileMode) error {
	if _, ok := f.files[path]; !ok {
		return os.ErrNotExist
	}
	f.modes[path] = mode
	return nil
}

func (f *fakeRemoteFiles) MkdirAll(string) error { return nil }

func (f *fakeRemoteFiles) Getwd() (string, error) { return "/home/fake", nil }

type fakeWriteFile struct {
	remote *fakeRemoteFiles
	path   string
	buf    bytes.Buffer
}

func (f *fakeWriteFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *fakeWriteFile) Close() error {
	f.remote.files[f.path] = append([]byte(nil), f.buf.Bytes()...)
	return nil
}
