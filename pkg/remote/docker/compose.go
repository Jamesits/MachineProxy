//go:build backend_docker

package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"

	"github.com/jamesits/machineproxy/pkg/logging"
)

// composeFileNames is the search order Docker Compose uses when looking for
// a project file in the working directory.
var composeFileNames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yaml",
	"docker-compose.yml",
}

// ResolveCurrentProject determines the Compose project name for the given
// directory using the same precedence chain that Docker Compose itself uses:
//  1. COMPOSE_PROJECT_NAME environment variable
//  2. The "name:" field in a compose file found in dir
//  3. The directory name, normalised to [a-z0-9_-]
func ResolveCurrentProject(dir string) (string, error) {
	if name := os.Getenv("COMPOSE_PROJECT_NAME"); name != "" {
		return name, nil
	}
	for _, fname := range composeFileNames {
		data, err := os.ReadFile(filepath.Join(dir, fname))
		if err != nil {
			continue
		}
		var f struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(data, &f); err == nil && f.Name != "" {
			return f.Name, nil
		}
	}
	return normalizeProjectName(filepath.Base(dir)), nil
}

// normalizeProjectName lowercases s and strips characters that are not
// alphanumeric, hyphen, or underscore — matching the compose-spec loader.
func normalizeProjectName(s string) string {
	re := regexp.MustCompile(`[^a-z0-9_-]`)
	s = strings.ToLower(s)
	s = re.ReplaceAllString(s, "")
	return strings.TrimLeft(s, "_-")
}

// ResolveComposeService finds the container ID of a running container that
// belongs to the given Compose project and service. When sequence > 0 it
// selects a specific replica via the com.docker.compose.container-number
// label (1-based, matching Docker Compose convention). When sequence == 0
// and multiple containers match, it returns an error with a hint.
func ResolveComposeService(ctx context.Context, cli *client.Client, project, service string, sequence int) (string, error) {
	args := filters.NewArgs(
		filters.Arg("label", "com.docker.compose.project="+project),
		filters.Arg("label", "com.docker.compose.service="+service),
		filters.Arg("status", "running"),
	)
	if sequence > 0 {
		args.Add("label", fmt.Sprintf("com.docker.compose.container-number=%d", sequence))
	}

	containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: args})
	if err != nil {
		return "", fmt.Errorf("compose: list containers for %s/%s: %w", project, service, err)
	}
	if len(containers) == 0 {
		return "", fmt.Errorf("compose: no running container for %s/%s", project, service)
	}
	if len(containers) > 1 {
		return "", fmt.Errorf(
			"compose: %d running containers match %s/%s; use compose://%s/%s/<n> to select a specific replica",
			len(containers), project, service, project, service,
		)
	}
	return containers[0].ID, nil
}

// NewFromCompose creates a Docker backend after resolving the given Compose
// project/service to a container ID. Pass project="." to auto-detect from
// the current working directory. The same Docker client is reused for both
// resolution and backend operations, avoiding a double-dial.
func NewFromCompose(ctx context.Context, cfg Config, project, service string, sequence int) (*Backend, error) {
	if project == "." {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("compose: get working directory: %w", err)
		}
		project, err = ResolveCurrentProject(cwd)
		if err != nil {
			return nil, fmt.Errorf("compose: resolve current project: %w", err)
		}
	}
	if project == "" {
		return nil, errors.New("compose: project name must not be empty")
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	opts, err := resolveClientOpts(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("compose context resolution: %w", err)
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("compose docker client: %w", err)
	}

	containerID, err := ResolveComposeService(ctx, cli, project, service, sequence)
	if err != nil {
		_ = cli.Close()
		return nil, err
	}

	log.Log(ctx, logging.LevelTrace, "compose resolved container",
		"project", project,
		"service", service,
		"sequence", sequence,
		"container_id", containerID[:12],
	)

	cfg.Container = containerID
	return &Backend{cfg: cfg, cli: cli, log: log}, nil
}
