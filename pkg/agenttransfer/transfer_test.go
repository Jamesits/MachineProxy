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
