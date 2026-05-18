package agentproto

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// RecordType tags each record in a recording file.
type RecordType uint8

const (
	RecordSessionHeader RecordType = 1
	RecordCommandStart  RecordType = 2
	RecordFrame         RecordType = 3
	RecordCommandEnd    RecordType = 4
	RecordSessionFooter RecordType = 5
)

// Direction indicates whether a frame was sent or received.
type Direction uint8

const (
	DirSend Direction = 1 // local → agent
	DirRecv Direction = 2 // agent → local
)

// Record is the top-level envelope written to a recording file.
// Exactly one of the optional fields is set per record.
type Record struct {
	Type     RecordType     `cbor:"type"`
	Header   *SessionHeader `cbor:"header,omitempty"`
	CmdStart *CommandStart  `cbor:"cmd_start,omitempty"`
	Frame    *RecordedFrame `cbor:"frame,omitempty"`
	CmdEnd   *CommandEnd    `cbor:"cmd_end,omitempty"`
	Footer   *SessionFooter `cbor:"footer,omitempty"`
}

// SessionHeader is written once at the start of a recording file.
//
// SSHAddr/SSHUser are kept for back-compat with pre-multi-backend
// recordings; new code should also populate BackendType/BackendAddr so
// non-SSH backends can be identified.
type SessionHeader struct {
	Version     string `cbor:"version"`
	StartTime   int64  `cbor:"start_time"` // unix nanos
	LocalUser   string `cbor:"local_user"`
	LocalPID    int    `cbor:"local_pid"`
	SSHAddr     string `cbor:"ssh_addr,omitempty"`
	SSHUser     string `cbor:"ssh_user,omitempty"`
	BackendType string `cbor:"backend_type,omitempty"`
	BackendAddr string `cbor:"backend_addr,omitempty"`
	AgentPath   string `cbor:"agent_path"`
}

// CommandStart marks the beginning of a command execution.
type CommandStart struct {
	SeqNum    uint32  `cbor:"seq"`
	StartTime int64   `cbor:"start_time"`
	Exec      ExecMsg `cbor:"exec"`
}

// RecordedFrame wraps an agentproto Frame with recording metadata.
type RecordedFrame struct {
	SeqNum    uint32    `cbor:"seq"`
	Timestamp int64     `cbor:"ts"`
	Direction Direction `cbor:"dir"`
	Frame     Frame     `cbor:"frame"`
}

// CommandEnd marks the end of a command execution.
type CommandEnd struct {
	SeqNum   uint32 `cbor:"seq"`
	EndTime  int64  `cbor:"end_time"`
	ExitCode int    `cbor:"exit_code"`
}

// SessionFooter is written once at the end of a recording file.
type SessionFooter struct {
	EndTime      int64  `cbor:"end_time"`
	CommandCount uint32 `cbor:"command_count"`
}

// Recorder writes session recording data. Safe for concurrent use.
type Recorder struct {
	enc    *cbor.Encoder
	mu     sync.Mutex
	seqNum atomic.Uint32
	file   io.Closer // nil when writing to a bare io.Writer
}

// NewRecorder creates a Recorder that writes to path. The file is
// created (or truncated) immediately.
func NewRecorder(path string) (*Recorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Recorder{
		enc:  encMode.NewEncoder(f),
		file: f,
	}, nil
}

// NewRecorderWriter creates a Recorder that writes to w. The caller
// is responsible for closing w.
func NewRecorderWriter(w io.Writer) *Recorder {
	return &Recorder{
		enc: encMode.NewEncoder(w),
	}
}

func (r *Recorder) write(rec *Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enc.Encode(rec)
}

// WriteSessionHeader writes the session-level metadata. Call once at
// the start of a recording.
func (r *Recorder) WriteSessionHeader(h *SessionHeader) error {
	return r.write(&Record{Type: RecordSessionHeader, Header: h})
}

// WriteCommandStart marks the beginning of a new command and returns
// its sequence number.
func (r *Recorder) WriteCommandStart(exec *ExecMsg) (uint32, error) {
	seq := r.seqNum.Add(1) - 1
	err := r.write(&Record{
		Type: RecordCommandStart,
		CmdStart: &CommandStart{
			SeqNum:    seq,
			StartTime: time.Now().UnixNano(),
			Exec:      *exec,
		},
	})
	return seq, err
}

// WriteFrame records a single agentproto frame with direction metadata.
func (r *Recorder) WriteFrame(seq uint32, dir Direction, f *Frame) error {
	return r.write(&Record{
		Type: RecordFrame,
		Frame: &RecordedFrame{
			SeqNum:    seq,
			Timestamp: time.Now().UnixNano(),
			Direction: dir,
			Frame:     *f,
		},
	})
}

// WriteCommandEnd marks the completion of a command.
func (r *Recorder) WriteCommandEnd(seq uint32, exitCode int) error {
	return r.write(&Record{
		Type: RecordCommandEnd,
		CmdEnd: &CommandEnd{
			SeqNum:   seq,
			EndTime:  time.Now().UnixNano(),
			ExitCode: exitCode,
		},
	})
}

// WriteSessionFooter writes the session-level summary. Call once
// before closing the recorder.
func (r *Recorder) WriteSessionFooter() error {
	return r.write(&Record{
		Type: RecordSessionFooter,
		Footer: &SessionFooter{
			EndTime:      time.Now().UnixNano(),
			CommandCount: r.seqNum.Load(),
		},
	})
}

// Close writes the session footer and closes the underlying file.
func (r *Recorder) Close() error {
	_ = r.WriteSessionFooter()
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

// ReadRecording decodes all records from a recording stream.
func ReadRecording(r io.Reader) ([]Record, error) {
	dec := decMode.NewDecoder(r)
	var records []Record
	for {
		var rec Record
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			return records, err
		}
		records = append(records, rec)
	}
	return records, nil
}
