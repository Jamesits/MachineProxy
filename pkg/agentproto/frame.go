package agentproto

// FrameType identifies the kind of message on the CBOR mux.
type FrameType uint8

const (
	FrameExec      FrameType = 1  // local → agent: start command
	FrameData      FrameType = 2  // bidirectional: data for a stream
	FrameEOF       FrameType = 3  // sender closes a stream
	FrameSignal    FrameType = 4  // local → agent: deliver signal to child pgid
	FrameExit      FrameType = 5  // agent → local: child exited (terminal frame)
	FrameError     FrameType = 6  // agent → local: internal error
	FrameLog       FrameType = 7  // agent → local: log message
	FrameConfig    FrameType = 8  // local → agent: serialized config
	FramePathQuery FrameType = 9  // local → agent: enumerate PATH executables
	FramePathInfo  FrameType = 10 // agent → local: PATH enumeration result
	FrameFileOp    FrameType = 11 // local → agent: file-op request
	FrameFileResp  FrameType = 12 // agent → local: file-op response
)

// Frame is a single message on the CBOR mux. Fields are omitted when
// zero-valued so only the relevant subset appears on the wire.
type Frame struct {
	Type     FrameType    `cbor:"t"`
	Stream   uint32       `cbor:"s,omitempty"` // 0=stdin, 1=stdout, 2=stderr, 3+=extra fds
	Data     []byte       `cbor:"d,omitempty"`
	Signal   int          `cbor:"sig,omitempty"`
	Code     int          `cbor:"c,omitempty"`
	Error    string       `cbor:"e,omitempty"`
	Exec     *ExecMsg     `cbor:"x,omitempty"`
	Log      *LogEntry    `cbor:"l,omitempty"`
	Config   *AgentConfig `cbor:"cfg,omitempty"`
	Query    *PathQuery   `cbor:"q,omitempty"`
	Info     *PathInfo    `cbor:"i,omitempty"`
	FileOp   *FileOpReq   `cbor:"fop,omitempty"`
	FileResp *FileOpResp  `cbor:"frp,omitempty"`
}

// LogEntry carries a structured log record from the agent.
type LogEntry struct {
	Level int      `cbor:"lvl"` // slog.Level value
	Msg   string   `cbor:"msg"`
	Attrs []string `cbor:"a,omitempty"` // key=value pairs
}

// AgentConfig carries the agent's runtime configuration, sent via FrameConfig
// before any FrameExec. Patterns are globs or /regex/ delimited strings.
type AgentConfig struct {
	EnvKeep   []string `cbor:"env_keep"`
	EnvRemove []string `cbor:"env_remove"`
}

// ExecMsg carries the command details in a FrameExec.
type ExecMsg struct {
	Path     string   `cbor:"path"`
	Argv     []string `cbor:"argv"`
	Env      []string `cbor:"env"`
	Cwd      string   `cbor:"cwd"`
	ExtraFDs []uint32 `cbor:"fds,omitempty"`
}

// PathQuery requests enumeration of executable files reachable through
// the listed PATH-style directories. An empty Paths slice tells the
// agent to use its own $PATH at the time of the request.
type PathQuery struct {
	Paths []string `cbor:"paths,omitempty"`
}

// PathInfoEntry describes one stub entry returned by FramePathInfo.
type PathInfoEntry struct {
	Name       string `cbor:"name"`
	RemotePath string `cbor:"path"`
	Mode       uint32 `cbor:"mode"`
	Size       int64  `cbor:"size"`
	MTimeNanos int64  `cbor:"mtime,omitempty"`
}

// PathInfo carries the agent's enumeration result. Entries are deduped
// by Name (first-found wins per PATH precedence) and listed in the same
// order as the input Paths.
type PathInfo struct {
	Entries []PathInfoEntry `cbor:"entries"`
}

// FileOp identifies a remote file operation. Operations are RPC-style:
// the caller assigns a unique ReqID and expects exactly one FileOpResp
// back with the same ReqID. File handles are agent-side state; the
// client treats them as opaque uint32 tokens returned by Open/Create.
type FileOp uint8

