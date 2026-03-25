package agentproto

import (
	"bytes"
	"io"
	"testing"
)

func TestCodecRoundtrip(t *testing.T) {
	cases := []struct {
		name  string
		frame Frame
	}{
		{
			name: "exec",
			frame: Frame{
				Type: FrameExec,
				Exec: &ExecMsg{
					Path:     "/bin/echo",
					Argv:     []string{"echo", "hello"},
					Env:      []string{"HOME=/root"},
					Cwd:      "/tmp",
					ExtraFDs: []uint32{3, 4},
				},
			},
		},
		{
			name:  "data",
			frame: Frame{Type: FrameData, Stream: 1, Data: []byte("hello world")},
		},
		{
			name:  "eof",
			frame: Frame{Type: FrameEOF, Stream: 0},
		},
		{
			name:  "signal",
			frame: Frame{Type: FrameSignal, Signal: 15},
		},
		{
			name:  "exit_zero",
			frame: Frame{Type: FrameExit, Code: 0},
		},
		{
			name:  "exit_nonzero",
			frame: Frame{Type: FrameExit, Code: 42},
		},
		{
			name:  "error",
			frame: Frame{Type: FrameError, Error: "something broke"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := NewEncoder(&buf)
			if err := enc.Encode(&tc.frame); err != nil {
				t.Fatalf("encode: %v", err)
			}

			dec := NewDecoder(&buf)
			got, err := dec.Decode()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if got.Type != tc.frame.Type {
				t.Errorf("Type: got %d, want %d", got.Type, tc.frame.Type)
			}
			if got.Stream != tc.frame.Stream {
				t.Errorf("Stream: got %d, want %d", got.Stream, tc.frame.Stream)
			}
			if !bytes.Equal(got.Data, tc.frame.Data) {
				t.Errorf("Data: got %q, want %q", got.Data, tc.frame.Data)
			}
			if got.Signal != tc.frame.Signal {
				t.Errorf("Signal: got %d, want %d", got.Signal, tc.frame.Signal)
			}
			if got.Code != tc.frame.Code {
				t.Errorf("Code: got %d, want %d", got.Code, tc.frame.Code)
			}
			if got.Error != tc.frame.Error {
				t.Errorf("Error: got %q, want %q", got.Error, tc.frame.Error)
			}
			if tc.frame.Exec != nil {
				if got.Exec == nil {
					t.Fatal("Exec is nil")
				}
				if got.Exec.Path != tc.frame.Exec.Path {
					t.Errorf("Exec.Path: got %q, want %q", got.Exec.Path, tc.frame.Exec.Path)
				}
			}
		})
	}
}

func TestDecoderEOF(t *testing.T) {
	dec := NewDecoder(bytes.NewReader(nil))
	_, err := dec.Decode()
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
}

func TestMultipleFrames(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)

	frames := []Frame{
		{Type: FrameData, Stream: 1, Data: []byte("chunk1")},
		{Type: FrameData, Stream: 1, Data: []byte("chunk2")},
		{Type: FrameExit, Code: 0},
	}
	for i := range frames {
		if err := enc.Encode(&frames[i]); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
	}

	dec := NewDecoder(&buf)
	for i, want := range frames {
		got, err := dec.Decode()
		if err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
		if got.Type != want.Type {
			t.Errorf("frame %d: Type got %d, want %d", i, got.Type, want.Type)
		}
	}
}
