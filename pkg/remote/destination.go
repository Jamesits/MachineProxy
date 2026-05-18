package remote

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Destination identifies a parsed target on the command line.
//
// SSH form:     ssh://[user@]host[:port]   or bare [user@]host
// Docker form:  docker://container_name_or_id
// Compose form: compose://project/service[/sequence]
//
//	project may be "." to auto-detect from the working directory.
//
// A bare string with no scheme prefix defaults to defaultType passed to
// ParseDestination — typically the value of --backend.
type Destination struct {
	Type Type
	// User is the SSH user, when present. Empty for Docker/Compose and for
	// SSH destinations that do not carry an explicit user.
	User string
	// Host is the SSH hostname/IP for type=ssh, the container name/ID for
	// type=docker, or the Compose project name for type=compose ("." = current).
	Host string
	// Port is the SSH port, zero when not specified. Unused for docker/compose.
	Port int
	// Service is the Docker Compose service name for type=compose.
	// Empty for ssh and docker.
	Service string
	// Sequence is the 1-based replica index for type=compose. Zero means
	// "unspecified — expect exactly one running match". Unused for ssh/docker.
	Sequence int
	// Raw is the original input string.
	Raw string
}

// ParseDestination parses a destination string. defaultType selects the
// backend when no scheme prefix is present. Returns an error when the
// scheme is unknown or the host portion is empty.
func ParseDestination(s string, defaultType Type) (Destination, error) {
	if s == "" {
		return Destination{}, errors.New("destination must not be empty")
	}

	t := defaultType
	rest := s
	if i := strings.Index(s, "://"); i >= 0 {
		scheme := s[:i]
		rest = s[i+3:]
		switch Type(scheme) {
		case TypeSSH:
			t = TypeSSH
		case TypeDocker:
			t = TypeDocker
		case TypeCompose:
			t = TypeCompose
		default:
			return Destination{}, fmt.Errorf("unknown destination scheme %q (want ssh://, docker://, or compose://)", scheme)
		}
	}
	if t == "" {
		t = TypeSSH
	}

	dst := Destination{Type: t, Raw: s}
	switch t {
	case TypeSSH:
		if err := parseSSHTarget(rest, &dst); err != nil {
			return Destination{}, err
		}
	case TypeDocker:
		if rest == "" {
			return Destination{}, errors.New("docker destination must include a container name or ID")
		}
		if strings.ContainsAny(rest, "/@") {
			return Destination{}, fmt.Errorf("docker destination %q contains an invalid character", rest)
		}
		dst.Host = rest
	case TypeCompose:
		if err := parseComposeTarget(rest, &dst); err != nil {
			return Destination{}, err
		}
	default:
		return Destination{}, fmt.Errorf("unknown backend type %q", t)
	}
	return dst, nil
}

// parseSSHTarget splits "[user@]host[:port]" into the components of dst.
// The last "@" separates the user from the host so IPv6 literals in
// "user@2001:db8::1" parse correctly. The trailing ":port" is only
// recognised when the host part is not a bracketed IPv6 literal that
// already contains its own port.
func parseSSHTarget(s string, dst *Destination) error {
	if s == "" {
		return errors.New("ssh destination must include a host")
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		dst.User = s[:i]
		s = s[i+1:]
	}
	// Bracketed IPv6: [::1]:22
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return fmt.Errorf("ssh destination %q has an unclosed '['", dst.Raw)
		}
		dst.Host = s[1:end]
		tail := s[end+1:]
		if tail == "" {
			return nil
		}
		if !strings.HasPrefix(tail, ":") {
			return fmt.Errorf("ssh destination %q has unexpected trailing %q", dst.Raw, tail)
		}
		return parsePort(tail[1:], dst)
	}
	// Bare IPv6 without brackets (no port allowed in this form).
	if strings.Count(s, ":") >= 2 {
		dst.Host = s
		return nil
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		dst.Host = s[:i]
		return parsePort(s[i+1:], dst)
	}
	dst.Host = s
	if dst.Host == "" {
		return errors.New("ssh destination must include a host")
	}
	return nil
}

// parseComposeTarget splits "project/service[/N]" into the components of dst.
// project may be "." to request auto-detection from the working directory.
// N is a 1-based replica sequence number; omitting it leaves Sequence at 0
// (meaning "expect exactly one running match").
func parseComposeTarget(s string, dst *Destination) error {
	if s == "" {
		return fmt.Errorf("compose destination %q must be project/service[/N] form", dst.Raw)
	}
	// Split off the project (everything before the first slash).
	i := strings.Index(s, "/")
	if i < 0 {
		return fmt.Errorf("compose destination %q must be project/service[/N] form", dst.Raw)
	}
	project := s[:i]
	rest := s[i+1:]
	if project == "" {
		return fmt.Errorf("compose destination %q: project name must not be empty", dst.Raw)
	}
	if strings.ContainsAny(project, "@:") {
		return fmt.Errorf("compose destination %q: project name contains invalid characters", dst.Raw)
	}

	// Split rest into service and optional sequence.
	service := rest
	if j := strings.Index(rest, "/"); j >= 0 {
		service = rest[:j]
		seq := rest[j+1:]
		n, err := strconv.Atoi(seq)
		if err != nil || n < 1 {
			return fmt.Errorf("compose destination %q: sequence %q must be a positive integer", dst.Raw, seq)
		}
		dst.Sequence = n
	}
	if service == "" {
		return fmt.Errorf("compose destination %q: service name must not be empty", dst.Raw)
	}
	if strings.ContainsAny(service, "@:/") {
		return fmt.Errorf("compose destination %q: service name contains invalid characters", dst.Raw)
	}
	dst.Host = project
	dst.Service = service
	return nil
}

func parsePort(p string, dst *Destination) error {
	if p == "" {
		return fmt.Errorf("ssh destination %q has empty port", dst.Raw)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return fmt.Errorf("ssh destination %q has invalid port: %w", dst.Raw, err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("ssh destination %q port %d out of range", dst.Raw, port)
	}
	dst.Port = port
	return nil
}
