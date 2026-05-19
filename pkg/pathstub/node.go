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
		NullPermissions: true,
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "machineproxy-pathstub",
			Name:       "machineproxy-pathstub",
			// Mount as read-only; the kernel will reject write ops
			// before they reach our node handlers as a fast path.
			Options: []string{"ro", "default_permissions"},
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

func (n *dirNode) Access(_ context.Context, mask uint32) syscall.Errno {
	const writeMask = 2
	if mask&writeMask != 0 {
		n.backend.log.Debug("pathstub access denied", "mask", mask)
		return syscall.EACCES
	}
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

func (n *dirNode) Create(_ context.Context, name string, flags uint32, mode uint32, _ *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	n.backend.log.Warn("pathstub create unsupported", "name", name, "flags", flags, "mode", mode)
	return nil, nil, 0, syscall.EROFS
}

func (n *dirNode) Mkdir(_ context.Context, name string, mode uint32, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.backend.log.Warn("pathstub mkdir unsupported", "name", name, "mode", mode)
	return nil, syscall.EROFS
}

func (n *dirNode) Mknod(_ context.Context, name string, mode uint32, dev uint32, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.backend.log.Warn("pathstub mknod unsupported", "name", name, "mode", mode, "dev", dev)
	return nil, syscall.EROFS
}

func (n *dirNode) Symlink(_ context.Context, target, name string, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.backend.log.Warn("pathstub symlink unsupported", "target", target, "name", name)
	return nil, syscall.EROFS
}

func (n *dirNode) Link(_ context.Context, _ fs.InodeEmbedder, name string, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.backend.log.Warn("pathstub hardlink unsupported", "name", name)
	return nil, syscall.EROFS
}

func (n *dirNode) Unlink(_ context.Context, name string) syscall.Errno {
	n.backend.log.Warn("pathstub unlink unsupported", "name", name)
	return syscall.EROFS
}

func (n *dirNode) Rmdir(_ context.Context, name string) syscall.Errno {
	n.backend.log.Warn("pathstub rmdir unsupported", "name", name)
	return syscall.EROFS
}

func (n *dirNode) Rename(_ context.Context, name string, _ fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	n.backend.log.Warn("pathstub rename unsupported", "from", name, "to", newName, "flags", flags)
	return syscall.EROFS
}

func (n *dirNode) Fsync(_ context.Context, _ fs.FileHandle, flags uint32) syscall.Errno {
	n.backend.log.Debug("pathstub fsyncdir noop", "flags", flags)
	return 0
}

func (n *dirNode) Getxattr(_ context.Context, attr string, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("pathstub getxattr unsupported", "attr", attr)
	return 0, fs.ENOATTR
}

func (n *dirNode) Listxattr(_ context.Context, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("pathstub listxattr unsupported")
	return 0, 0
}

func (n *dirNode) Setxattr(_ context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	n.backend.log.Warn("pathstub setxattr unsupported", "attr", attr, "size", len(data), "flags", flags)
	return fs.ENOATTR
}

func (n *dirNode) Removexattr(_ context.Context, attr string) syscall.Errno {
	n.backend.log.Warn("pathstub removexattr unsupported", "attr", attr)
	return fs.ENOATTR
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

func (n *fileNode) Access(_ context.Context, mask uint32) syscall.Errno {
	const writeMask = 2
	if mask&writeMask != 0 {
		n.backend.log.Debug("pathstub access denied", "name", n.name, "mask", mask)
		return syscall.EACCES
	}
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

func (n *fileNode) Write(_ context.Context, _ fs.FileHandle, _ []byte, off int64) (uint32, syscall.Errno) {
	n.backend.log.Warn("pathstub write unsupported", "name", n.name, "offset", off)
	return 0, syscall.EROFS
}

func (n *fileNode) Read(ctx context.Context, _ fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	data, errno := n.backend.ReadFile(ctx, n.name, off, len(dest))
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(data), 0
}

func (n *fileNode) Setattr(_ context.Context, _ fs.FileHandle, in *fuse.SetAttrIn, _ *fuse.AttrOut) syscall.Errno {
	n.backend.log.Warn("pathstub setattr unsupported", "name", n.name, "valid", in.Valid)
	return syscall.EROFS
}

func (n *fileNode) Fsync(_ context.Context, _ fs.FileHandle, flags uint32) syscall.Errno {
	n.backend.log.Debug("pathstub fsync noop", "name", n.name, "flags", flags)
	return 0
}

func (n *fileNode) Getxattr(_ context.Context, attr string, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("pathstub getxattr unsupported", "name", n.name, "attr", attr)
	return 0, fs.ENOATTR
}

func (n *fileNode) Listxattr(_ context.Context, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("pathstub listxattr unsupported", "name", n.name)
	return 0, 0
}

func (n *fileNode) Setxattr(_ context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	n.backend.log.Warn("pathstub setxattr unsupported", "name", n.name, "attr", attr, "size", len(data), "flags", flags)
	return fs.ENOATTR
}

func (n *fileNode) Removexattr(_ context.Context, attr string) syscall.Errno {
	n.backend.log.Warn("pathstub removexattr unsupported", "name", n.name, "attr", attr)
	return fs.ENOATTR
}

func (n *fileNode) Allocate(_ context.Context, _ fs.FileHandle, off uint64, size uint64, mode uint32) syscall.Errno {
	n.backend.log.Warn("pathstub fallocate unsupported", "name", n.name, "offset", off, "size", size, "mode", mode)
	return syscall.EROFS
}

func (n *fileNode) CopyFileRange(_ context.Context, _ fs.FileHandle, offIn uint64, _ *fs.Inode, _ fs.FileHandle, offOut uint64, length uint64, flags uint64) (uint32, syscall.Errno) {
	n.backend.log.Warn("pathstub copy_file_range unsupported", "name", n.name, "off_in", offIn, "off_out", offOut, "length", length, "flags", flags)
	return 0, syscall.ENOTSUP
}

func (n *fileNode) Getlk(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) syscall.Errno {
	n.backend.log.Warn("pathstub getlk unsupported", "name", n.name, "owner", owner, "lock", lk, "flags", flags, "out", out)
	return syscall.ENOTSUP
}

func (n *fileNode) Setlk(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno {
	n.backend.log.Warn("pathstub setlk unsupported", "name", n.name, "owner", owner, "lock", lk, "flags", flags)
	return syscall.ENOTSUP
}

func (n *fileNode) Setlkw(_ context.Context, _ fs.FileHandle, owner uint64, lk *fuse.FileLock, flags uint32) syscall.Errno {
	n.backend.log.Warn("pathstub setlkw unsupported", "name", n.name, "owner", owner, "lock", lk, "flags", flags)
	return syscall.ENOTSUP
}

func (n *fileNode) Ioctl(_ context.Context, _ fs.FileHandle, cmd uint32, arg uint64, input []byte, output []byte) (int32, syscall.Errno) {
	n.backend.log.Warn("pathstub ioctl unsupported", "name", n.name, "cmd", cmd, "arg", arg, "input_size", len(input), "output_size", len(output))
	return 0, syscall.ENOTTY
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
