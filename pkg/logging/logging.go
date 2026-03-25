package logging

import (
	"log"
	"os"
)

type Logger struct {
	inner *log.Logger
}

func New(prefix string) *Logger {
	return &Logger{inner: log.New(os.Stderr, prefix, log.LstdFlags|log.Lmicroseconds)}
}

func (l *Logger) Infof(format string, args ...any) {
	l.inner.Printf("INFO: "+format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	l.inner.Printf("ERROR: "+format, args...)
}
