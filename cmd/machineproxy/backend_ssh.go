//go:build backend_ssh

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/remote"
	remotessh "github.com/jamesits/machineproxy/pkg/remote/ssh"
)

func init() {
	registerBackend("ssh", func(_ context.Context, cfg *config.Config, log *slog.Logger) (remote.Backend, error) {
		return remotessh.New(remotessh.Config{
			Host:           cfg.Remote.SSH.Host,
			User:           cfg.Remote.SSH.User,
			Port:           cfg.Remote.SSH.Port,
			ConnectTimeout: 10 * time.Second,
			Log:            log.With("component", "ssh"),
		})
	})
}
