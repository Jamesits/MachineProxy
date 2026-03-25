package agentproto

import (
	"io"
	"sync"

	"github.com/fxamacker/cbor/v2"
)

var encMode cbor.EncMode
var decMode cbor.DecMode

func init() {
	var err error
	encMode, err = cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic("agentproto: " + err.Error())
	}
	decMode, err = cbor.DecOptions{}.DecMode()
	if err != nil {
		panic("agentproto: " + err.Error())
	}
}

// Encoder writes CBOR-encoded frames. It is safe for concurrent use.
type Encoder struct {
	enc *cbor.Encoder
	mu  sync.Mutex
}

// NewEncoder returns an Encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{enc: encMode.NewEncoder(w)}
}

// Encode writes a single frame.
func (e *Encoder) Encode(f *Frame) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enc.Encode(f)
}

// Decoder reads CBOR-encoded frames.
type Decoder struct {
	dec *cbor.Decoder
}

// NewDecoder returns a Decoder that reads from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: decMode.NewDecoder(r)}
}

// Decode reads the next frame. Returns io.EOF at end of stream.
func (d *Decoder) Decode() (*Frame, error) {
	var f Frame
	if err := d.dec.Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}
