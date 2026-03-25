package workspacefs

import (
	"context"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type dirNode struct {
	fs.Inode
	backend *FileSystem
	relPath string
}

func Mount(ctx context.Context, backend *FileSystem, mountpoint string) (*fuse.Server, error) {
	root := &dirNode{backend: backend, relPath: ""}

	server, err := fs.Mount(mountpoint, root, &fs.Options{
		MountOptions: fuse.MountOptions{
			AllowOther: false,
			FsName:     "machineproxy-sftp",
			Name:       "machineproxy",
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
	st, errno := n.backend.Stat(n.relPath)
	if errno != 0 {
		return errno
	}
	applyFileInfo(&out.Attr, st)
	if out.Mode&uint32(syscall.S_IFMT) == 0 {
		out.Mode = (out.Mode &^ uint32(syscall.S_IFMT)) | fuse.S_IFDIR
	}
	return 0
}

func (n *dirNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	st, errno := n.backend.Stat(rel)
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
	applyFileInfo(&out.Attr, st)
	out.AttrValid = 1
	return inode, 0
}

func (n *dirNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	if errno := n.backend.CreateFile(ctx, rel); errno != 0 {
		return nil, nil, 0, errno
	}
	st, errno := n.backend.Stat(rel)
	if errno != 0 {
		return nil, nil, 0, errno
	}
	child := &fileNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFREG})
	applyFileInfo(&out.Attr, st)
	return inode, nil, fuse.FOPEN_DIRECT_IO, 0
}

func (n *dirNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	rel := path.Join(n.relPath, name)
	if errno := n.backend.MkDir(ctx, rel); errno != 0 {
		return nil, errno
	}
	st, errno := n.backend.Stat(rel)
	if errno != 0 {
		return nil, errno
	}
	child := &dirNode{backend: n.backend, relPath: rel}
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: fuse.S_IFDIR})
	applyFileInfo(&out.Attr, st)
	return inode, 0
}

func (n *dirNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.backend.Unlink(ctx, path.Join(n.relPath, name))
}

func (n *dirNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.backend.Rmdir(ctx, path.Join(n.relPath, name))
}

func (n *dirNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	newDir, ok := newParent.(*dirNode)
	if !ok {
		return syscall.EINVAL
	}
	return n.backend.Rename(ctx, path.Join(n.relPath, name), path.Join(newDir.relPath, newName))
}

func (n *dirNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, errno := n.backend.ReadDir(n.relPath)
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
	return fuse.S_IFREG
}

func applyFileInfo(out *fuse.Attr, st os.FileInfo) {
	out.Size = uint64(st.Size())
	out.Mode = modeToDirEntryMode(st.Mode()) | uint32(st.Mode().Perm())
	out.Mtime = uint64(st.ModTime().Unix())
	out.Mtimensec = uint32(st.ModTime().Nanosecond())
	now := time.Now()
	out.Atime = uint64(now.Unix())
	out.Ctime = out.Mtime
	out.Ctimensec = out.Mtimensec
}
