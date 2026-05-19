package workspacefs

import (
	"context"
	"os"
	"path"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/remote"
)

type dirNode struct {
	fs.Inode
	backend *FileSystem
	relPath string
}

func Mount(ctx context.Context, backend *FileSystem, mountpoint string) (*fuse.Server, error) {
	root := &dirNode{backend: backend, relPath: ""}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		NullPermissions: true,
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "machineproxy-sftp",
			Name:       "machineproxy",
			Options:    []string{"default_permissions"},
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

func (n *dirNode) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	st, errno := n.backend.Stat(ctx, n.relPath)
	if errno != 0 {
		return errno
	}
	n.backend.applyAttr(&out.Attr, st)
	if out.Mode&uint32(syscall.S_IFMT) == 0 {
		out.Mode = (out.Mode &^ uint32(syscall.S_IFMT)) | fuse.S_IFDIR
	}
	return 0
}

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	st, errno := n.backend.Stat(ctx, rel)
	if errno != 0 {
		return nil, errno
	}

	var childOps fs.InodeEmbedder
	if st.IsDir() {
		childOps = &dirNode{backend: n.backend, relPath: rel}
	} else {
		childOps = &fileNode{backend: n.backend, relPath: rel}
	}

	attr := modeToStable(st.Mode())
	inode := n.NewInode(ctx, childOps, attr)
	n.backend.applyAttr(&out.Attr, st)
	out.AttrValid = 1
	return inode, 0
}

func (n *dirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	abs := n.backend.absPath(rel)
	fh, err := n.backend.sftp.OpenFile(abs, int(flags)|os.O_CREATE, posixFileMode(mode))
	if err != nil {
		n.backend.log.Warn("fuse create failed", "path", rel, "error", err)
		return nil, nil, 0, toErrno(err)
	}
	handle := &fileHandle{write: fh, flags: flags}
	st, errno := n.backend.Stat(ctx, rel)
	if errno != 0 {
		_ = handle.Release(ctx)
		return nil, nil, 0, errno
	}
	child := &fileNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG})
	n.backend.applyAttr(&out.Attr, st)
	return inode, handle, fuse.FOPEN_DIRECT_IO, 0
}

func (n *dirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	if errno := n.backend.MkDir(ctx, rel, mode); errno != 0 {
		return nil, errno
	}
	st, errno := n.backend.Stat(ctx, rel)
	if errno != 0 {
		return nil, errno
	}
	child := &dirNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR})
	n.backend.applyAttr(&out.Attr, st)
	return inode, 0
}

func (n *dirNode) Mknod(ctx context.Context, name string, mode uint32, dev uint32, _ *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.backend.log.Warn("fuse mknod unsupported", "path", path.Join(n.relPath, name), "mode", mode, "dev", dev)
	return nil, syscall.ENOTSUP
}

func (n *dirNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	if errno := n.backend.Symlink(ctx, target, rel); errno != 0 {
		return nil, errno
	}
	st, errno := n.backend.Stat(ctx, rel)
	if errno != 0 {
		return nil, errno
	}
	child := &fileNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFLNK})
	n.backend.applyAttr(&out.Attr, st)
	return inode, 0
}

func (n *dirNode) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	oldFile, ok := target.(*fileNode)
	if !ok {
		n.backend.log.Warn("fuse hardlink target unsupported", "new_path", path.Join(n.relPath, name), "target_type", target)
		return nil, syscall.EPERM
	}
	rel := path.Join(n.relPath, name)
	if errno := n.backend.Link(ctx, oldFile.relPath, rel); errno != 0 {
		return nil, errno
	}
	st, errno := n.backend.Stat(ctx, rel)
	if errno != 0 {
		return nil, errno
	}
	child := &fileNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, modeToStable(st.Mode()))
	n.backend.applyAttr(&out.Attr, st)
	return inode, 0
}

func (n *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.backend.Unlink(ctx, path.Join(n.relPath, name))
}

func (n *dirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.backend.Rmdir(ctx, path.Join(n.relPath, name))
}

func (n *dirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags != 0 {
		n.backend.log.Warn("fuse rename with unsupported flags", "from", path.Join(n.relPath, name), "to", newName, "flags", flags)
		return syscall.ENOTSUP
	}
	newDir, ok := newParent.(*dirNode)
	if !ok {
		n.backend.log.Warn("fuse rename target parent is not a directory", "from", path.Join(n.relPath, name), "to", newName)
		return syscall.EINVAL
	}
	return n.backend.Rename(ctx, path.Join(n.relPath, name), path.Join(newDir.relPath, newName))
}

func (n *dirNode) Opendir(ctx context.Context) syscall.Errno {
	_, errno := n.backend.Stat(ctx, n.relPath)
	return errno
}

