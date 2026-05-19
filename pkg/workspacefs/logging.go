package workspacefs

import (
	"log/slog"

	"github.com/jamesits/machineproxy/pkg/logging"
)

func traceLevel() slog.Level {
	return logging.LevelTrace
}
