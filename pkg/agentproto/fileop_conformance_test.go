package agentproto

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"
)

type fileOpCase struct {
	name string
	run  func(t *testing.T, server *FileOpServer, root string)
}

func TestFileOpServerFilesystemConformance(t *testing.T) {
	oldUmask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldUmask) })

	cases := []fileOpCase{
		{name: "OpenReadEOFAndClose", run: testFileOpOpenReadEOFAndClose},
		{name: "OpenFileWriteFstatFtruncate", run: testFileOpOpenFileWriteFstatFtruncate},
		{name: "DirectoryMetadataAndStatfs", run: testFileOpDirectoryMetadataAndStatfs},
		{name: "RenameLinkSymlinkAndReadDir", run: testFileOpRenameLinkSymlinkAndReadDir},
		{name: "ErrorsAndUnknownOps", run: testFileOpErrorsAndUnknownOps},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			server := NewFileOpServer()
			t.Cleanup(server.CloseAll)
			tc.run(t, server, root)
		})
	}
}

func testFileOpOpenReadEOFAndClose(t *testing.T, server *FileOpServer, root string) {
	path := filepath.Join(root, "read.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	open := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpOpen, Path: path}))
	read := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpReadAt, Handle: open.Handle, Offset: 2, Size: 10}))
	if string(read.Data) != "cdef" || read.N != 4 || !read.EOF {
		t.Fatalf("ReadAt response = data %q n=%d eof=%v, want cdef/4/true", read.Data, read.N, read.EOF)
	}
	zero := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpReadAt, Handle: open.Handle, Offset: 0, Size: 0}))
	if len(zero.Data) != 0 || zero.N != 0 || zero.EOF {
		t.Fatalf("zero-size ReadAt = data %q n=%d eof=%v, want empty/0/false", zero.Data, zero.N, zero.EOF)
	}
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpClose, Handle: open.Handle}))
	bad := server.Handle(&FileOpReq{Op: FileOpReadAt, Handle: open.Handle, Size: 1})
	if bad.Errno != uint32(syscall.EBADF) {
		t.Fatalf("ReadAt after Close errno = %d, want EBADF", bad.Errno)
	}
}

func testFileOpOpenFileWriteFstatFtruncate(t *testing.T, server *FileOpServer, root string) {
	path := filepath.Join(root, "write.txt")
	open := requireFileOpOK(t, server.Handle(&FileOpReq{
		Op:    FileOpOpenFile,
		Path:  path,
		Flags: int32(os.O_CREATE | os.O_RDWR | os.O_EXCL),
		Mode:  0o640,
	}))

	write := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpWriteAt, Handle: open.Handle, Offset: 0, Data: []byte("hello world")}))
	if write.N != int64(len("hello world")) {
		t.Fatalf("WriteAt N = %d, want %d", write.N, len("hello world"))
	}
	fstat := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpFstat, Handle: open.Handle}))
	if fstat.Stat == nil || fstat.Stat.Size != int64(len("hello world")) || fstat.Stat.Mode&0o777 != 0o640 {
		t.Fatalf("Fstat = %+v, want size=%d mode=640", fstat.Stat, len("hello world"))
	}
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpFtruncate, Handle: open.Handle, Size: 5}))
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpFsync, Handle: open.Handle}))
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpClose, Handle: open.Handle}))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backing file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("backing contents = %q, want hello", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat backing file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("backing mode = %o, want 640", got)
	}
}

