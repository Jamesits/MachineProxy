package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateBrokerSocketPathUsesPrivateDirectory(t *testing.T) {
	socketPath, err := createBrokerSocketPath()
	if err != nil {
		t.Fatalf("create broker socket path: %v", err)
	}

	dir := filepath.Dir(socketPath)
	defer func() { _ = os.RemoveAll(dir) }()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("socket dir mode = %o, want 700", got)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket path exists before listen or unexpected error: %v", err)
	}
}
