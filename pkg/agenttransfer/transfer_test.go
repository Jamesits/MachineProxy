package agenttransfer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamesits/machineproxy/pkg/remote"
)

func TestEnsureMemoisesAcrossCalls(t *testing.T) {
	be := &fakeBackend{remotePath: "/tmp/mproxy-agent"}
	tr := New(be, "/local", "/tmp/mproxy-agent", slog.New(slog.NewTextHandler(io.Discard, nil)))

	for i := 0; i < 3; i++ {
		got, err := tr.Ensure(context.Background())
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if got != "/tmp/mproxy-agent" {
			t.Fatalf("got %q, want /tmp/mproxy-agent", got)
		}
	}
	if calls := be.calls.Load(); calls != 1 {
		t.Fatalf("UploadAgent called %d times, want 1", calls)
	}
}

func TestEnsureSurfacesUploadError(t *testing.T) {
	be := &fakeBackend{err: errors.New("boom")}
	tr := New(be, "/local", "/tmp/mproxy-agent", slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, err := tr.Ensure(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// fakeBackend is a remote.Backend that only implements UploadAgent.
type fakeBackend struct {
	remotePath string
	err        error
	calls      atomic.Int32
}

func (f *fakeBackend) UploadAgent(_ context.Context, _, _ string, _ os.FileMode) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	return f.remotePath, nil
}

func (f *fakeBackend) Type() remote.Type           { return remote.TypeSSH }
func (f *fakeBackend) Addr() string                { return "" }
func (f *fakeBackend) User() string                { return "" }
func (f *fakeBackend) Start(context.Context) error { return nil }
func (f *fakeBackend) Close() error                { return nil }
func (f *fakeBackend) IsConnected() bool           { return true }
func (f *fakeBackend) LastErr() error              { return nil }
func (f *fakeBackend) NewSession(context.Context) (remote.Session, error) {
	return nil, errors.New("not used")
}
func (f *fakeBackend) Files(context.Context) (remote.FileClient, error) {
	return nil, errors.New("not used")
}
func (f *fakeBackend) KeepAliveInterval() time.Duration    { return 0 }
func (f *fakeBackend) SendKeepAlive(context.Context) error { return nil }
func (f *fakeBackend) DetectPlatform(context.Context) (remote.PlatformInfo, error) {
	return remote.PlatformInfo{}, errors.New("not used")
}

var _ remote.Backend = (*fakeBackend)(nil)