func (n *dirNode) Access(ctx context.Context, mask uint32) syscall.Errno {
	st, errno := n.backend.Stat(ctx, n.relPath)
	if errno != 0 {
		return errno
	}
	if errno := access(ctx, n.backend, st, mask); errno != 0 {
		n.backend.log.Debug("fuse access denied", "path", n.relPath, "mask", mask, "mode", st.Mode(), "error", errno)
		return errno
	}
	return 0
}

func (n *dirNode) Fsync(ctx context.Context, _ fs.FileHandle, flags uint32) syscall.Errno {
	n.backend.log.Debug("fuse fsyncdir noop", "path", n.relPath, "flags", flags)
	return 0
}

func (n *dirNode) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	var st remote.Statfs
	if errno := n.backend.Statfs(ctx, n.relPath, &st); errno != 0 {
		return errno
	}
	out.Blocks = st.Blocks
	out.Bfree = st.Bfree
	out.Bavail = st.Bavail
	out.Files = st.Files
	out.Ffree = st.Ffree
	out.Bsize = st.Bsize
	out.Frsize = st.Frsize
	out.NameLen = st.NameLen
	return 0
}

func (n *dirNode) Getxattr(_ context.Context, attr string, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("fuse getxattr unsupported", "path", n.relPath, "attr", attr)
	return 0, fs.ENOATTR
}

func (n *dirNode) Listxattr(_ context.Context, _ []byte) (uint32, syscall.Errno) {
	n.backend.log.Debug("fuse listxattr unsupported", "path", n.relPath)
	return 0, 0
}

func (n *dirNode) Setxattr(_ context.Context, attr string, data []byte, flags uint32) syscall.Errno {
	n.backend.log.Warn("fuse setxattr unsupported", "path", n.relPath, "attr", attr, "size", len(data), "flags", flags)
	return fs.ENOATTR
}

func (n *dirNode) Removexattr(_ context.Context, attr string) syscall.Errno {
	n.backend.log.Warn("fuse removexattr unsupported", "path", n.relPath, "attr", attr)
	return fs.ENOATTR
}

func (n *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, errno := n.backend.ReadDir(ctx, n.relPath)
	if errno != 0 {
		return nil, errno
	}

	dirs := make([]fuse.DirEntry, 0, len(entries)+2)
	dirs = append(dirs,
		fuse.DirEntry{Name: ".", Mode: fuse.S_IFDIR},
		fuse.DirEntry{Name: "..", Mode: fuse.S_IFDIR},
	)

	for _, e := range entries {
		de := fuse.DirEntry{Name: e.Name(), Mode: modeToDirEntryMode(e.Mode())}
		dirs = append(dirs, de)
	}

	return fs.NewListDirStream(dirs), 0
}

func modeToStable(mode os.FileMode) fs.StableAttr {
	if mode.IsDir() {
		return fs.StableAttr{Mode: fuse.S_IFDIR}
	}
	if mode&os.ModeSymlink != 0 {
		return fs.StableAttr{Mode: fuse.S_IFLNK}
	}
	return fs.StableAttr{Mode: fuse.S_IFREG}
}

func modeToDirEntryMode(mode os.FileMode) uint32 {
	if mode.IsDir() {
		return fuse.S_IFDIR
	}
	if mode&os.ModeSymlink != 0 {
		return fuse.S_IFLNK
	}
	if mode&os.ModeNamedPipe != 0 {
		return fuse.S_IFIFO
	}
	if mode&os.ModeSocket != 0 {
		return syscall.S_IFSOCK
	}
	if mode&os.ModeDevice != 0 {
		if mode&os.ModeCharDevice != 0 {
			return syscall.S_IFCHR
		}
		return syscall.S_IFBLK
	}
	return fuse.S_IFREG
}

func applyFileInfo(out *fuse.Attr, st os.FileInfo) {
	out.Size = uint64(st.Size())
	out.Mode = fuseModeFromFileMode(st.Mode())
	out.Mtime = uint64(st.ModTime().Unix())
	out.Mtimensec = uint32(st.ModTime().Nanosecond())
	out.Atime = out.Mtime
	out.Atimensec = out.Mtimensec
	out.Ctime = out.Mtime
	out.Ctimensec = out.Mtimensec
	out.Nlink = 1
	out.Blksize = 4096
	out.Blocks = (out.Size + 511) / 512
	applyStatDetails(out, st.Sys())
}

func fuseModeFromFileMode(mode os.FileMode) uint32 {
	out := modeToDirEntryMode(mode) | uint32(mode.Perm())
	if mode&os.ModeSetuid != 0 {
		out |= 0o4000
	}
	if mode&os.ModeSetgid != 0 {
		out |= 0o2000
	}
	if mode&os.ModeSticky != 0 {
		out |= 0o1000
	}
	return out
}
