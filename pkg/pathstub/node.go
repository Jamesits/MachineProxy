//go:build linux || darwin || freebsd

package pathstub

import (
	"context"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// dirNode is the read-only root of the path-stub FUSE filesystem. It
// has no nested directories; all stubs are direct children.
type dirNode struct {
	fs.Inode
	backend *FileSystem
}

// Mount mounts backend as a read-only FUSE filesystem at mountpoint.
// The returned server is unmounted automatically when ctx is canceled,
// or callers may call server.Unmount() directly.
func Mount(ctx context.Context, backend *FileSystem, mountpoint string) (*fuse.Server, error) {
	root := &dirNode{backend: backend}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "machineproxy-pathstub",
			Name:       "machineproxy-pathstub",
			// Mount as read-only; the kernel will reject write ops
			// before they reach our node handlers as a fast path.
			Options: []string{"ro"},
		},
	})
	if err != nil {
		return nil, err
	}
	if err := server.WaitMount(); err != nil {
		_ = server.Unmount()
		return nil, err
	}

	go func() {
		<-ctx.Done()
		_ = server.Unmount()
	}()

	return server, nil
}

func (n *dirNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFDIR | 0o555
	now := uint64(time.Now().Unix())
	out.Atime = now
	out.Mtime = now
	out.Ctime = now
	return 0
}

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	e, ok := n.backend.Lookup(name)
	if !ok {
		return nil, syscall.ENOENT
	}
	child := &fileNode{backend: n.backend, name: name}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG})
	applyEntryAttr(&out.Attr, e)
	out.AttrValid = 30 // entries are immutable for the lifetime of the FS
	out.EntryValid = 30
	return inode, 0
}

func (n *dirNode) Readdir(_ context.Context) (fs.DirStream, syscall.Errno) {
	names := n.backend.Names()
	dirs := make([]fuse.DirEntry, 0, len(names)+2)
	dirs = append(dirs,
		fuse.DirEntry{Name: ".", Mode: fuse.S_IFDIR},
		fuse.DirEntry{Name: "..", Mode: fuse.S_IFDIR},
	)
	for _, name := range names {
		dirs = append(dirs, fuse.DirEntry{Name: name, Mode: fuse.S_IFREG})
	}
	return fs.NewListDirStream(dirs), 0
}

// fileNode is one stub file. Reads proxy to the remote real path; all
// write-style operations return EROFS.
type fileNode struct {
	fs.Inode
	backend *FileSystem
	name    string
}

func (n *fileNode) Getattr(_ context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	e, ok := n.backend.Lookup(n.name)
	if !ok {
		return syscall.ENOENT
	}
	applyEntryAttr(&out.Attr, e)
	return 0
}

func (n *fileNode) Open(_ context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	// Reject any write/truncate flags; the FS is mounted ro but the
	// kernel can still ask us about O_RDWR before failing the syscall,
	// and being explicit here surfaces a cleaner errno to callers.
	const writeFlags = syscall.O_WRONLY | syscall.O_RDWR | syscall.O_TRUNC | syscall.O_APPEND | syscall.O_CREAT
	if flags&uint32(writeFlags) != 0 {
		return nil, 0, syscall.EROFS
	}
	return nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *fileNode) Read(ctx context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, errno := n.backend.ReadFile(ctx, n.name, off, len(dest))
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(data), 0
}

func applyEntryAttr(out *fuse.Attr, e Entry) {
	// Force regular-file bit plus the cached execute bits. We intentionally
	// drop any group/other write bits even if the remote file is world-
	// writable — this is a read-only view.
	perm := e.Mode & 0o555
	if perm == 0 {
		perm = 0o555
	}
	out.Mode = fuse.S_IFREG | perm
	out.Size = uint64(e.Size)
	if e.MTimeNanos > 0 {
		out.Mtime = uint64(e.MTimeNanos / 1e9)
		out.Mtimensec = uint32(e.MTimeNanos % 1e9)
	} else {
		now := time.Now()
		out.Mtime = uint64(now.Unix())
	}
	out.Ctime = out.Mtime
	out.Ctimensec = out.Mtimensec
	out.Atime = out.Mtime
	out.Atimensec = out.Mtimensec
	out.Nlink = 1
}
