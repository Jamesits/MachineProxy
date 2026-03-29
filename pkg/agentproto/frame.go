package agentproto

// FrameType identifies the kind of message on the CBOR mux.
type FrameType uint8

const (
	FrameExec   FrameType = 1 // local → agent: start command
	FrameData   FrameType = 2 // bidirectional: data for a stream
	FrameEOF    FrameType = 3 // sender closes a stream
	FrameSignal FrameType = 4 // local → agent: deliver signal to child pgid
	FrameExit   FrameType = 5 // agent → local: child exited (terminal frame)
	FrameError  FrameType = 6 // agent → local: internal error
	FrameLog    FrameType = 7 // agent → local: log message
	FrameConfig FrameType = 8 // local → agent: serialized config
)

// Frame is a single message on the CBOR mux. Fields are omitted when
// zero-valued so only the relevant subset appears on the wire.
type Frame struct {
	Type   FrameType    `cbor:"t"`
	Stream uint32       `cbor:"s,omitempty"` // 0=stdin, 1=stdout, 2=stderr, 3+=extra fds
	Data   []byte       `cbor:"d,omitempty"`
	Signal int          `cbor:"sig,omitempty"`
	Code   int          `cbor:"c,omitempty"`
	Error  string       `cbor:"e,omitempty"`
	Exec   *ExecMsg     `cbor:"x,omitempty"`
	Log    *LogEntry    `cbor:"l,omitempty"`
	Config *AgentConfig `cbor:"cfg,omitempty"`
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
