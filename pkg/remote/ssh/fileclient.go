//go:build backend_ssh

package ssh

import (
	"errors"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// sftpFileClient wraps *sftp.Client so it satisfies remote.FileClient
// for workspacefs, pathstub, and agenttransfer callers.
type sftpFileClient struct{ c *sftp.Client }

func (a *sftpFileClient) Open(p string) (remote.RemoteFile, error) {
	f, err := a.c.Open(p)
	if err != nil {
		return nil, err
	}
	return sftpRemoteFile{File: f}, nil
}
func (a *sftpFileClient) Create(p string) (remote.RemoteWriteFile, error) {
	f, err := a.c.Create(p)
	if err != nil {
		return nil, err
	}
	return sftpRemoteFile{File: f}, nil
}
func (a *sftpFileClient) OpenFile(p string, flags int, mode os.FileMode) (remote.RemoteWriteFile, error) {
	_, statErr := a.c.Lstat(p)
	f, err := a.c.OpenFile(p, flags)
	if err != nil {
		return nil, err
	}
	if flags&os.O_CREATE != 0 && flags&os.O_EXCL != 0 && errors.Is(statErr, os.ErrNotExist) {
		if err := f.Chmod(mode); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return sftpRemoteFile{File: f}, nil
}
func (a *sftpFileClient) Stat(p string) (os.FileInfo, error) {
	return a.c.Stat(p)
}
func (a *sftpFileClient) Lstat(p string) (os.FileInfo, error) {
	return a.c.Lstat(p)
}
func (a *sftpFileClient) ReadDir(p string) ([]os.FileInfo, error) {
	return a.c.ReadDir(p)
}
func (a *sftpFileClient) Readlink(p string) (string, error) { return a.c.ReadLink(p) }
func (a *sftpFileClient) Mkdir(p string, mode os.FileMode) error {
	if err := a.c.Mkdir(p); err != nil {
		return err
	}
	return a.c.Chmod(p, mode)
}
func (a *sftpFileClient) MkdirAll(p string, mode os.FileMode) error {
	return a.c.MkdirAll(p)
}
func (a *sftpFileClient) Remove(p string) error        { return a.c.Remove(p) }
func (a *sftpFileClient) Rename(old, new string) error { return a.c.Rename(old, new) }
func (a *sftpFileClient) Symlink(target, linkpath string) error {
	return a.c.Symlink(target, linkpath)
}
func (a *sftpFileClient) Link(old, new string) error { return a.c.Link(old, new) }
func (a *sftpFileClient) Chmod(p string, mode os.FileMode) error {
	return a.c.Chmod(p, mode)
}
func (a *sftpFileClient) Chown(p string, uid, gid int) error {
	return a.c.Chown(p, uid, gid)
}
func (a *sftpFileClient) Chtimes(p string, atime, mtime time.Time) error {
	return a.c.Chtimes(p, atime, mtime)
}
func (a *sftpFileClient) Truncate(p string, size int64) error {
	return a.c.Truncate(p, size)
}
func (a *sftpFileClient) Statfs(p string) (*remote.Statfs, error) {
	st, err := a.c.StatVFS(p)
	if err != nil {
		return nil, err
	}
	return &remote.Statfs{
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Bsize:   uint32(st.Bsize),
		Frsize:  uint32(st.Frsize),
		NameLen: uint32(st.Namemax),
	}, nil
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

type sftpRemoteFile struct {
	*sftp.File
}

func (f sftpRemoteFile) Sync() error {
	err := f.File.Sync()
	if isUnsupported(err) {
		return nil
	}
	return err
}

func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	var status *sftp.StatusError
	if errors.As(err, &status) && status.FxCode() == sftp.ErrSSHFxOpUnsupported {
		return true
	}
	return errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP)
}
