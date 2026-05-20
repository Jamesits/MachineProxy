package logging

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// JSONValue serializes v to a compact JSON string for use as a slog value.
// Errors are silently discarded; v must be JSON-marshalable.
func JSONValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// LevelTrace is a custom level below Debug for high-volume diagnostics
// such as every command execution and connection keepalive.
const LevelTrace = slog.Level(-8)

// ParseLevel converts a human-readable level string to a slog.Level.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Setup creates and registers a default slog.Logger writing to w at the
// given level. It replaces the custom "TRACE" level name in output.
func Setup(level string, w io.Writer) *slog.Logger {
	lvl := ParseLevel(level)
	h := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: lvl,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if a.Value.Any().(slog.Level) == LevelTrace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	})
	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}
