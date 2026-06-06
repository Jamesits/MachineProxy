package workspacefs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/remote"
)

// IDMode controls how a single ID dimension (uid or gid) is translated
// between the remote backend and the local FUSE mount. See
// Options.UIDMode / Options.GIDMode.
type IDMode string

const (
	// IDModeTransparent forwards the remote-reported ID through to the
	// FUSE client unchanged, and forwards chown calls verbatim.
	IDModeTransparent IDMode = "transparent"
	// IDModeOverride substitutes the local user's UID/GID on every stat
	// reply and turns chown into a no-op on that dimension. Useful when
	// the remote runs as a different user (often root) than the local
	// process: without it, default_permissions in the kernel denies the
	// local user access to files the remote claims root owns.
	IDModeOverride IDMode = "override"
)

// IDMapEntry describes one contiguous translation between the remote
// and local UID/GID spaces. A UID/GID r matches when
// RemoteID <= r < RemoteID+Count, and is reported locally as
// LocalID + (r - RemoteID). The reverse mapping is used to translate
// chown values from the local view back to the remote backend.
type IDMapEntry struct {
	RemoteID uint32
	LocalID  uint32
	Count    uint32
}

// Options configures FileSystem behaviour that is not derivable from
// the remote.FileClient alone.
type Options struct {
	// UIDMode selects the fallback UID translation when no UIDMap entry
	// covers the ID being translated. Empty value defaults to
	// IDModeOverride.
	UIDMode IDMode
	// GIDMode is the GID counterpart to UIDMode.
	GIDMode IDMode
	// UIDMap is consulted before falling back to UIDMode. The first
	// matching entry wins; entries are searched in order.
	UIDMap []IDMapEntry
	// GIDMap is the GID counterpart to UIDMap.
	GIDMap []IDMapEntry
	// LocalUID is the UID to report when UIDMode == IDModeOverride and
	// no UIDMap entry matches.
	LocalUID uint32
	// LocalGID is the GID counterpart to LocalUID.
	LocalGID uint32
}

// FileSystem backs a FUSE mount with a remote.FileClient. Each op is
// translated into the corresponding FileClient method call.
type FileSystem struct {
	root     string
	files    remote.FileClient
	log      *slog.Logger
	uidMode  IDMode
	gidMode  IDMode
	uidMap   []IDMapEntry
	gidMap   []IDMapEntry
	localUID uint32
	localGID uint32
}

// New constructs a FileSystem backed by files, rooted at root. opts may
// be nil to accept the default override mapping with localUID/localGID
// both zero (mostly useful for tests; production callers should set
// the local IDs explicitly).
func New(files remote.FileClient, root string, log *slog.Logger, opts *Options) *FileSystem {
	cleanRoot := path.Clean(root)
	if cleanRoot == "." {
		cleanRoot = "/"
	}
	if log == nil {
		log = slog.Default()
	}
	fs := &FileSystem{
		root:    cleanRoot,
		files:   files,
		log:     log,
		uidMode: IDModeOverride,
		gidMode: IDModeOverride,
	}
	if opts != nil {
		if opts.UIDMode != "" {
			fs.uidMode = opts.UIDMode
		}
		if opts.GIDMode != "" {
			fs.gidMode = opts.GIDMode
		}
		fs.uidMap = opts.UIDMap
		fs.gidMap = opts.GIDMap
		fs.localUID = opts.LocalUID
		fs.localGID = opts.LocalGID
	}
	return fs
}

// mapToLocal scans entries for a range covering remote and returns the
// translated local ID. ok=false means no entry matched.
func mapToLocal(entries []IDMapEntry, remote uint32) (uint32, bool) {
	for _, e := range entries {
		if e.Count == 0 {
			continue
		}
		if remote >= e.RemoteID && remote-e.RemoteID < e.Count {
			return e.LocalID + (remote - e.RemoteID), true
		}
	}
	return 0, false
}

