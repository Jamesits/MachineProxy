package sshconn

import (
	"context"
	"os"
	"testing"
	"time"
)

func testDialFunc(t *testing.T) func(context.Context) (Conn, error) {
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

	dial, err := NewDialFunc(DialConfig{
		Addr:           addr,
		User:           user,
		PrivateKeyPath: keyPath,
		Timeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialFunc: %v", err)
	}
	return dial
}

func TestIntegrationDial(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
}

func TestIntegrationNewSFTP(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	sftp, err := conn.NewSFTP(ctx)
	if err != nil {
		t.Fatalf("NewSFTP: %v", err)
	}
	defer sftp.Close()
}

func TestIntegrationNewSession(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	sess, err := conn.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
}

func TestIntegrationSendKeepAlive(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if err := conn.SendKeepAlive(ctx); err != nil {
		t.Fatalf("SendKeepAlive: %v", err)
	}
}

func TestIntegrationManagerStartAndConnect(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := NewManager(Options{
		Dial:              dial,
		ReconnectInterval: 500 * time.Millisecond,
		KeepAliveInterval: 0,
	})

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Close()

	if !mgr.IsConnected() {
		t.Fatal("expected manager to be connected after Start")
	}
	if mgr.LastErr() != nil {
		t.Fatalf("unexpected LastErr: %v", mgr.LastErr())
	}
	if mgr.SFTP() == nil {
		t.Fatal("expected SFTP client to be non-nil")
	}
	if mgr.CurrentConn() == nil {
		t.Fatal("expected CurrentConn to be non-nil")
	}
}

func TestIntegrationManagerKeepAlive(t *testing.T) {
	dial := testDialFunc(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mgr := NewManager(Options{
		Dial:              dial,
		ReconnectInterval: 1 * time.Second,
		KeepAliveInterval: 200 * time.Millisecond,
	})

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer mgr.Close()

	// Let a few keepalives fire
	time.Sleep(500 * time.Millisecond)

	if !mgr.IsConnected() {
		t.Fatal("expected manager to remain connected after keepalives")
	}
}
