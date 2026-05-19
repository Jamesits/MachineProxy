package workspacefs

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func TestReadFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/hello.txt": []byte("hello"),
		},
	}

	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)

	_, errno := fs.ReadFile(context.Background(), "missing.txt", 0, 128)
	if errno != syscall.ENOENT {
		t.Fatalf("expected ENOENT, got %v", errno)
	}
}

func TestReadDirDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/b.txt":                []byte("bb"),
			"/workspace/a.txt":                []byte("a"),
			"/workspace/subdir/nested.txt":    []byte("nested"),
			"/workspace/subdir/another.txt":   []byte("nested"),
			"/workspace/dir/hidden-child.txt": []byte("hidden"),
		},
		dirs: map[string]bool{
			"/workspace":        true,
			"/workspace/dir":    true,
			"/workspace/subdir": true,
		},
		symlinks: map[string]string{
			"/workspace/link": "a.txt",
		},
	}
	fs := New(client, "/workspace", nil, nil)

	entries, errno := fs.ReadDir(context.Background(), "")
	if errno != 0 {
		t.Fatalf("ReadDir() errno = %v", errno)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"a.txt", "b.txt", "dir", "link", "subdir"}
	if !slices.Equal(names, want) {
		t.Fatalf("ReadDir() names = %v, want %v", names, want)
	}
}

func TestWriteFileDelegatesToSFTP(t *testing.T) {
	client := &fakeSFTPClient{
		files: map[string][]byte{
			"/workspace/out.txt": {},
		},
	}

	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})), nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)
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

	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

	errno := fs.Fsync(context.Background(), "out.txt")
	if errno != syscall.EIO {
		t.Fatalf("Fsync() errno = %v, want EIO", errno)
	}
}

func TestSymlinkPreservesLinkMetadata(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/target.txt": []byte("data")}}
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)

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
	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)
	node := &dirNode{backend: fs, relPath: ""}

	if errno := node.Fsync(context.Background(), nil, 0); errno != 0 {
		t.Fatalf("Fsync(dir) errno = %v, want 0", errno)
	}
}

func TestRootAccessRequiresExecuteBitForXOK(t *testing.T) {
	ctx := &fuse.Context{Caller: fuse.Caller{Owner: fuse.Owner{Uid: 0, Gid: 0}}}

	errno := access(ctx, nil, fakeFileInfo{name: "script", mode: 0o644}, uint32(fuse.X_OK))

	if errno != syscall.EACCES {
		t.Fatalf("access(root, X_OK, 0644) errno = %v, want EACCES", errno)
	}
}

func TestSetattrRejectsUnsupportedAttributes(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/file.txt": []byte("data")}}
	fs := New(client, "/workspace", nil, nil)
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
	fs := New(client, "/workspace", nil, nil)
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

func TestApplyAttrOverrideRewritesUIDGID(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode:  IDModeOverride,
		GIDMode:  IDModeOverride,
		LocalUID: 1000,
		LocalGID: 1001,
	})
	st := fakeFileInfo{name: "x", mode: 0o644, size: 1, sys: &agentproto.FileStat{UID: 0, GID: 0}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 1000 || out.Gid != 1001 {
		t.Fatalf("override mapping: uid/gid = %d/%d, want 1000/1001", out.Uid, out.Gid)
	}
}

func TestApplyAttrTransparentPreservesRemoteIDs(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode:  IDModeTransparent,
		GIDMode:  IDModeTransparent,
		LocalUID: 1000,
		LocalGID: 1001,
	})
	st := fakeFileInfo{name: "x", mode: 0o644, size: 1, sys: &agentproto.FileStat{UID: 42, GID: 43}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 42 || out.Gid != 43 {
		t.Fatalf("transparent mapping: uid/gid = %d/%d, want 42/43", out.Uid, out.Gid)
	}
}

func TestApplyAttrMixedOverrideOneDimension(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode:  IDModeOverride,
		GIDMode:  IDModeTransparent,
		LocalUID: 1000,
		LocalGID: 1001,
	})
	st := fakeFileInfo{name: "x", mode: 0o644, size: 1, sys: &agentproto.FileStat{UID: 0, GID: 43}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 1000 {
		t.Fatalf("override uid: got %d, want 1000", out.Uid)
	}
	if out.Gid != 43 {
		t.Fatalf("transparent gid: got %d, want 43", out.Gid)
	}
}

