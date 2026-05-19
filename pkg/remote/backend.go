// Package remote defines the backend-agnostic abstraction over the
// remote target that machineproxy talks to. Today this is implemented by
// pkg/remote/ssh (SSH+SFTP) and pkg/remote/docker (Docker exec+archive).
package remote

import (
	"context"
	"io"
	"os"
	"time"
)

// Type identifies one backend implementation.
type Type string

const (
	TypeSSH     Type = "ssh"
	TypeDocker  Type = "docker"
	TypeCompose Type = "compose"
)

// PlatformInfo describes the remote machine's OS, architecture, and
// (when meaningful) CPU variant. Field naming follows Go's GOOS/GOARCH
// convention so values can be plugged into agent-binary path
// resolution without further translation.
//
// Empty fields mean "not detected". Callers must treat each field
// independently; a backend may know the OS but not the architecture.
type PlatformInfo struct {
	OS      string // "linux", "darwin", "windows", "freebsd", ...
	Arch    string // "amd64", "arm64", "arm", "386", ...
	Variant string // optional, e.g. "v7" for armv7l
}

// Backend is a single connection to a remote command-execution +
// filesystem target. Implementations are expected to be safe for
// concurrent use after Start returns.
type Backend interface {
	// Type returns the implementation identifier (e.g. "ssh", "docker").
	Type() Type
	// Addr is a human-readable identifier of the target (host:port,
	// container name, etc.). Used in logs and recording metadata.
	Addr() string
	// User is the remote user identity, when applicable. May be empty
	// for backends like Docker where there is no notion of an SSH user.
	User() string
	// Start establishes the connection. May be a no-op for
	// backend-specific reasons. The context governs the dial and any
	// preflight checks; once Start returns the backend manages its
	// own connection lifecycle.
	Start(ctx context.Context) error
	// IsConnected reports whether the backend is currently usable.
	IsConnected() bool
	// LastErr returns the most recent connection error, or nil when the
	// last attempt succeeded.
	LastErr() error
	// KeepAliveInterval, when > 0, is the period at which the manager
	// should call SendKeepAlive to keep the connection alive.
	KeepAliveInterval() time.Duration
	// SendKeepAlive sends a backend-appropriate keepalive ping. Errors
	// returned here are treated as fatal to the connection.
	SendKeepAlive(ctx context.Context) error
	// NewSession creates a fresh command execution session. Implementations
	// must allow multiple concurrent sessions.
	NewSession(ctx context.Context) (Session, error)
	// Files returns a FileClient for file operations against the remote
	// (used by agent binary transfer and the FUSE workspace mount).
	// May lazily start a long-lived server-side worker.
	Files(ctx context.Context) (FileClient, error)
	// UploadAgent installs the mproxy-agent binary on the remote at the
	// given path. Backends that can self-host the agent via FileClient
	// (SSH+SFTP) may delegate; backends that need a bootstrap primitive
	// (Docker CopyToContainer) implement it directly.
	UploadAgent(ctx context.Context, localPath, remotePath string, mode os.FileMode) (resolvedRemote string, err error)
	// DetectPlatform queries the remote for its OS, architecture, and
	// optional CPU variant. Implementations may leave any field empty
	// when the backend cannot derive that piece of information; they
	// should return a non-nil error only when nothing useful could be
	// determined. Callers are expected to fall back to a sensible
	// default (typically the local GOOS/GOARCH) on error.
	DetectPlatform(ctx context.Context) (PlatformInfo, error)
	// Close terminates the connection and releases resources.
	Close() error
}

// Session is a single command execution channel. The shape mirrors
// golang.org/x/crypto/ssh.Session for back-compat with code that grew
// up around it.
type Session interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.Reader, error)
	StderrPipe() (io.Reader, error)
	Setenv(name, value string) error
	Start(cmd string) error
	Wait() error
	Close() error
}

// RemoteFile is the read-side handle returned by FileClient.Open. It
// matches the subset of *sftp.File / *os.File that workspacefs and
// pathstub rely on.
type RemoteFile interface {
	io.ReadCloser
	ReadAt(p []byte, off int64) (int, error)
}

// RemoteWriteFile is the write-side handle returned by FileClient.Create
// and FileClient.OpenFile.
type RemoteWriteFile interface {
	io.WriteCloser
	ReadAt(p []byte, off int64) (int, error)
	WriteAt(p []byte, off int64) (int, error)
	Stat() (os.FileInfo, error)
	Truncate(size int64) error
	// Sync flushes the file's writes to durable storage on the remote.
	// Backends whose underlying protocol cannot express fsync (e.g. an
	// SFTP server that lacks the fsync@openssh.com extension) should
	// return nil rather than an error so the FUSE layer can treat the
	// op as a successful no-op.
	Sync() error
}

// Statfs holds filesystem capacity metadata in POSIX statvfs/statfs
// terms. Zero fields mean the backend could not report that value.
type Statfs struct {
	Blocks  uint64
	Bfree   uint64
	Bavail  uint64
	Files   uint64
	Ffree   uint64
	Bsize   uint32
	Frsize  uint32
	NameLen uint32
}

// FileClient is the backend-agnostic file-operation surface.
type FileClient interface {
	Open(path string) (RemoteFile, error)
	Create(path string) (RemoteWriteFile, error)
	OpenFile(path string, flags int, mode os.FileMode) (RemoteWriteFile, error)
	Stat(path string) (os.FileInfo, error)
	Lstat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.FileInfo, error)
	Readlink(path string) (string, error)
	Mkdir(path string, mode os.FileMode) error
	MkdirAll(path string, mode os.FileMode) error
	Remove(path string) error
	Rename(oldpath, newpath string) error
	Symlink(target, linkpath string) error
	Link(oldpath, newpath string) error
	Chmod(path string, mode os.FileMode) error
	Chown(path string, uid, gid int) error
	Chtimes(path string, atime, mtime time.Time) error
	Truncate(path string, size int64) error
	Statfs(path string) (*Statfs, error)
	// Getwd returns the remote server's default working directory, used
	// for "~" expansion. Backends without a meaningful working dir
	// should return "/".
	Getwd() (string, error)
}
