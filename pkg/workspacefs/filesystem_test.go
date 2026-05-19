package workspacefs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/remote"
)

func TestReadFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/hello.txt": []byte("hello"),
		},
	}

	fs := New(client, "/workspace", nil)
	data, errno := fs.ReadFile(context.Background(), "hello.txt", 0, 5)

	if errno != 0 {
		t.Fatalf("ReadFile() errno = %v", errno)
	}
	if string(data) != "hello" {
		t.Fatalf("ReadFile() = %q, want %q", string(data), "hello")
	}
	if client.lastOpened != "/workspace/hello.txt" {
		t.Fatalf("expected read from /workspace/hello.txt, got %q", client.lastOpened)
	}
}

func TestReadFileMissingReturnsENOENT(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	_, errno := fs.ReadFile(context.Background(), "missing.txt", 0, 128)
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT, got %v", errno)
	}
}

func TestWriteFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/out.txt": {},
		},
	}

	fs := New(client, "/workspace", nil)
	n, errno := fs.WriteFile(context.Background(), "out.txt", []byte("written"), 0)

	if errno != 0 {
		t.Fatalf("WriteFile() errno = %v", errno)
	}
	if n != 7 {
		t.Fatalf("WriteFile() = %d bytes, want 7", n)
	}
	if string(client.files["/workspace/out.txt"]) != "written" {
		t.Fatalf("file contents = %q, want %q", string(client.files["/workspace/out.txt"]), "written")
	}
}

func TestWriteFileReturnsAndLogsCloseError(t *testing.T) {
	var log bytes.Buffer
	client := &fakeSFTPClient{
		files:    map[string][]byte{"/workspace/out.txt": []byte("old")},
		closeErr: syscall.ENOSPC,
	}
	fs := New(client, "/workspace", slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))

	_, errno := fs.WriteFile(context.Background(), "out.txt", []byte("new"), 0)

	if errno != syscall.ENOSPC {
		t.Fatalf("WriteFile() errno = %v, want ENOSPC", errno)
	}
	if !bytes.Contains(log.Bytes(), []byte("level=WARN")) || !bytes.Contains(log.Bytes(), []byte("fuse write close failed")) {
		t.Fatalf("expected warn log for close failure, got %q", log.String())
	}
}

func TestCreateFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.CreateFile(context.Background(), "new.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_TRUNC), 0o644)
	if errno != 0 {
		t.Fatalf("CreateFile() errno = %v", errno)
	}
	if _, ok := client.files["/workspace/new.txt"]; !ok {
		t.Fatal("expected file to be created")
	}
}

