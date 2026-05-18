//go:build backend_docker

package docker

import (
	"strings"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// normalizeOS converts a Docker `OSType` string into Go's GOOS naming.
// Returns "" for values that have no Go equivalent.
func normalizeOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "linux":
		return "linux"
	case "darwin", "macos", "osx":
		return "darwin"
	case "windows":
		return "windows"
	case "freebsd":
		return "freebsd"
	case "openbsd":
		return "openbsd"
	case "netbsd":
		return "netbsd"
	case "dragonfly":
		return "dragonfly"
	case "solaris", "sunos", "illumos":
		return "solaris"
	}
	return ""
}

// normalizeArch converts a Docker `Architecture` string (which is
// effectively `uname -m`) into Go's GOARCH naming. The boolean return
// signals whether the input was recognised. When the input names an
// ARM variant the variant is also returned, mirroring how container
// runtimes describe the same CPU.
func normalizeArch(s string) (arch, variant string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x86_64", "amd64":
		return "amd64", "", true
	case "aarch64", "arm64":
		return "arm64", "", true
	case "armv8l", "armv8":
		return "arm64", "v8", true
	case "armv7l", "armv7", "armhf":
		return "arm", "v7", true
	case "armv6l", "armv6":
		return "arm", "v6", true
	case "armv5l", "armv5", "armel":
		return "arm", "v5", true
	case "arm":
		return "arm", "", true
	case "i386", "i486", "i586", "i686", "x86":
		return "386", "", true
	case "ppc64":
		return "ppc64", "", true
	case "ppc64le":
		return "ppc64le", "", true
	case "s390x":
		return "s390x", "", true
	case "riscv64":
		return "riscv64", "", true
	case "mips":
		return "mips", "", true
	case "mipsle", "mipsel":
		return "mipsle", "", true
	case "mips64":
		return "mips64", "", true
	case "mips64le", "mips64el":
		return "mips64le", "", true
	case "loongarch64", "loong64":
		return "loong64", "", true
	}
	return "", "", false
}

// platformFromInfo composes a PlatformInfo from a docker `info`
// response's OSType / Architecture fields.
func platformFromInfo(osType, arch string) remote.PlatformInfo {
	info := remote.PlatformInfo{OS: normalizeOS(osType)}
	if a, v, ok := normalizeArch(arch); ok {
		info.Arch = a
		info.Variant = v
	}
	return info
}
