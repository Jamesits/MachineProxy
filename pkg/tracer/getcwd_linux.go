//go:build linux

package tracer

import "golang.org/x/sys/unix"

// SysGetcwd returns the getcwd(2) syscall number for the build target. Unlike
// execve/execveat (whose numbers vary widely and are defined per-arch), the
// getcwd number is taken from golang.org/x/sys/unix, which provides it for
// every Linux GOARCH.
func SysGetcwd() uint32 { return uint32(unix.SYS_GETCWD) }
