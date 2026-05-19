//go:build backend_docker

package docker

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

const (
	envDockerHost      = "DOCKER_HOST"
	envDockerConfig    = "DOCKER_CONFIG"
	envOverrideContext = "DOCKER_CONTEXT"
	envXDGRuntime      = "XDG_RUNTIME_DIR"
	defaultContextName = "default"
)

// resolveClientOpts returns docker client options that honour the full
// Docker context resolution chain:
//
//  1. cfgHost (machineproxy config) – highest priority
//  2. DOCKER_HOST env – handled by client.FromEnv
//  3. DOCKER_CONTEXT env – context store lookup
//  4. currentContext in ~/.docker/config.json – context store lookup
//  5. Rootless socket at $XDG_RUNTIME_DIR/docker.sock
//  6. client.FromEnv default (/var/run/docker.sock on Linux)
//
// TLS contexts are fully supported. SSH-scheme contexts are passed through
// as-is (docker/docker/client handles ssh:// natively via its dialer).
func resolveClientOpts(cfgHost string) ([]client.Opt, error) {
	base := []client.Opt{client.FromEnv}

	// Explicit host in machineproxy config overrides everything.
	if cfgHost != "" {
		return append(base, client.WithHost(cfgHost)), nil
	}

	// DOCKER_HOST is set → client.FromEnv already handles it.
	if os.Getenv(envDockerHost) != "" {
		return base, nil
	}

	// Determine which context to use.
	contextName := os.Getenv(envOverrideContext)
	if contextName == "" {
		contextName = currentContextFromConfig()
	}

	if contextName == "" || contextName == defaultContextName {
		// For the default context, check for a rootless Docker socket before
		// falling back to the system socket (/var/run/docker.sock).
		if sock := rootlessSocket(); sock != "" {
			return append(base, client.WithHost("unix://"+sock)), nil
		}
		return base, nil
	}

	// Load the named context from the store.
	ep, err := endpointFromContextStore(contextName)
	if err != nil {
		return nil, fmt.Errorf("docker context %q: %w", contextName, err)
	}

	opts := base
	if ep.host != "" {
		opts = append(opts, client.WithHost(ep.host))
	}
	if tlsOpt, err := ep.tlsClientOpt(contextName); err != nil {
		return nil, err
	} else if tlsOpt != nil {
		opts = append(opts, tlsOpt)
	}
	return opts, nil
}

// dockerConfigFile is a minimal view of ~/.docker/config.json.
type dockerConfigFile struct {
	CurrentContext string `json:"currentContext"`
}

// contextEndpointMeta mirrors the JSON layout of the "docker" endpoint in a
// context's meta.json. Field names match Docker's JSON serialisation (capitalised).
type contextEndpointMeta struct {
	Host          string `json:"Host"`
	SkipTLSVerify bool   `json:"SkipTLSVerify"`
}

type contextFileMeta struct {
	Endpoints map[string]json.RawMessage `json:"Endpoints"`
}

// contextEndpoint is the parsed result of a context store lookup.
type contextEndpoint struct {
	host          string
	skipTLSVerify bool
	tlsDir        string // path to the TLS directory for this context+endpoint
}

func currentContextFromConfig() string {
	data, err := os.ReadFile(filepath.Join(dockerConfigDir(), "config.json"))
	if err != nil {
		return ""
	}
	var cfg dockerConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.CurrentContext
}

func dockerConfigDir() string {
	if d := os.Getenv(envDockerConfig); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".docker")
}

// contextDirName returns the directory name used by the Docker context store
// for a given context name. The store hashes the name with SHA-256.
func contextDirName(name string) string {
	h := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%x", h)
}

func endpointFromContextStore(name string) (*contextEndpoint, error) {
	configDir := dockerConfigDir()
	hash := contextDirName(name)

	metaPath := filepath.Join(configDir, "contexts", "meta", hash, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("context not found")
		}
		return nil, err
	}

	var meta contextFileMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parsing context metadata: %w", err)
	}

	raw, ok := meta.Endpoints["docker"]
	if !ok {
		return nil, fmt.Errorf("no docker endpoint in context")
	}

	var ep contextEndpointMeta
	if err := json.Unmarshal(raw, &ep); err != nil {
		return nil, fmt.Errorf("parsing docker endpoint: %w", err)
	}

	// TLS data lives under contexts/tls/<hash>/docker/
	tlsDir := filepath.Join(configDir, "contexts", "tls", hash, "docker")

	return &contextEndpoint{
		host:          ep.Host,
		skipTLSVerify: ep.SkipTLSVerify,
		tlsDir:        tlsDir,
	}, nil
}

// tlsClientOpt returns a client.Opt that configures TLS for TCP endpoints,
// or nil when TLS is not needed (unix/npipe sockets).
func (ep *contextEndpoint) tlsClientOpt(contextName string) (client.Opt, error) {
	// Unix/named-pipe/fd sockets don't need TLS.
	if isLocalSocket(ep.host) {
		return nil, nil
	}

	caPath := filepath.Join(ep.tlsDir, "ca.pem")
	certPath := filepath.Join(ep.tlsDir, "cert.pem")
	keyPath := filepath.Join(ep.tlsDir, "key.pem")
	hasCerts := fileExists(certPath) && fileExists(keyPath)

	if !ep.skipTLSVerify && !hasCerts && !fileExists(caPath) {
		// No TLS material configured; leave transport as-is.
		return nil, nil
	}

	if !ep.skipTLSVerify {
		// Standard verified TLS.
		ca, cert, key := "", "", ""
		if fileExists(caPath) {
			ca = caPath
		}
		if hasCerts {
			cert = certPath
			key = keyPath
		}
		return client.WithTLSClientConfig(ca, cert, key), nil
	}

	// SkipTLSVerify requires a custom HTTP client.
	return client.WithHTTPClient(&http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			DialContext: (&net.Dialer{
				KeepAlive: 30 * time.Second,
				Timeout:   30 * time.Second,
			}).DialContext,
		},
		CheckRedirect: client.CheckRedirect,
	}), nil
}

func isLocalSocket(host string) bool {
	return host == "" ||
		strings.HasPrefix(host, "unix://") ||
		strings.HasPrefix(host, "npipe://") ||
		strings.HasPrefix(host, "fd:")
}

// rootlessSocket returns the path (without the unix:// prefix) to the
// rootless Docker socket if it exists, otherwise "".
func rootlessSocket() string {
	xdg := os.Getenv(envXDGRuntime)
	if xdg == "" {
		return ""
	}
	sock := filepath.Join(xdg, "docker.sock")
	if fileExists(sock) {
		return sock
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
