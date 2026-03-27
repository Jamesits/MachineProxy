package agentproto

import (
	"context"
	"io"
	"sync"
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

// OnExec sets the callback for FrameExec messages.
func (m *Mux) OnExec(fn func(*ExecMsg)) { m.onExec = fn }

// OnSignal sets the callback for FrameSignal messages.
func (m *Mux) OnSignal(fn func(int)) { m.onSignal = fn }

// OnExit sets the callback for FrameExit messages.
func (m *Mux) OnExit(fn func(code int, errStr string)) { m.onExit = fn }

// OnError sets the callback for FrameError messages.
func (m *Mux) OnError(fn func(errStr string)) { m.onError = fn }

// OnLog sets the callback for FrameLog messages (agent log forwarding).
func (m *Mux) OnLog(fn func(*LogEntry)) { m.onLog = fn }

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
		}
	}
}
