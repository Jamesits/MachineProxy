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
//
// A bare string with no scheme prefix defaults to defaultType passed to
// ParseDestination — typically the value of --backend.
type Destination struct {
	Type Type
	// User is the SSH user, when present. Empty for Docker and for SSH
	// destinations that do not carry an explicit user.
	User string
	// Host is the SSH hostname/IP for type=ssh, or the container
	// name/ID for type=docker.
	Host string
	// Port is the SSH port, zero when not specified. Unused for docker.
	Port int
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
		default:
			return Destination{}, fmt.Errorf("unknown destination scheme %q (want ssh:// or docker://)", scheme)
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
