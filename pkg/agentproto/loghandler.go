package agentproto

import (
	"context"
	"fmt"
	"log/slog"
)

// MuxLogHandler is a slog.Handler that sends log records over the CBOR
// mux as FrameLog frames. This lets agent-side logs appear on the
// machineproxy process where the operator can see them.
type MuxLogHandler struct {
	mux   *Mux
	level slog.Level
	attrs []string // precomputed "key=value" pairs from WithAttrs
	group string   // current group prefix
}

// NewMuxLogHandler creates a handler that forwards log records through mux.
func NewMuxLogHandler(mux *Mux, level slog.Level) *MuxLogHandler {
	return &MuxLogHandler{mux: mux, level: level}
}

func (h *MuxLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *MuxLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]string, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		attrs = append(attrs, fmt.Sprintf("%s=%s", key, a.Value.String()))
		return true
	})

	return h.mux.Send(&Frame{
		Type: FrameLog,
		Log: &LogEntry{
			Level: int(r.Level),
			Msg:   r.Message,
			Attrs: attrs,
		},
	})
}

func (h *MuxLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]string, len(h.attrs), len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	for _, a := range attrs {
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		newAttrs = append(newAttrs, fmt.Sprintf("%s=%s", key, a.Value.String()))
	}
	return &MuxLogHandler{mux: h.mux, level: h.level, attrs: newAttrs, group: h.group}
}

func (h *MuxLogHandler) WithGroup(name string) slog.Handler {
	newGroup := name
	if h.group != "" {
		newGroup = h.group + "." + name
	}
	newAttrs := make([]string, len(h.attrs))
	copy(newAttrs, h.attrs)
	return &MuxLogHandler{mux: h.mux, level: h.level, attrs: newAttrs, group: newGroup}
}
