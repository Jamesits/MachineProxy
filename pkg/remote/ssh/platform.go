//go:build backend_ssh

package ssh

import (
	"errors"
	"strings"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// parseServerVersion derives a PlatformInfo from an SSH server
// identification string of the form "SSH-2.0-<softwareversion> [comments]".
// Detection is best-effort and relies on common tags injected by OS
// distributions and ports trees. Architecture is essentially never
// announced in the banner, so this function only fills OS and
// returns an empty Arch — the caller is responsible for falling back
// to a local default.
//
// Returns an error when no platform hint is recognised.
func parseServerVersion(banner string) (remote.PlatformInfo, error) {
	banner = strings.TrimSpace(banner)
	if banner == "" {
		return remote.PlatformInfo{}, errors.New("ssh detect: empty server version")
	}
	// Lowercased view used for case-insensitive substring matches.
	lc := strings.ToLower(banner)

	var info remote.PlatformInfo
	switch {
	case strings.Contains(lc, "for_windows"),
		strings.Contains(lc, "for windows"),
		strings.Contains(lc, "windows"):
		info.OS = "windows"
	case strings.Contains(lc, "darwin"),
		strings.Contains(lc, "macos"):
		info.OS = "darwin"
	case strings.Contains(lc, "freebsd"):
		info.OS = "freebsd"
	case strings.Contains(lc, "openbsd"):
		info.OS = "openbsd"
	case strings.Contains(lc, "netbsd"):
		info.OS = "netbsd"
	case strings.Contains(lc, "dragonfly"):
		info.OS = "dragonfly"
	case strings.Contains(lc, "solaris"),
		strings.Contains(lc, "sunos"),
		strings.Contains(lc, "illumos"):
		info.OS = "solaris"
	case
		// Common Linux distribution tags appended after the OpenSSH
		// version. OpenSSH itself does not announce "linux", but
		// every major distro patches its build to include a
		// distribution suffix.
		strings.Contains(lc, "ubuntu"),
		strings.Contains(lc, "debian"),
		strings.Contains(lc, "raspbian"),
		strings.Contains(lc, "alpine"),
		strings.Contains(lc, "arch"),
		strings.Contains(lc, "fedora"),
		strings.Contains(lc, "centos"),
		strings.Contains(lc, "rhel"),
		strings.Contains(lc, "redhat"),
		strings.Contains(lc, "rocky"),
		strings.Contains(lc, "alma"),
		strings.Contains(lc, "opensuse"),
		strings.Contains(lc, "suse"),
		strings.Contains(lc, "gentoo"),
		strings.Contains(lc, "nixos"),
		strings.Contains(lc, "android"),
		strings.Contains(lc, "linux"),
		// Dropbear is overwhelmingly deployed on embedded Linux.
		strings.Contains(lc, "dropbear"):
		info.OS = "linux"
	}

	if info.OS == "" && info.Arch == "" {
		return remote.PlatformInfo{}, errors.New("ssh detect: no platform hint in server version " + banner)
	}
	return info, nil
}