func TestChownOverrideBothIsNoop(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/f.txt": []byte("d")}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeOverride,
		LocalUID: 1000, LocalGID: 1001,
	})

	if errno := fs.Chown(context.Background(), "f.txt", 555, 666); errno != 0 {
		t.Fatalf("Chown override: errno = %v, want 0", errno)
	}
	if _, recorded := client.owners["/workspace/f.txt"]; recorded {
		t.Fatalf("override mode should not forward chown to backend, got %v", client.owners)
	}
}

func TestChownTransparentForwards(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/f.txt": []byte("d")}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeTransparent, GIDMode: IDModeTransparent,
	})

	if errno := fs.Chown(context.Background(), "f.txt", 555, 666); errno != 0 {
		t.Fatalf("Chown transparent: errno = %v, want 0", errno)
	}
	got, ok := client.owners["/workspace/f.txt"]
	if !ok {
		t.Fatalf("transparent mode should forward chown to backend")
	}
	if got != [2]int{555, 666} {
		t.Fatalf("chown forwarded as %v, want [555 666]", got)
	}
}

func TestChownMixedOverrideKeepsRemoteSide(t *testing.T) {
	client := &fakeSFTPClient{
		files:  map[string][]byte{"/workspace/f.txt": []byte("d")},
		owners: map[string][2]int{},
	}
	// Seed the remote-reported owner so the override-side fallback has
	// something to read.
	client.fileSys = map[string]any{"/workspace/f.txt": &agentproto.FileStat{UID: 42, GID: 7}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeTransparent,
		LocalUID: 1000, LocalGID: 1001,
	})

	if errno := fs.Chown(context.Background(), "f.txt", 555, 666); errno != 0 {
		t.Fatalf("Chown mixed: errno = %v, want 0", errno)
	}
	got, ok := client.owners["/workspace/f.txt"]
	if !ok {
		t.Fatalf("mixed mode should still forward chown to backend")
	}
	// uid is override, so the original remote uid (42) is preserved;
	// gid is transparent, so the user-supplied 666 lands on the backend.
	if got != [2]int{42, 666} {
		t.Fatalf("chown forwarded as %v, want [42 666]", got)
	}
}

func TestAccessOverrideTreatsLocalUserAsOwner(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeOverride,
		LocalUID: 1000, LocalGID: 1001,
	})
	// Remote-reported owner is root, but mode is 0600 — only the owner
	// can read. With override, the local user (1000) must look like the
	// owner so default_permissions lets them through.
	st := fakeFileInfo{name: "x", mode: 0o600, sys: &agentproto.FileStat{UID: 0, GID: 0}}
	ctx := &fuse.Context{Caller: fuse.Caller{Owner: fuse.Owner{Uid: 1000, Gid: 1001}}}

	if errno := access(ctx, fs, st, uint32(fuse.R_OK|fuse.W_OK)); errno != 0 {
		t.Fatalf("access(override, owner-only file): errno = %v, want 0", errno)
	}
}

func TestApplyAttrUIDMapTakesPrecedenceOverOverride(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode:  IDModeOverride,
		GIDMode:  IDModeOverride,
		LocalUID: 1000,
		LocalGID: 1001,
		UIDMap:   []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
		GIDMap:   []IDMapEntry{{RemoteID: 0, LocalID: 5001, Count: 1}},
	})
	st := fakeFileInfo{name: "x", mode: 0o644, size: 1, sys: &agentproto.FileStat{UID: 0, GID: 0}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 5000 || out.Gid != 5001 {
		t.Fatalf("map should beat override: uid/gid = %d/%d, want 5000/5001", out.Uid, out.Gid)
	}
}

func TestApplyAttrUIDMapRangeOffset(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeTransparent,
		GIDMode: IDModeTransparent,
		UIDMap:  []IDMapEntry{{RemoteID: 100, LocalID: 200, Count: 10}},
	})
	st := fakeFileInfo{name: "x", mode: 0o644, sys: &agentproto.FileStat{UID: 105, GID: 7}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 205 {
		t.Fatalf("range mapping uid = %d, want 205", out.Uid)
	}
	if out.Gid != 7 {
		t.Fatalf("unmapped gid = %d, want 7 (transparent)", out.Gid)
	}
}