// mapToRemote is the reverse of mapToLocal: translate a local-view ID
// back to the remote space. Used when forwarding chown.
func mapToRemote(entries []IDMapEntry, local uint32) (uint32, bool) {
	for _, e := range entries {
		if e.Count == 0 {
			continue
		}
		if local >= e.LocalID && local-e.LocalID < e.Count {
			return e.RemoteID + (local - e.LocalID), true
		}
	}
	return 0, false
}

// applyAttr fills out from st and applies the configured uid/gid
// mapping. All FUSE attr-emitting paths funnel through this helper so
// the translation is enforced in exactly one place. UIDMap/GIDMap take
// precedence over UIDMode/GIDMode; the mode only governs IDs that no
// map entry covers.
func (f *FileSystem) applyAttr(out *fuse.Attr, st os.FileInfo) {
	applyFileInfo(out, st)
	if mapped, ok := mapToLocal(f.uidMap, out.Uid); ok {
		out.Uid = mapped
	} else if f.uidMode == IDModeOverride {
		out.Uid = f.localUID
	}
	if mapped, ok := mapToLocal(f.gidMap, out.Gid); ok {
		out.Gid = mapped
	} else if f.gidMode == IDModeOverride {
		out.Gid = f.localGID
	}
}

// viewUID returns the UID that the local FUSE side sees for st under
// the current mapping. Used by the in-process permission check (which
// must agree with what the kernel will see via default_permissions).
func (f *FileSystem) viewUID(sys any) uint32 {
	remote := currentUID(sys)
	if mapped, ok := mapToLocal(f.uidMap, remote); ok {
		return mapped
	}
	if f.uidMode == IDModeOverride {
		return f.localUID
	}
	return remote
}

// viewGID is the GID counterpart to viewUID.
func (f *FileSystem) viewGID(sys any) uint32 {
	remote := currentGID(sys)
	if mapped, ok := mapToLocal(f.gidMap, remote); ok {
		return mapped
	}
	if f.gidMode == IDModeOverride {
		return f.localGID
	}
	return remote
}

func (f *FileSystem) ReadFile(ctx context.Context, rel string, off int64, size int) ([]byte, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse read", "path", rel, "offset", off, "size", size)

	abs := f.absPath(rel)
	fh, err := f.files.Open(abs)
	if err != nil {
		f.log.Warn("fuse read open failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	defer func() {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse read close failed", "path", rel, "error", cerr)
		}
	}()

	if size <= 0 {
		return []byte{}, 0
	}

	buf := make([]byte, size)
	n, err := fh.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		f.log.Warn("fuse read failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return buf[:n], 0
}

func (f *FileSystem) Stat(ctx context.Context, rel string) (os.FileInfo, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse stat", "path", rel)
	st, err := f.files.Lstat(f.absPath(rel))
	if err != nil {
		// ENOENT is the normal answer to "does this exist?" probes
		// during Lookup; keep it visible without flooding warning logs.
		if errors.Is(err, os.ErrNotExist) {
			f.log.Debug("fuse stat missing", "path", rel, "error", err)
		} else {
			f.log.Warn("fuse stat failed", "path", rel, "error", err)
		}
		return nil, toErrno(err)
	}
	return st, 0
}

func (f *FileSystem) ReadDir(ctx context.Context, rel string) ([]os.FileInfo, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse readdir", "path", rel)
	entries, err := f.files.ReadDir(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse readdir failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return entries, 0
}

func (f *FileSystem) Readlink(ctx context.Context, rel string) ([]byte, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse readlink", "path", rel)
	target, err := f.files.Readlink(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse readlink failed", "path", rel, "error", err)
		return nil, toErrno(err)
	}
	return []byte(target), 0
}

func (f *FileSystem) WriteFile(ctx context.Context, rel string, data []byte, off int64) (uint32, syscall.Errno) {
	f.log.Log(ctx, logging.LevelTrace, "fuse write", "path", rel, "offset", off, "size", len(data))
	abs := f.absPath(rel)
	fh, err := f.files.OpenFile(abs, os.O_WRONLY, 0)
	if err != nil {
		f.log.Warn("fuse write open failed", "path", rel, "error", err)
		return 0, toErrno(err)
	}
	n, err := fh.WriteAt(data, off)
	if err != nil {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse write close failed after write error", "path", rel, "error", cerr)
		}
		f.log.Warn("fuse write failed", "path", rel, "error", err)
		return uint32(n), toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse write close failed", "path", rel, "error", cerr)
		return uint32(n), toErrno(cerr)
	}
	return uint32(n), 0
}

