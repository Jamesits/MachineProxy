package broker

import (
	"io"
	"sync"

	"github.com/fxamacker/cbor/v2"
	"github.com/jamesits/machineproxy/pkg/remoteexec"
)

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

// Frame is a single CBOR-encoded message exchanged between the shim and the
// broker over the Unix socket.
type Frame struct {
	Stream string `cbor:"stream"`
	Data   []byte `cbor:"data,omitempty"`
	Code   int    `cbor:"code,omitempty"`
	Error  string `cbor:"error,omitempty"`
	Signal int    `cbor:"signal,omitempty"`
}

var (
	encMode cbor.EncMode
	decMode cbor.DecMode
)

func init() {
	var err error
	encMode, err = cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic("broker: " + err.Error())
	}
	decMode, err = cbor.DecOptions{}.DecMode()
	if err != nil {
		panic("broker: " + err.Error())
	}
}

// Encoder writes CBOR-encoded values to an io.Writer. Safe for concurrent use.
type Encoder struct {
	enc *cbor.Encoder
	mu  sync.Mutex
}

// NewEncoder returns an Encoder that writes CBOR frames/requests to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{enc: encMode.NewEncoder(w)}
}

// Encode writes a single CBOR-encoded value.
func (e *Encoder) Encode(v any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(v)
}

// Decoder reads CBOR-encoded values from an io.Reader.
type Decoder struct {
	dec *cbor.Decoder
}

// NewDecoder returns a Decoder that reads CBOR frames/requests from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: decMode.NewDecoder(r)}
}

// Decode reads the next CBOR-encoded value into v.
func (d *Decoder) Decode(v any) error {
	return d.dec.Decode(v)
}