const (
	FileOpOpen      FileOp = 1  // input: Path, Flags  → output: Handle
	FileOpCreate    FileOp = 2  // input: Path         → output: Handle (O_WRONLY|O_CREATE|O_TRUNC)
	FileOpOpenFile  FileOp = 3  // input: Path, Flags  → output: Handle
	FileOpClose     FileOp = 4  // input: Handle
	FileOpReadAt    FileOp = 5  // input: Handle, Offset, Size  → output: Data, EOF
	FileOpWriteAt   FileOp = 6  // input: Handle, Offset, Data  → output: Size (bytes written)
	FileOpStat      FileOp = 7  // input: Path → output: Stat
	FileOpLstat     FileOp = 8  // input: Path → output: Stat, does not follow symlinks
	FileOpReadDir   FileOp = 9  // input: Path → output: Entries
	FileOpMkdir     FileOp = 10 // input: Path
	FileOpMkdirAll  FileOp = 11 // input: Path
	FileOpRemove    FileOp = 12 // input: Path
	FileOpRename    FileOp = 13 // input: Path, NewPath
	FileOpChmod     FileOp = 14 // input: Path, Mode
	FileOpTruncate  FileOp = 15 // input: Path, Size
	FileOpGetwd     FileOp = 16 //             → output: Path
	FileOpFsync     FileOp = 17 // input: Handle
	FileOpReadlink  FileOp = 18 // input: Path → output: Path
	FileOpSymlink   FileOp = 19 // input: Path target, NewPath linkpath
	FileOpLink      FileOp = 20 // input: Path old, NewPath new
	FileOpChown     FileOp = 21 // input: Path, UID, GID
	FileOpChtimes   FileOp = 22 // input: Path, AtimeNanos, MTimeNanos
	FileOpStatfs    FileOp = 23 // input: Path → output: Statfs
	FileOpFstat     FileOp = 24 // input: Handle → output: Stat
	FileOpFtruncate FileOp = 25 // input: Handle, Size
)

// FileOpReq is the request half of a file-op RPC. Only fields relevant
// to the chosen Op should be set.
type FileOpReq struct {
	ReqID      uint32 `cbor:"id"`
	Op         FileOp `cbor:"op"`
	Path       string `cbor:"p,omitempty"`
	NewPath    string `cbor:"np,omitempty"`
	Flags      int32  `cbor:"fl,omitempty"`
	Mode       uint32 `cbor:"mo,omitempty"`
	UID        uint32 `cbor:"uid,omitempty"`
	GID        uint32 `cbor:"gid,omitempty"`
	Handle     uint32 `cbor:"h,omitempty"`
	Offset     int64  `cbor:"of,omitempty"`
	Size       int64  `cbor:"sz,omitempty"`
	AtimeNanos int64  `cbor:"at,omitempty"`
	MTimeNanos int64  `cbor:"mt,omitempty"`
	Data       []byte `cbor:"d,omitempty"`
}

// FileOpResp is the response half. Errno (when nonzero) is a POSIX
// errno value the client maps back to an os.Err* sentinel; ErrMsg is a
// human-readable description for logs.
type FileOpResp struct {
	ReqID   uint32         `cbor:"id"`
	Errno   uint32         `cbor:"er,omitempty"`
	ErrMsg  string         `cbor:"em,omitempty"`
	Handle  uint32         `cbor:"h,omitempty"`
	Data    []byte         `cbor:"d,omitempty"`
	N       int64          `cbor:"n,omitempty"`   // bytes read/written
	EOF     bool           `cbor:"eof,omitempty"` // for ReadAt
	Path    string         `cbor:"p,omitempty"`   // for Getwd
	Stat    *FileStat      `cbor:"st,omitempty"`
	Statfs  *FileStatfs    `cbor:"sf,omitempty"`
	Entries []FileDirEntry `cbor:"en,omitempty"`
}

// FileStat is a backend-agnostic snapshot of os.FileInfo fields.
type FileStat struct {
	Name       string `cbor:"n"`
	Size       int64  `cbor:"s"`
	Mode       uint32 `cbor:"m"`
	MTimeNanos int64  `cbor:"t,omitempty"`
	ATimeNanos int64  `cbor:"at,omitempty"`
	CTimeNanos int64  `cbor:"ct,omitempty"`
	UID        uint32 `cbor:"uid,omitempty"`
	GID        uint32 `cbor:"gid,omitempty"`
	Nlink      uint32 `cbor:"nl,omitempty"`
	Rdev       uint32 `cbor:"rd,omitempty"`
	Blocks     uint64 `cbor:"bl,omitempty"`
	Blksize    uint32 `cbor:"bs,omitempty"`
	Ino        uint64 `cbor:"ino,omitempty"`
	IsDir      bool   `cbor:"d,omitempty"`
}

// FileStatfs is a backend-agnostic statfs/statvfs snapshot.
type FileStatfs struct {
	Blocks  uint64 `cbor:"b,omitempty"`
	Bfree   uint64 `cbor:"bf,omitempty"`
	Bavail  uint64 `cbor:"ba,omitempty"`
	Files   uint64 `cbor:"f,omitempty"`
	Ffree   uint64 `cbor:"ff,omitempty"`
	Bsize   uint32 `cbor:"bs,omitempty"`
	Frsize  uint32 `cbor:"fr,omitempty"`
	NameLen uint32 `cbor:"nl,omitempty"`
}

// FileDirEntry is one entry in a FileOpReadDir response.
type FileDirEntry struct {
	Stat FileStat `cbor:"st"`
}
