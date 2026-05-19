package pathstub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sort"
	"syscall"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/logging"
	"github.com/jamesits/machineproxy/pkg/remote"
)

// RemoteOpener opens a remote file by absolute path for reading. Today
// it is satisfied by remote.FileClient and any narrower interface that
// exposes Open(path) → (remote.RemoteFile, error).
type RemoteOpener interface {
	Open(path string) (remote.RemoteFile, error)
}

// Entry is one immutable stub entry.
type Entry struct {
	Name       string
	RemotePath string
	Mode       uint32
	Size       int64
	MTimeNanos int64
}

// FileSystem holds the immutable stub table plus the SFTP opener used
// for read-proxying. It is safe for concurrent use.
type FileSystem struct {
	opener  RemoteOpener
	entries map[string]Entry
	names   []string // sorted for deterministic Readdir output
	log     *slog.Logger
}

// New constructs a FileSystem from enumeration entries. The entries map
// is copied so the caller can mutate the source slice afterwards.
func New(opener RemoteOpener, info []agentproto.PathInfoEntry, log *slog.Logger) *FileSystem {
	if log == nil {
		log = slog.Default()
	}
	entries := make(map[string]Entry, len(info))
	names := make([]string, 0, len(info))
	for _, e := range info {
		if _, dup := entries[e.Name]; dup {
			continue
		}
		entries[e.Name] = Entry{
			Name:       e.Name,
			RemotePath: e.RemotePath,
			Mode:       e.Mode,
			Size:       e.Size,
			MTimeNanos: e.MTimeNanos,
		}
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return &FileSystem{opener: opener, entries: entries, names: names, log: log}
}

// Names returns the sorted list of stub names.
func (f *FileSystem) Names() []string {
	out := make([]string, len(f.names))
	copy(out, f.names)
	return out
}

// Entries returns a copy of the underlying map. Useful for building the
// broker's path mapper (stub-path → remote-path) without exposing the
// raw map.
func (f *FileSystem) Entries() map[string]Entry {
	out := make(map[string]Entry, len(f.entries))
	for k, v := range f.entries {
		out[k] = v
	}
	return out
}

// Lookup returns the entry for name and whether it exists.
func (f *FileSystem) Lookup(name string) (Entry, bool) {
	e, ok := f.entries[name]
	return e, ok
}

// ReadFile proxies a read of name to its remote path. It mirrors
// workspacefs.FileSystem.ReadFile so the FUSE node implementations
// look nearly identical.
func (f *FileSystem) ReadFile(ctx context.Context, name string, off int64, size int) ([]byte, syscall.Errno) {
	e, ok := f.entries[name]
	if !ok {
		return nil, syscall.ENOENT
	}
	if size <= 0 {
		return []byte{}, 0
	}
	f.log.Log(ctx, logging.LevelTrace, "pathstub read", "name", name, "remote", e.RemotePath, "offset", off, "size", size)
	fh, err := f.opener.Open(e.RemotePath)
	if err != nil {
		f.log.Warn("pathstub open failed", "name", name, "remote", e.RemotePath, "error", err)
		return nil, toErrno(err)
	}
	defer func() {
		if cerr := fh.Close(); cerr != nil {
			f.log.Warn("pathstub close failed", "name", name, "remote", e.RemotePath, "error", cerr)
		}
	}()

	buf := make([]byte, size)
	n, err := fh.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		f.log.Warn("pathstub read failed", "name", name, "remote", e.RemotePath, "error", err)
		return nil, toErrno(err)
	}
	return buf[:n], 0
}

func toErrno(err error) syscall.Errno {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	default:
		return syscall.EIO
	}
}
