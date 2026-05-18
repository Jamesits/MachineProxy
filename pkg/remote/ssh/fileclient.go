//go:build backend_ssh

package ssh

import (
	"errors"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// sftpFileClient wraps *sftp.Client so it satisfies remote.FileClient.
// The method bodies match what workspacefs.SFTPAdapter,
// pathstub.RemoteOpener and agenttransfer's internal client wanted
// before this refactor.
type sftpFileClient struct{ c *sftp.Client }

func (a *sftpFileClient) Open(p string) (remote.RemoteFile, error) {
	return a.c.Open(p)
}
func (a *sftpFileClient) Create(p string) (remote.RemoteWriteFile, error) {
	return a.c.Create(p)
}
func (a *sftpFileClient) OpenFile(p string, flags int) (remote.RemoteWriteFile, error) {
	return a.c.OpenFile(p, flags)
}
func (a *sftpFileClient) Stat(p string) (os.FileInfo, error) {
	return a.c.Stat(p)
}
func (a *sftpFileClient) ReadDir(p string) ([]os.FileInfo, error) {
	return a.c.ReadDir(p)
}
func (a *sftpFileClient) Mkdir(p string) error         { return a.c.Mkdir(p) }
func (a *sftpFileClient) MkdirAll(p string) error      { return a.c.MkdirAll(p) }
func (a *sftpFileClient) Remove(p string) error        { return a.c.Remove(p) }
func (a *sftpFileClient) Rename(old, new string) error { return a.c.Rename(old, new) }
func (a *sftpFileClient) Chmod(p string, mode os.FileMode) error {
	return a.c.Chmod(p, mode)
}
func (a *sftpFileClient) Truncate(p string, size int64) error {
	return a.c.Truncate(p, size)
}
func (a *sftpFileClient) Getwd() (string, error) { return a.c.Getwd() }

// expandRemoteHome resolves a leading "~"/"~/" against the remote user's
// home (returned by Getwd). Other paths are returned unchanged without
// the extra round-trip.
func expandRemoteHome(fc remote.FileClient, p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := fc.Getwd()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("remote home directory is empty")
	}
	if p == "~" {
		return home, nil
	}
	return path.Join(home, p[2:]), nil
}

// parentDir returns the parent directory of p, using POSIX semantics
// (the remote is always POSIX-style regardless of local OS).
func parentDir(p string) string { return path.Dir(p) }
