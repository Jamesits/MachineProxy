//go:build backend_docker

package main

import (
	"log/slog"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/remote"
	remotedocker "github.com/jamesits/machineproxy/pkg/remote/docker"
)

func init() {
	registerBackend("docker", func(cfg *config.Config, log *slog.Logger) (remote.Backend, error) {
		return remotedocker.New(remotedocker.Config{
			Container: cfg.Remote.Docker.Container,
			Host:      cfg.Remote.Docker.Host,
			Log:       log.With("component", "docker"),
		})
	})
}