func TestDirNodeCreateHonorsExclusiveFlag(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/existing.txt": []byte("keep")}}
	fs := New(client, "/workspace", nil)
	node := &dirNode{backend: fs, relPath: ""}

	_, _, _, errno := node.Create(context.Background(), "existing.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_EXCL), 0o600, &fuse.EntryOut{})

	if errno != syscall.EEXIST {
		t.Fatalf("Create(O_EXCL existing) errno = %v, want EEXIST", errno)
	}
	if string(client.files["/workspace/existing.txt"]) != "keep" {
		t.Fatalf("existing file was modified: %q", string(client.files["/workspace/existing.txt"]))
	}
}

func TestCreateFilePassesExclusiveFlagToBackend(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.CreateFile(context.Background(), "new.txt", uint32(os.O_CREATE|os.O_WRONLY|os.O_EXCL), 0o600)

	if errno != 0 {
		t.Fatalf("CreateFile() errno = %v, want 0", errno)
	}
	if client.lastOpenFlags&os.O_EXCL == 0 {
		t.Fatalf("OpenFile flags = %#x, want O_EXCL set", client.lastOpenFlags)
	}
}

func TestDirNodeCreateAppliesRequestedMode(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.CreateFile(context.Background(), "created.txt", uint32(os.O_CREATE|os.O_WRONLY), 0o600)

	if errno != 0 {
		t.Fatalf("CreateFile() errno = %v", errno)
	}
	info, errno := fs.Stat(context.Background(), "created.txt")
	if errno != 0 {
		t.Fatalf("Stat() errno = %v", errno)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("created mode = %o, want 600", got)
	}
}

func TestCreateFileHonorsZeroMode(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.CreateFile(context.Background(), "private.txt", uint32(os.O_CREATE|os.O_WRONLY), 0)

	if errno != 0 {
		t.Fatalf("CreateFile() errno = %v", errno)
	}
	info, errno := fs.Stat(context.Background(), "private.txt")
	if errno != 0 {
		t.Fatalf("Stat() errno = %v", errno)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("created mode = %o, want 000", got)
	}
}

func TestFileNodeAppendUsesOpenHandleFlags(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/log.txt": []byte("old")}}
	fs := New(client, "/workspace", nil)
	node := &fileNode{backend: fs, relPath: "log.txt"}

	fh, _, errno := node.Open(context.Background(), uint32(os.O_WRONLY|os.O_APPEND))
	if errno != 0 {
		t.Fatalf("Open(O_APPEND) errno = %v", errno)
	}
	if _, errno := node.Write(context.Background(), fh, []byte("new"), 0); errno != 0 {
		t.Fatalf("Write() errno = %v", errno)
	}

	if got := string(client.files["/workspace/log.txt"]); got != "oldnew" {
		t.Fatalf("file contents = %q, want oldnew", got)
	}
}

func TestFileNodeFsyncUsesOpenHandleAfterUnlink(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/out.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)
	node := &fileNode{backend: fs, relPath: "out.txt"}

	fh, _, errno := node.Open(context.Background(), uint32(os.O_WRONLY))
	if errno != 0 {
		t.Fatalf("Open() errno = %v", errno)
	}
	delete(client.files, "/workspace/out.txt")

	if errno := node.Fsync(context.Background(), fh, 0); errno != 0 {
		t.Fatalf("Fsync() errno = %v, want 0", errno)
	}
	if len(client.syncedPaths) != 1 || client.syncedPaths[0] != "/workspace/out.txt" {
		t.Fatalf("synced paths = %v, want [/workspace/out.txt]", client.syncedPaths)
	}
}

func TestFileNodeGetattrUsesOpenHandleAfterUnlink(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/out.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)
	node := &fileNode{backend: fs, relPath: "out.txt"}

	fh, _, errno := node.Open(context.Background(), uint32(os.O_WRONLY))
	if errno != 0 {
		t.Fatalf("Open() errno = %v", errno)
	}
	delete(client.files, "/workspace/out.txt")

	var out fuse.AttrOut
	if errno := node.Getattr(context.Background(), fh, &out); errno != 0 {
		t.Fatalf("Getattr() errno = %v, want 0", errno)
	}
	if out.Size != 4 {
		t.Fatalf("Getattr size = %d, want 4", out.Size)
	}
}

func TestFsyncDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/out.txt": []byte("data"),
		},
	}

	fs := New(client, "/workspace", nil)
	errno := fs.Fsync(context.Background(), "out.txt")
	if errno != 0 {
		t.Fatalf("Fsync() errno = %v, want 0", errno)
	}
	if len(client.syncedPaths) != 1 || client.syncedPaths[0] != "/workspace/out.txt" {
		t.Fatalf("synced paths = %v, want [/workspace/out.txt]", client.syncedPaths)
	}
}

func TestFsyncMissingFileIsNoop(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}}
	fs := New(client, "/workspace", nil)

	errno := fs.Fsync(context.Background(), "missing.txt")
	if errno != syscall.ENOENT {
		t.Fatalf("Fsync() on missing file errno = %v, want ENOENT", errno)
	}
	if len(client.syncedPaths) != 0 {
		t.Fatalf("expected no syncs, got %v", client.syncedPaths)
	}
}

func TestFsyncReturnsSyncError(t *testing.T) {
	client := &fakeSFTPClient{
		files:   map[string][]byte{"/workspace/out.txt": []byte("data")},
		syncErr: syscall.EIO,
	}
	fs := New(client, "/workspace", nil)

	errno := fs.Fsync(context.Background(), "out.txt")
	if errno != syscall.EIO {
		t.Fatalf("Fsync() errno = %v, want EIO", errno)
	}
}

