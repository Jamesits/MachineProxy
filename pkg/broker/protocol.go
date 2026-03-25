package broker

import "github.com/jamesits/machineproxy/pkg/remoteexec"

const (
	StreamStdin  = "stdin"
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamExit   = "exit"
)

type ExecRequest = remoteexec.Request

type Frame struct {
	Stream string `json:"stream"`
	Data   []byte `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
	Error  string `json:"error,omitempty"`
}
