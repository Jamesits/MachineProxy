package workspacefs

import (
	"io"
	"os"
	"path"
	"sort"
	"syscall"
	"time"

	"github.com/jamesits/machineproxy/pkg/remote"
)

// fakeSFTPClient is an in-memory remote.FileClient used for unit tests.
type fakeSFTPClient struct {
	files         map[string][]byte
	dirs          map[string]bool
	symlinks      map[string]string
	modes         map[string]os.FileMode
	owners        map[string][2]int
	times         map[string][2]time.Time
	fileSys       map[string]any // optional os.FileInfo.Sys() payload per path
	lastOpened    string
	lastOpenFlags int
	syncedPaths   []string
	closeErr      error
	syncErr       error
}

var _ remote.FileClient = (*fakeSFTPClient)(nil)

func (f *fakeSFTPClient) Open(p string) (remote.RemoteFile, error) {
	f.lastOpened = path.Clean(p)
	if target, ok := f.symlinks[f.lastOpened]; ok {
		f.lastOpened = path.Clean(path.Join(path.Dir(f.lastOpened), target))
	}
	data, ok := f.files[f.lastOpened]
	if !ok {
		return nil, os.ErrNotExist
	}
	return &fakeRemoteFile{client: f, path: f.lastOpened, data: data}, nil
}

func (f *fakeSFTPClient) Create(p string) (remote.RemoteWriteFile, error) {
	clean := path.Clean(p)
	f.files[clean] = []byte{}
	f.setMode(clean, 0o644)
	return &fakeRemoteWriteFile{client: f, path: clean, statSize: int64(len(f.files[clean])), statMode: f.mode(clean, 0o644)}, nil
}

func (f *fakeSFTPClient) OpenFile(p string, flags int, mode os.FileMode) (remote.RemoteWriteFile, error) {
	clean := path.Clean(p)
	f.lastOpened = clean
	f.lastOpenFlags = flags
	if target, ok := f.symlinks[clean]; ok {
		clean = path.Clean(path.Join(path.Dir(clean), target))
	}
	_, ok := f.files[clean]
	if !ok && flags&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	if ok && flags&os.O_EXCL != 0 && flags&os.O_CREATE != 0 {
		return nil, os.ErrExist
	}
	if !ok {
		f.files[clean] = []byte{}
		f.setMode(clean, mode)
	}
	if flags&os.O_TRUNC != 0 {
		f.files[clean] = []byte{}
	}
	return &fakeRemoteWriteFile{client: f, path: clean, flags: flags, statSize: int64(len(f.files[clean])), statMode: f.mode(clean, 0o644)}, nil
}

func (f *fakeSFTPClient) Stat(p string) (os.FileInfo, error) {
	return f.stat(p, true)
}

func (f *fakeSFTPClient) Lstat(p string) (os.FileInfo, error) {
	return f.stat(p, false)
}

func (f *fakeSFTPClient) stat(p string, follow bool) (os.FileInfo, error) {
	clean := path.Clean(p)
	if f.dirs[clean] {
		return fakeFileInfo{name: path.Base(clean), mode: os.ModeDir | f.mode(clean, 0o755), sys: f.sysFor(clean)}, nil
	}
	if target, ok := f.symlinks[clean]; ok {
		if follow {
			return f.stat(path.Join(path.Dir(clean), target), true)
		}
		return fakeFileInfo{name: path.Base(clean), size: int64(len(target)), mode: os.ModeSymlink | f.mode(clean, 0o777), sys: f.sysFor(clean)}, nil
	}
	data, ok := f.files[clean]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: path.Base(clean), size: int64(len(data)), mode: f.mode(clean, 0o644), sys: f.sysFor(clean)}, nil
}

func (f *fakeSFTPClient) sysFor(p string) any {
	if f.fileSys == nil {
		return nil
	}
	return f.fileSys[p]
}