func TestSymlinkPreservesLinkMetadata(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/target.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)

	if errno := fs.Symlink(context.Background(), "target.txt", "link.txt"); errno != 0 {
		t.Fatalf("Symlink() errno = %v", errno)
	}
	info, errno := fs.Stat(context.Background(), "link.txt")
	if errno != 0 {
		t.Fatalf("Stat(link) errno = %v", errno)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link mode = %v, want symlink", info.Mode())
	}
	target, errno := fs.Readlink(context.Background(), "link.txt")
	if errno != 0 {
		t.Fatalf("Readlink() errno = %v", errno)
	}
	if string(target) != "target.txt" {
		t.Fatalf("Readlink() = %q, want target.txt", string(target))
	}
}

func TestMkDirDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{},
		dirs:  map[string]bool{},
	}
	fs := New(client, "/workspace", nil)

	errno := fs.MkDir(context.Background(), "subdir", 0o755)
	if errno != 0 {
		t.Fatalf("MkDir() errno = %v", errno)
	}
	if !client.dirs["/workspace/subdir"] {
		t.Fatal("expected directory to be created")
	}
}

func TestMkDirAppliesRequestedMode(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}, dirs: map[string]bool{}}
	fs := New(client, "/workspace", nil)

	errno := fs.MkDir(context.Background(), "private", 0o700)

	if errno != 0 {
		t.Fatalf("MkDir() errno = %v", errno)
	}
	info, errno := fs.Stat(context.Background(), "private")
	if errno != 0 {
		t.Fatalf("Stat() errno = %v", errno)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
}

func TestMkDirHonorsZeroMode(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}, dirs: map[string]bool{}}
	fs := New(client, "/workspace", nil)

	errno := fs.MkDir(context.Background(), "closed", 0)

	if errno != 0 {
		t.Fatalf("MkDir() errno = %v", errno)
	}
	info, errno := fs.Stat(context.Background(), "closed")
	if errno != 0 {
		t.Fatalf("Stat() errno = %v", errno)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("directory mode = %o, want 000", got)
	}
}

func TestUnlinkRejectsDirectory(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{}, dirs: map[string]bool{"/workspace/dir": true}}
	fs := New(client, "/workspace", nil)

	errno := fs.Unlink(context.Background(), "dir")

	if errno != syscall.EISDIR {
		t.Fatalf("Unlink(directory) errno = %v, want EISDIR", errno)
	}
	if !client.dirs["/workspace/dir"] {
		t.Fatal("directory was removed by Unlink")
	}
}

func TestRmdirRejectsRegularFile(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/file.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)

	errno := fs.Rmdir(context.Background(), "file.txt")

	if errno != syscall.ENOTDIR {
		t.Fatalf("Rmdir(file) errno = %v, want ENOTDIR", errno)
	}
	if _, ok := client.files["/workspace/file.txt"]; !ok {
		t.Fatal("file was removed by Rmdir")
	}
}

func TestRenameRejectsUnsupportedFlags(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/old.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)
	oldDir := &dirNode{backend: fs, relPath: ""}
	newDir := &dirNode{backend: fs, relPath: ""}

	errno := oldDir.Rename(context.Background(), "old.txt", newDir, "new.txt", 1)

	if errno != syscall.ENOTSUP {
		t.Fatalf("Rename(flags=1) errno = %v, want ENOTSUP", errno)
	}
	if _, ok := client.files["/workspace/old.txt"]; !ok {
		t.Fatal("old file was renamed despite unsupported flags")
	}
}

func TestDirFsyncSucceedsAsNoop(t *testing.T) {
	client := &fakeSFTPClient{dirs: map[string]bool{"/workspace": true}}
	fs := New(client, "/workspace", nil)
	node := &dirNode{backend: fs, relPath: ""}

	if errno := node.Fsync(context.Background(), nil, 0); errno != 0 {
		t.Fatalf("Fsync(dir) errno = %v, want 0", errno)
	}
}

func TestRootAccessRequiresExecuteBitForXOK(t *testing.T) {
	ctx := &fuse.Context{Caller: fuse.Caller{Owner: fuse.Owner{Uid: 0, Gid: 0}}}

	errno := access(ctx, fakeFileInfo{name: "script", mode: 0o644}, uint32(fuse.X_OK))

	if errno != syscall.EACCES {
		t.Fatalf("access(root, X_OK, 0644) errno = %v, want EACCES", errno)
	}
}

