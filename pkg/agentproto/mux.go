package agentproto

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// StreamHandler receives data and EOF events for a single stream ID.
type StreamHandler interface {
	Write(data []byte) error
	EOF()
}

// writerHandler adapts an io.WriteCloser into a StreamHandler.
type writerHandler struct {
	w io.WriteCloser
}

func (h *writerHandler) Write(data []byte) error { _, err := h.w.Write(data); return err }
func (h *writerHandler) EOF()                    { _ = h.w.Close() }

// WriterHandler returns a StreamHandler that writes to w and closes it on EOF.
func WriterHandler(w io.WriteCloser) StreamHandler {
	return &writerHandler{w: w}
}

// Mux multiplexes CBOR frames over a reader/writer pair. Incoming frames
// are dispatched to registered handlers; Send serialises outgoing frames.
type Mux struct {
	enc *Encoder
	dec *Decoder

	mu       sync.RWMutex
	streams  map[uint32]StreamHandler
	onExec   func(*ExecMsg)
	onSignal func(int)
	onExit   func(code int, errStr string)
	onError  func(errStr string)
	onLog    func(*LogEntry)
	onFileOp func(*FileOpReq) *FileOpResp

	// File-op RPC state. nextReqID is allocated atomically; pending
	// maps in-flight ReqIDs to the channel waiting for the response.
	nextReqID atomic.Uint32
	pendingMu sync.Mutex
	pending   map[uint32]chan *FileOpResp

	recorder  *Recorder
	recSeqNum uint32
}

// NewMux creates a mux over the given reader (incoming frames) and writer
// (outgoing frames). Typically r and w are the two ends of an SSH session's
// stdio.
func NewMux(r io.Reader, w io.Writer) *Mux {
	return &Mux{
		enc:     NewEncoder(w),
		dec:     NewDecoder(r),
		streams: make(map[uint32]StreamHandler),
	}
}

// RegisterStream adds a handler for the given stream ID.
func (m *Mux) RegisterStream(id uint32, h StreamHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streams[id] = h
}

// OnSignal sets the callback for FrameSignal messages.
func (m *Mux) OnSignal(fn func(int)) { m.onSignal = fn }

// OnLog sets the callback for FrameLog messages (agent log forwarding).
func (m *Mux) OnLog(fn func(*LogEntry)) { m.onLog = fn }

// OnFileOp registers the agent-side handler that services file-op
// requests. The handler receives the request and must return a fully
// populated FileOpResp (ReqID is propagated automatically).
func (m *Mux) OnFileOp(fn func(*FileOpReq) *FileOpResp) { m.onFileOp = fn }

// FileOp performs a client-side file-op RPC: encodes req with a fresh
// ReqID, sends it, and waits for the matching FileOpResp. Multiple
// FileOp calls are safe concurrently.
func (m *Mux) FileOp(ctx context.Context, req *FileOpReq) (*FileOpResp, error) {
	if req == nil {
		return nil, fmt.Errorf("agentproto: FileOp request is nil")
	}
	// 0 is reserved for "uninitialised"; start from 1.
	for {
		id := m.nextReqID.Add(1)
		if id != 0 {
			req.ReqID = id
			break
		}
	}
	ch := make(chan *FileOpResp, 1)
	m.pendingMu.Lock()
	if m.pending == nil {
		m.pending = make(map[uint32]chan *FileOpResp)
	}
	m.pending[req.ReqID] = ch
	m.pendingMu.Unlock()
	defer func() {
		m.pendingMu.Lock()
		delete(m.pending, req.ReqID)
		m.pendingMu.Unlock()
	}()
	if err := m.Send(&Frame{Type: FrameFileOp, FileOp: req}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, io.ErrUnexpectedEOF
		}
		return resp, nil
	}
}

