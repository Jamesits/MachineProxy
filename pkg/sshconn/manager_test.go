package sshconn

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerReconnectsAfterInitialFailure(t *testing.T) {
	var attempts atomic.Int32
	mgr := NewManager(Options{
		ReconnectInterval: 20 * time.Millisecond,
		KeepAliveInterval: 0,
		Dial: func(context.Context) (Conn, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("dial failed")
			}
			return &fakeConn{}, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if err := waitFor(2*time.Second, 20*time.Millisecond, func() bool { return mgr.IsConnected() }); err != nil {
		t.Fatalf("manager did not connect: %v", err)
	}
	if attempts.Load() < 2 {
		t.Fatalf("expected reconnect attempt after failure, got %d attempts", attempts.Load())
	}
}

func waitFor(timeout, interval time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(interval)
	}
	return errors.New("timeout")
}

type fakeConn struct{}

func (f *fakeConn) NewSFTP(context.Context) (SFTPClient, error) { return nil, nil }
func (f *fakeConn) NewSession(context.Context) (Session, error) { return nil, nil }
func (f *fakeConn) SendKeepAlive(context.Context) error         { return nil }
func (f *fakeConn) Close() error                                { return nil }