func TestSetattrRejectsUnsupportedAttributes(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/file.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)
	node := &fileNode{backend: fs, relPath: "file.txt"}
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_MTIME

	errno := node.Setattr(context.Background(), nil, in, &fuse.AttrOut{})

	if errno != 0 {
		t.Fatalf("Setattr(mtime) errno = %v, want 0", errno)
	}
}

func TestSetattrRejectsCtime(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/file.txt": []byte("data")}}
	fs := New(client, "/workspace", nil)
	node := &fileNode{backend: fs, relPath: "file.txt"}
	in := &fuse.SetAttrIn{}
	in.Valid = fuse.FATTR_CTIME

	errno := node.Setattr(context.Background(), nil, in, &fuse.AttrOut{})

	if errno != syscall.ENOTSUP {
		t.Fatalf("Setattr(ctime) errno = %v, want ENOTSUP", errno)
	}
}

func TestToErrnoPreservesSpecificErrnos(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want syscall.Errno
	}{
		{name: "direct errno", err: syscall.ENOTEMPTY, want: syscall.ENOTEMPTY},
		{name: "path error errno", err: &os.PathError{Op: "remove", Path: "/x", Err: syscall.ENOTDIR}, want: syscall.ENOTDIR},
		{name: "invalid sentinel", err: os.ErrInvalid, want: syscall.EINVAL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toErrno(tt.err); got != tt.want {
				t.Fatalf("toErrno(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestSymlinkModeIsPreserved(t *testing.T) {
	if got := modeToDirEntryMode(os.ModeSymlink | 0o777); got != fuse.S_IFLNK {
		t.Fatalf("modeToDirEntryMode(symlink) = %#o, want symlink %#o", got, fuse.S_IFLNK)
	}
}

func TestApplyFileInfoUsesStableAtime(t *testing.T) {
	mtime := time.Unix(123, 456)
	var out fuse.Attr

	applyFileInfo(&out, fakeFileInfo{name: "file.txt", mode: 0o644, size: 1, modTime: mtime})

	if out.Atime != uint64(mtime.Unix()) || out.Atimensec != uint32(mtime.Nanosecond()) {
		t.Fatalf("atime = %d.%d, want mtime %d.%d", out.Atime, out.Atimensec, mtime.Unix(), mtime.Nanosecond())
	}
}

func TestApplyFileInfoPreservesSpecialModeBits(t *testing.T) {
	var fileOut fuse.Attr
	applyFileInfo(&fileOut, fakeFileInfo{name: "setuid", mode: os.ModeSetuid | os.ModeSetgid | 0o755, size: 1})
	if fileOut.Mode&0o6000 != 0o6000 {
		t.Fatalf("file mode = %#o, want setuid and setgid bits", fileOut.Mode)
	}

	var dirOut fuse.Attr
	applyFileInfo(&dirOut, fakeFileInfo{name: "tmp", mode: os.ModeDir | os.ModeSticky | 0o777})
	if dirOut.Mode&0o1000 != 0o1000 {
		t.Fatalf("dir mode = %#o, want sticky bit", dirOut.Mode)
	}
}

// fakeSFTPClient is an in-memory remote.FileClient used for unit tests.
type fakeSFTPClient struct {
	files         map[string][]byte
	dirs          map[string]bool
	symlinks      map[string]string
	modes         map[string]os.FileMode
	owners        map[string][2]int
	times         map[string][2]time.Time
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
	return &fakeRemoteFile{data: data}, nil
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
		return fakeFileInfo{name: path.Base(clean), mode: os.ModeDir | f.mode(clean, 0o755)}, nil
	}
	if target, ok := f.symlinks[clean]; ok {
		if follow {
			return f.stat(path.Join(path.Dir(clean), target), true)
		}
		return fakeFileInfo{name: path.Base(clean), size: int64(len(target)), mode: os.ModeSymlink | f.mode(clean, 0o777)}, nil
	}
	data, ok := f.files[clean]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: path.Base(clean), size: int64(len(data)), mode: f.mode(clean, 0o644)}, nil
}

func (f *fakeSFTPClient) ReadDir(string) ([]os.FileInfo, error) {
	return nil, errors.New("not implemented")
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
	data []byte
	pos  int64
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
func (f fakeFileInfo) Sys() any    { return nil }