// deliverFileResp routes an incoming FileOpResp to the waiting caller.
// Drops the frame silently when nobody is waiting (e.g. response
// arriving after FileOp was cancelled by its context).
func (m *Mux) deliverFileResp(resp *FileOpResp) {
	if resp == nil {
		return
	}
	m.pendingMu.Lock()
	ch, ok := m.pending[resp.ReqID]
	if ok {
		delete(m.pending, resp.ReqID)
	}
	m.pendingMu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// failPending closes out all in-flight FileOp calls. Used when the read
// loop terminates so blocked callers don't hang forever.
func (m *Mux) failPending() {
	m.pendingMu.Lock()
	pending := m.pending
	m.pending = nil
	m.pendingMu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
}

// SetRecorder enables recording of all frames passing through this mux.
// seq is the command sequence number assigned by the Recorder.
func (m *Mux) SetRecorder(rec *Recorder, seq uint32) {
	m.recorder = rec
	m.recSeqNum = seq
}

// Send writes a frame to the underlying writer. Safe for concurrent use.
func (m *Mux) Send(f *Frame) error {
	if m.recorder != nil {
		_ = m.recorder.WriteFrame(m.recSeqNum, DirSend, f)
	}
	return m.enc.Encode(f)
}

// SendData is a convenience for sending a FrameData.
func (m *Mux) SendData(stream uint32, data []byte) error {
	return m.Send(&Frame{Type: FrameData, Stream: stream, Data: data})
}

// SendEOF is a convenience for sending a FrameEOF.
func (m *Mux) SendEOF(stream uint32) error {
	return m.Send(&Frame{Type: FrameEOF, Stream: stream})
}

// DecodeOne reads a single frame from the underlying reader without
// dispatching it. Useful for reading the initial FrameExec before
// the read loop starts.
func (m *Mux) DecodeOne() (*Frame, error) {
	return m.dec.Decode()
}

// ReadLoop reads and dispatches frames until the context is cancelled,
// a FrameExit is received, or the underlying reader returns an error.
// It returns the exit code from a FrameExit, or -1 if the loop ended
// without one.
func (m *Mux) ReadLoop(ctx context.Context) (exitCode int, err error) {
	exitCode = -1
	defer m.failPending()
	for {
		if ctx.Err() != nil {
			return exitCode, ctx.Err()
		}

		f, decErr := m.dec.Decode()
		if decErr != nil {
			return exitCode, decErr
		}

		if m.recorder != nil {
			_ = m.recorder.WriteFrame(m.recSeqNum, DirRecv, f)
		}

		switch f.Type {
		case FrameExec:
			if m.onExec != nil && f.Exec != nil {
				m.onExec(f.Exec)
			}

		case FrameData:
			m.mu.RLock()
			h := m.streams[f.Stream]
			m.mu.RUnlock()
			if h != nil && len(f.Data) > 0 {
				_ = h.Write(f.Data)
			}

		case FrameEOF:
			m.mu.RLock()
			h := m.streams[f.Stream]
			m.mu.RUnlock()
			if h != nil {
				h.EOF()
			}

		case FrameSignal:
			if m.onSignal != nil {
				m.onSignal(f.Signal)
			}

		case FrameExit:
			exitCode = f.Code
			if m.onExit != nil {
				m.onExit(f.Code, f.Error)
			}
			return exitCode, nil

		case FrameError:
			if m.onError != nil {
				m.onError(f.Error)
			}

		case FrameLog:
			if m.onLog != nil && f.Log != nil {
				m.onLog(f.Log)
			}

		case FrameFileOp:
			// Agent-side: service the request and send the response.
			if m.onFileOp != nil && f.FileOp != nil {
				resp := m.onFileOp(f.FileOp)
				if resp == nil {
					resp = &FileOpResp{}
				}
				resp.ReqID = f.FileOp.ReqID
				_ = m.Send(&Frame{Type: FrameFileResp, FileResp: resp})
			}

		case FrameFileResp:
			// Client-side: hand the response to the waiting caller.
			if f.FileResp != nil {
				m.deliverFileResp(f.FileResp)
			}
		}
	}
}