func TestApplyAttrUIDMapMissEntryFallsBackToMode(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode:  IDModeOverride,
		GIDMode:  IDModeTransparent,
		LocalUID: 1000,
		UIDMap:   []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
	})
	// Remote UID 99 is not in the map → override fallback wins.
	st := fakeFileInfo{name: "x", mode: 0o644, sys: &agentproto.FileStat{UID: 99, GID: 7}}

	var out fuse.Attr
	fs.applyAttr(&out, st)

	if out.Uid != 1000 {
		t.Fatalf("uid = %d, want 1000 (override fallback)", out.Uid)
	}
	if out.Gid != 7 {
		t.Fatalf("gid = %d, want 7 (transparent fallback)", out.Gid)
	}
}

func TestChownUIDMapTranslatesLocalToRemote(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/f.txt": []byte("d")}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeOverride,
		LocalUID: 1000, LocalGID: 1001,
		UIDMap: []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
		GIDMap: []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
	})

	if errno := fs.Chown(context.Background(), "f.txt", 5000, 5000); errno != 0 {
		t.Fatalf("Chown mapped: errno = %v", errno)
	}
	got, ok := client.owners["/workspace/f.txt"]
	if !ok {
		t.Fatalf("mapped chown should forward to backend, got %v", client.owners)
	}
	if got != [2]int{0, 0} {
		t.Fatalf("chown forwarded as %v, want [0 0]", got)
	}
}

func TestChownUIDMapMissOverrideStillNoops(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/f.txt": []byte("d")}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeOverride,
		LocalUID: 1000, LocalGID: 1001,
		UIDMap: []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
	})

	// uid=9999 is not in the map; override mode means preserve remote.
	if errno := fs.Chown(context.Background(), "f.txt", 9999, 9999); errno != 0 {
		t.Fatalf("Chown unmapped override: errno = %v", errno)
	}
	if _, recorded := client.owners["/workspace/f.txt"]; recorded {
		t.Fatalf("unmapped override should not forward chown, got %v", client.owners)
	}
}

func TestChownUIDMapMissTransparentForwards(t *testing.T) {
	client := &fakeSFTPClient{files: map[string][]byte{"/workspace/f.txt": []byte("d")}}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeTransparent, GIDMode: IDModeTransparent,
		UIDMap: []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
	})

	if errno := fs.Chown(context.Background(), "f.txt", 9999, 9999); errno != 0 {
		t.Fatalf("Chown unmapped transparent: errno = %v", errno)
	}
	got, ok := client.owners["/workspace/f.txt"]
	if !ok {
		t.Fatalf("unmapped transparent should forward chown, got %v", client.owners)
	}
	if got != [2]int{9999, 9999} {
		t.Fatalf("chown forwarded as %v, want [9999 9999]", got)
	}
}

func TestChownUIDMapMixedOnePerDimension(t *testing.T) {
	client := &fakeSFTPClient{
		files:   map[string][]byte{"/workspace/f.txt": []byte("d")},
		owners:  map[string][2]int{},
		fileSys: map[string]any{"/workspace/f.txt": &agentproto.FileStat{UID: 42, GID: 7}},
	}
	// UID mapped (local 5000 → remote 0), GID unmapped, gid_mode override
	// (preserve remote 7).
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeOverride, GIDMode: IDModeOverride,
		LocalUID: 1000, LocalGID: 1001,
		UIDMap: []IDMapEntry{{RemoteID: 0, LocalID: 5000, Count: 1}},
	})

	if errno := fs.Chown(context.Background(), "f.txt", 5000, 9999); errno != 0 {
		t.Fatalf("Chown mixed: errno = %v", errno)
	}
	got, ok := client.owners["/workspace/f.txt"]
	if !ok {
		t.Fatalf("mixed chown should forward to backend")
	}
	if got != [2]int{0, 7} {
		t.Fatalf("chown forwarded as %v, want [0 7]", got)
	}
}

func TestAccessTransparentRejectsForeignUser(t *testing.T) {
	client := &fakeSFTPClient{}
	fs := New(client, "/workspace", nil, &Options{
		UIDMode: IDModeTransparent, GIDMode: IDModeTransparent,
	})
	st := fakeFileInfo{name: "x", mode: 0o600, sys: &agentproto.FileStat{UID: 0, GID: 0}}
	ctx := &fuse.Context{Caller: fuse.Caller{Owner: fuse.Owner{Uid: 1000, Gid: 1001}}}

	if errno := access(ctx, fs, st, uint32(fuse.R_OK)); errno != syscall.EACCES {
		t.Fatalf("access(transparent, owner-only file): errno = %v, want EACCES", errno)
	}
}
