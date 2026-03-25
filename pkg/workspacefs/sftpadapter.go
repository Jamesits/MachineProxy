package workspacefs

import (
	"os"

	"github.com/pkg/sftp"
)

// SFTPAdapter wraps *sftp.Client so it satisfies the SFTPClient interface.
// The underlying sftp methods return *sftp.File, which implements both
// RemoteFile and RemoteWriteFile, but Go requires exact return-type matches
// for interface satisfaction, so we wrap the calls.
type SFTPAdapter struct {
	C *sftp.Client
}

func (a *SFTPAdapter) Open(path string) (RemoteFile, error) {
	return a.C.Open(path)
}

func (a *SFTPAdapter) Create(path string) (RemoteWriteFile, error) {
	return a.C.Create(path)
}

func (a *SFTPAdapter) OpenFile(path string, flags int) (RemoteWriteFile, error) {
	return a.C.OpenFile(path, flags)
}

func (a *SFTPAdapter) Stat(path string) (os.FileInfo, error) {
	return a.C.Stat(path)
}

func (a *SFTPAdapter) ReadDir(path string) ([]os.FileInfo, error) {
	return a.C.ReadDir(path)
}

func (a *SFTPAdapter) Mkdir(path string) error {
	return a.C.Mkdir(path)
}

func (a *SFTPAdapter) Remove(path string) error {
	return a.C.Remove(path)
}

func (a *SFTPAdapter) Rename(oldpath, newpath string) error {
	return a.C.Rename(oldpath, newpath)
}

func (a *SFTPAdapter) Chmod(path string, mode os.FileMode) error {
	return a.C.Chmod(path, mode)
}

func (a *SFTPAdapter) Truncate(path string, size int64) error {
	return a.C.Truncate(path, size)
}