func testFileOpDirectoryMetadataAndStatfs(t *testing.T, server *FileOpServer, root string) {
	dir := filepath.Join(root, "dir", "nested")
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpMkdirAll, Path: dir, Mode: 0o750}))
	stat := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpStat, Path: dir}))
	if stat.Stat == nil || !stat.Stat.IsDir || stat.Stat.Mode&0o777 != 0o750 {
		t.Fatalf("Stat dir = %+v, want directory mode 750", stat.Stat)
	}

	file := filepath.Join(dir, "meta.txt")
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpOpenFile, Path: file, Flags: int32(os.O_CREATE | os.O_WRONLY), Mode: 0o600}))
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpChmod, Path: file, Mode: 0o644}))
	mtime := time.Unix(1_700_000_000, 456_000_000)
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpChtimes, Path: file, AtimeNanos: mtime.UnixNano(), MTimeNanos: mtime.UnixNano()}))
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpTruncate, Path: file, Size: 3}))
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat backing file: %v", err)
	}
	if info.Size() != 3 || info.Mode().Perm() != 0o644 {
		t.Fatalf("backing metadata size=%d mode=%o, want size=3 mode=644", info.Size(), info.Mode().Perm())
	}

	statfs := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpStatfs, Path: root}))
	if statfs.Statfs == nil || statfs.Statfs.Bsize == 0 || statfs.Statfs.NameLen == 0 {
		t.Fatalf("Statfs = %+v, want non-zero bsize and namelen", statfs.Statfs)
	}
}

func testFileOpRenameLinkSymlinkAndReadDir(t *testing.T, server *FileOpServer, root string) {
	dir := filepath.Join(root, "links")
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpMkdir, Path: dir, Mode: 0o755}))
	original := filepath.Join(dir, "original.txt")
	if err := os.WriteFile(original, []byte("content"), 0o644); err != nil {
		t.Fatalf("seed original: %v", err)
	}

	renamed := filepath.Join(dir, "renamed.txt")
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpRename, Path: original, NewPath: renamed}))
	if _, err := os.Stat(original); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat old path error = %v, want ENOENT", err)
	}
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpLink, Path: renamed, NewPath: filepath.Join(dir, "hard.txt")}))
	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpSymlink, Path: "renamed.txt", NewPath: filepath.Join(dir, "sym.txt")}))
	readlink := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpReadlink, Path: filepath.Join(dir, "sym.txt")}))
	if readlink.Path != "renamed.txt" {
		t.Fatalf("Readlink path = %q, want renamed.txt", readlink.Path)
	}
	lstat := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpLstat, Path: filepath.Join(dir, "sym.txt")}))
	if lstat.Stat == nil || lstat.Stat.Mode&uint32(os.ModeSymlink) == 0 {
		t.Fatalf("Lstat symlink = %+v, want symlink mode", lstat.Stat)
	}

	readdir := requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpReadDir, Path: dir}))
	names := make([]string, 0, len(readdir.Entries))
	for _, entry := range readdir.Entries {
		names = append(names, entry.Stat.Name)
	}
	sort.Strings(names)
	if got := names; len(got) != 3 || got[0] != "hard.txt" || got[1] != "renamed.txt" || got[2] != "sym.txt" {
		t.Fatalf("ReadDir names = %v, want [hard.txt renamed.txt sym.txt]", names)
	}

	requireFileOpOK(t, server.Handle(&FileOpReq{Op: FileOpRemove, Path: filepath.Join(dir, "hard.txt")}))
	if _, err := os.Stat(filepath.Join(dir, "hard.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat removed hard link error = %v, want ENOENT", err)
	}
}

func testFileOpErrorsAndUnknownOps(t *testing.T, server *FileOpServer, root string) {
	missing := server.Handle(&FileOpReq{Op: FileOpOpen, Path: filepath.Join(root, "missing")})
	if missing.Errno != uint32(syscall.ENOENT) {
		t.Fatalf("Open missing errno = %d, want ENOENT", missing.Errno)
	}
	badHandle := server.Handle(&FileOpReq{Op: FileOpClose, Handle: 999})
	if badHandle.Errno != uint32(syscall.EBADF) {
		t.Fatalf("Close bad handle errno = %d, want EBADF", badHandle.Errno)
	}
	unknown := server.Handle(&FileOpReq{Op: FileOp(255)})
	if unknown.Errno != uint32(syscall.ENOSYS) {
		t.Fatalf("unknown op errno = %d, want ENOSYS", unknown.Errno)
	}
}

func requireFileOpOK(t *testing.T, resp *FileOpResp) *FileOpResp {
	t.Helper()
	if resp.Errno != 0 {
		t.Fatalf("FileOp errno = %d (%s), want 0", resp.Errno, resp.ErrMsg)
	}
	return resp
}