func (f *fakeSFTPClient) ReadDir(p string) ([]os.FileInfo, error) {
	clean := path.Clean(p)
	if _, ok := f.files[clean]; ok {
		return nil, syscall.ENOTDIR
	}
	if _, ok := f.symlinks[clean]; ok {
		return nil, syscall.ENOTDIR
	}

	byName := map[string]os.FileInfo{}
	for dir := range f.dirs {
		if dir == clean || path.Dir(dir) != clean {
			continue
		}
		st, err := f.stat(dir, false)
		if err == nil {
			byName[st.Name()] = st
		}
	}
	for p, data := range f.files {
		filePath := path.Clean(p)
		if path.Dir(filePath) != clean {
			continue
		}
		byName[path.Base(filePath)] = fakeFileInfo{
			name: path.Base(filePath),
			size: int64(len(data)),
			mode: f.mode(filePath, 0o644),
			sys:  f.sysFor(filePath),
		}
	}
	for p, target := range f.symlinks {
		linkPath := path.Clean(p)
		if path.Dir(linkPath) != clean {
			continue
		}
		byName[path.Base(linkPath)] = fakeFileInfo{
			name: path.Base(linkPath),
			size: int64(len(target)),
			mode: os.ModeSymlink | f.mode(linkPath, 0o777),
			sys:  f.sysFor(linkPath),
		}
	}

	if !f.dirs[clean] && len(byName) == 0 {
		return nil, os.ErrNotExist
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]os.FileInfo, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out, nil
}

func (f *fakeSFTPClient) Readlink(p string) (string, error) {
	target, ok := f.symlinks[path.Clean(p)]
	if !ok {
		return "", os.ErrNotExist
	}
	return target, nil
}

func (f *fakeSFTPClient) Mkdir(p string, mode os.FileMode) error {
	clean := path.Clean(p)
	if f.dirs == nil {
		f.dirs = map[string]bool{}
	}
	f.dirs[clean] = true
	f.setMode(clean, os.ModeDir|mode)
	return nil
}

func (f *fakeSFTPClient) MkdirAll(p string, mode os.FileMode) error { return f.Mkdir(p, mode) }

func (f *fakeSFTPClient) Remove(p string) error {
	clean := path.Clean(p)
	delete(f.files, clean)
	delete(f.dirs, clean)
	return nil
}

func (f *fakeSFTPClient) Rename(old, new string) error {
	oldClean := path.Clean(old)
	newClean := path.Clean(new)
	if data, ok := f.files[oldClean]; ok {
		f.files[newClean] = data
		delete(f.files, oldClean)
		return nil
	}
	if target, ok := f.symlinks[oldClean]; ok {
		if f.symlinks == nil {
			f.symlinks = map[string]string{}
		}
		f.symlinks[newClean] = target
		delete(f.symlinks, oldClean)
		return nil
	}
	if f.dirs[oldClean] {
		if f.dirs == nil {
			f.dirs = map[string]bool{}
		}
		f.dirs[newClean] = true
		delete(f.dirs, oldClean)
		return nil
	}
	return os.ErrNotExist
}

func (f *fakeSFTPClient) Symlink(target, linkpath string) error {
	clean := path.Clean(linkpath)
	if f.symlinks == nil {
		f.symlinks = map[string]string{}
	}
	if _, ok := f.files[clean]; ok || f.dirs[clean] || f.symlinks[clean] != "" {
		return os.ErrExist
	}
	f.symlinks[clean] = target
	f.setMode(clean, os.ModeSymlink|0o777)
	return nil
}

func (f *fakeSFTPClient) Link(old, new string) error {
	oldClean := path.Clean(old)
	newClean := path.Clean(new)
	data, ok := f.files[oldClean]
	if !ok {
		return os.ErrNotExist
	}
	f.files[newClean] = data
	f.setMode(newClean, f.mode(oldClean, 0o644))
	return nil
}

func (f *fakeSFTPClient) setMode(p string, mode os.FileMode) {
	if f.modes == nil {
		f.modes = map[string]os.FileMode{}
	}
	f.modes[path.Clean(p)] = mode
}

func (f *fakeSFTPClient) mode(p string, def os.FileMode) os.FileMode {
	if f.modes == nil {
		return def
	}
	if mode, ok := f.modes[path.Clean(p)]; ok {
		return mode
	}
	return def
}

func (f *fakeSFTPClient) Chmod(p string, mode os.FileMode) error {
	clean := path.Clean(p)
	if _, ok := f.files[clean]; !ok && !f.dirs[clean] && f.symlinks[clean] == "" {
		return os.ErrNotExist
	}
	f.setMode(clean, mode)
	return nil
}

