package broker

import "github.com/jamesits/machineproxy/pkg/remoteexec"

const (
	StreamStdin  = "stdin"
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamExit   = "exit"
	StreamSignal = "signal"

	// StreamFDPrefix is used for extra file descriptor streams.
	// The full stream name is "fd:N" where N is the fd number.
	StreamFDPrefix = "fd:"
)

type ExecRequest = remoteexec.Request

type Frame struct {
	Stream string `json:"stream"`
	Data   []byte `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
	Signal int    `json:"signal,omitempty"`
}