func (f *FileSystem) CreateFile(ctx context.Context, rel string, flags uint32, mode uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse create", "path", rel, "flags", flags, "mode", mode)
	abs := f.absPath(rel)
	openFlags := int(flags) | os.O_CREATE
	fh, err := f.files.OpenFile(abs, openFlags, posixFileMode(mode))
	if err != nil {
		f.log.Warn("fuse create failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse create close failed", "path", rel, "error", cerr)
		return toErrno(cerr)
	}
	return 0
}

func (f *FileSystem) MkDir(ctx context.Context, rel string, mode uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse mkdir", "path", rel, "mode", mode)
	if err := f.files.Mkdir(f.absPath(rel), posixFileMode(mode)); err != nil {
		f.log.Warn("fuse mkdir failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Unlink(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse unlink", "path", rel)
	st, errno := f.Stat(ctx, rel)
	if errno != 0 {
		return errno
	}
	if st.IsDir() {
		f.log.Warn("fuse unlink rejected directory", "path", rel)
		return syscall.EISDIR
	}
	if err := f.files.Remove(f.absPath(rel)); err != nil {
		f.log.Warn("fuse unlink failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Rmdir(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse rmdir", "path", rel)
	st, errno := f.Stat(ctx, rel)
	if errno != 0 {
		return errno
	}
	if !st.IsDir() {
		f.log.Warn("fuse rmdir rejected non-directory", "path", rel)
		return syscall.ENOTDIR
	}
	if err := f.files.Remove(f.absPath(rel)); err != nil {
		f.log.Warn("fuse rmdir failed", "path", rel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Rename(ctx context.Context, oldRel, newRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse rename", "from", oldRel, "to", newRel)
	if err := f.files.Rename(f.absPath(oldRel), f.absPath(newRel)); err != nil {
		f.log.Warn("fuse rename failed", "from", oldRel, "to", newRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Symlink(ctx context.Context, target, linkRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse symlink", "target", target, "link", linkRel)
	if err := f.files.Symlink(target, f.absPath(linkRel)); err != nil {
		f.log.Warn("fuse symlink failed", "target", target, "link", linkRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Link(ctx context.Context, oldRel, newRel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse link", "from", oldRel, "to", newRel)
	if err := f.files.Link(f.absPath(oldRel), f.absPath(newRel)); err != nil {
		f.log.Warn("fuse link failed", "from", oldRel, "to", newRel, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Chmod(ctx context.Context, rel string, mode os.FileMode) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chmod", "path", rel, "mode", mode)
	if err := f.files.Chmod(f.absPath(rel), mode); err != nil {
		f.log.Warn("fuse chmod failed", "path", rel, "mode", mode, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Chown(ctx context.Context, rel string, uid, gid uint32) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chown", "path", rel, "uid", uid, "gid", gid)
	// For each dimension, decide which remote value (if any) to forward.
	// Map hit  → translate local → remote and forward the mapped value.
	// Override → preserve the remote-side ownership (no-op for this
	//            dimension; the local view was fabricated).
	// Transparent → forward the user-supplied value verbatim.
	finalUID, uidAction := f.resolveChownUID(uid)
	finalGID, gidAction := f.resolveChownGID(gid)
	if uidAction == chownNoop && gidAction == chownNoop {
		f.log.Debug("fuse chown: both dimensions are no-op", "path", rel, "uid", uid, "gid", gid)
		return 0
	}
	if uidAction == chownNoop || gidAction == chownNoop {
		st, errno := f.Stat(ctx, rel)
		if errno != 0 {
			return errno
		}
		if uidAction == chownNoop {
			finalUID = currentUID(st.Sys())
		}
		if gidAction == chownNoop {
			finalGID = currentGID(st.Sys())
		}
	}
	if err := f.files.Chown(f.absPath(rel), int(finalUID), int(finalGID)); err != nil {
		f.log.Warn("fuse chown failed", "path", rel, "uid", finalUID, "gid", finalGID, "error", err)
		return toErrno(err)
	}
	return 0
}

// chownDecision selects how a single dimension of a chown call should
// be forwarded to the remote backend.
type chownDecision int

const (
	// chownForward sends the user-supplied (or mapped) value through to
	// the backend.
	chownForward chownDecision = iota
	// chownNoop preserves the remote-side ID; the caller fills in the
	// current remote value from a fresh Stat before issuing the chown.
	chownNoop
)

func (f *FileSystem) resolveChownUID(uid uint32) (uint32, chownDecision) {
	if mapped, ok := mapToRemote(f.uidMap, uid); ok {
		return mapped, chownForward
	}
	if f.uidMode == IDModeOverride {
		return 0, chownNoop
	}
	return uid, chownForward
}

func (f *FileSystem) resolveChownGID(gid uint32) (uint32, chownDecision) {
	if mapped, ok := mapToRemote(f.gidMap, gid); ok {
		return mapped, chownForward
	}
	if f.gidMode == IDModeOverride {
		return 0, chownNoop
	}
	return gid, chownForward
}

func (f *FileSystem) Chtimes(ctx context.Context, rel string, atime, mtime time.Time) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse chtimes", "path", rel, "atime", atime, "mtime", mtime)
	if err := f.files.Chtimes(f.absPath(rel), atime, mtime); err != nil {
		f.log.Warn("fuse chtimes failed", "path", rel, "atime", atime, "mtime", mtime, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Truncate(ctx context.Context, rel string, size int64) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse truncate", "path", rel, "size", size)
	if err := f.files.Truncate(f.absPath(rel), size); err != nil {
		f.log.Warn("fuse truncate failed", "path", rel, "size", size, "error", err)
		return toErrno(err)
	}
	return 0
}

func (f *FileSystem) Statfs(ctx context.Context, rel string, out *remote.Statfs) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse statfs", "path", rel)
	st, err := f.files.Statfs(f.absPath(rel))
	if err != nil {
		f.log.Warn("fuse statfs failed", "path", rel, "error", err)
		return toErrno(err)
	}
	*out = *st
	return 0
}

// Fsync is the path-based fallback used when FUSE does not provide one
// of our open file handles. It syncs the file currently named by rel,
// so callers that have a specific file descriptor should prefer syncing
// that handle directly.
//
// Backends whose protocol cannot express fsync are expected to make
// Sync a successful no-op; real open/sync failures are returned to the
// kernel.
func (f *FileSystem) Fsync(ctx context.Context, rel string) syscall.Errno {
	f.log.Log(ctx, logging.LevelTrace, "fuse fsync", "path", rel)
	abs := f.absPath(rel)
	fh, err := f.files.Open(abs)
	if err != nil {
		f.log.Warn("fuse fsync open failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if err := fh.Sync(); err != nil {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("fuse fsync close failed after sync error", "path", rel, "error", cerr)
		}
		f.log.Warn("fuse fsync failed", "path", rel, "error", err)
		return toErrno(err)
	}
	if cerr := fh.Close(); cerr != nil {
		f.log.Warn("fuse fsync close failed", "path", rel, "error", cerr)
		return toErrno(cerr)
	}
	return 0
}

func (f *FileSystem) absPath(rel string) string {
	clean := path.Clean("/" + rel)
	if clean == "/" {
		return f.root
	}
	return path.Join(f.root, strings.TrimPrefix(clean, "/"))
}

func posixFileMode(mode uint32) os.FileMode {
	out := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		out |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		out |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		out |= os.ModeSticky
	}
	return out
}

func toErrno(err error) syscall.Errno {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if errors.As(pathErr.Err, &errno) {
			return errno
		}
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrInvalid):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
