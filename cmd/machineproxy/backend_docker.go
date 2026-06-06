//go:build backend_docker

package main

import (
	"context"
	"log/slog"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/remote"
	remotedocker "github.com/jamesits/machineproxy/pkg/remote/docker"
)

func init() {
	registerBackend("docker", func(ctx context.Context, cfg *config.Config, log *slog.Logger) (remote.Backend, error) {
		return remotedocker.New(ctx, remotedocker.Config{
			Container:     cfg.Remote.Docker.Container,
			Host:          cfg.Remote.Docker.Host,
			Bind:          cfg.Remote.Bind,
			BindInterface: cfg.Remote.BindInterface,
			Log:           log.With("component", "docker"),
		})
	})
	registerBackend("compose", func(ctx context.Context, cfg *config.Config, log *slog.Logger) (remote.Backend, error) {
		return remotedocker.NewFromCompose(ctx, remotedocker.Config{
			Host:          cfg.Remote.Compose.Host,
			Bind:          cfg.Remote.Bind,
			BindInterface: cfg.Remote.BindInterface,
			Log:           log.With("component", "compose"),
		}, cfg.Remote.Compose.Project, cfg.Remote.Compose.Service, cfg.Remote.Compose.Sequence)
	})
}