func (f *fakeSFTPClient) Chown(p string, uid, gid int) error {
	clean := path.Clean(p)
	if _, err := f.stat(clean, true); err != nil {
		return err
	}
	if f.owners == nil {
		f.owners = map[string][2]int{}
	}
	f.owners[clean] = [2]int{uid, gid}
	return nil
}

func (f *fakeSFTPClient) Chtimes(p string, atime, mtime time.Time) error {
	clean := path.Clean(p)
	if _, err := f.stat(clean, true); err != nil {
		return err
	}
	if f.times == nil {
		f.times = map[string][2]time.Time{}
	}
	f.times[clean] = [2]time.Time{atime, mtime}
	return nil
}

func (f *fakeSFTPClient) Truncate(p string, size int64) error {
	clean := path.Clean(p)
	data, ok := f.files[clean]
	if !ok {
		return os.ErrNotExist
	}
	if int64(len(data)) > size {
		f.files[clean] = data[:size]
	}
	return nil
}

func (f *fakeSFTPClient) Statfs(string) (*remote.Statfs, error) {
	return &remote.Statfs{Blocks: 100, Bfree: 50, Bavail: 40, Files: 10, Ffree: 5, Bsize: 4096, Frsize: 4096, NameLen: 255}, nil
}

func (f *fakeSFTPClient) Getwd() (string, error) { return "/", nil }

type fakeRemoteFile struct {
	client *fakeSFTPClient
	path   string
	data   []byte
	pos    int64
}

type fakeRemoteWriteFile struct {
	client   *fakeSFTPClient
	path     string
	flags    int
	pos      int64
	statSize int64
	statMode os.FileMode
}

func (f *fakeRemoteFile) Read(p []byte) (int, error) {
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *fakeRemoteFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if int(off)+n >= len(f.data) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeRemoteFile) Close() error { return nil }

func (f *fakeRemoteFile) Sync() error {
	f.client.syncedPaths = append(f.client.syncedPaths, f.path)
	return f.client.syncErr
}

func (f *fakeRemoteWriteFile) Write(p []byte) (int, error) {
	if f.flags&os.O_APPEND != 0 {
		return f.WriteAt(p, int64(len(f.client.files[f.path])))
	}
	n, err := f.WriteAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *fakeRemoteWriteFile) WriteAt(p []byte, off int64) (int, error) {
	existing := f.client.files[f.path]
	needed := int(off) + len(p)
	if needed > len(existing) {
		grown := make([]byte, needed)
		copy(grown, existing)
		existing = grown
	}
	copy(existing[off:], p)
	f.client.files[f.path] = existing
	f.statSize = int64(len(existing))
	return len(p), nil
}

func (f *fakeRemoteWriteFile) ReadAt(p []byte, off int64) (int, error) {
	data := f.client.files[f.path]
	if off >= int64(len(data)) {
		return 0, io.EOF
	}
	n := copy(p, data[off:])
	if int(off)+n >= len(data) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeRemoteWriteFile) Stat() (os.FileInfo, error) {
	if data, ok := f.client.files[f.path]; ok {
		return fakeFileInfo{name: path.Base(f.path), size: int64(len(data)), mode: f.client.mode(f.path, 0o644)}, nil
	}
	return fakeFileInfo{name: path.Base(f.path), size: f.statSize, mode: f.statMode}, nil
}

func (f *fakeRemoteWriteFile) Truncate(size int64) error {
	f.statSize = size
	return f.client.Truncate(f.path, size)
}

func (f *fakeRemoteWriteFile) Sync() error {
	f.client.syncedPaths = append(f.client.syncedPaths, f.path)
	return f.client.syncErr
}

func (f *fakeRemoteWriteFile) Close() error { return f.client.closeErr }

type fakeFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	sys     any
}

func (f fakeFileInfo) Name() string      { return f.name }
func (f fakeFileInfo) Size() int64       { return f.size }
func (f fakeFileInfo) Mode() os.FileMode { return f.mode }
func (f fakeFileInfo) ModTime() time.Time {
	if f.modTime.IsZero() {
		return time.Unix(0, 0)
	}
	return f.modTime
}
func (f fakeFileInfo) IsDir() bool { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any    { return f.sys }
