package agentproto

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestRecordingRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorderWriter(&buf)

	// Write session header.
	if err := rec.WriteSessionHeader(&SessionHeader{
		Version:   "0.1.0",
		StartTime: 1000,
		LocalUser: "testuser",
		LocalPID:  42,
		SSHAddr:   "remote:22",
		SSHUser:   "root",
		AgentPath: "/tmp/mproxy-agent",
	}); err != nil {
		t.Fatalf("WriteSessionHeader: %v", err)
	}

	// Command 1.
	exec1 := &ExecMsg{Path: "/bin/echo", Argv: []string{"echo", "hello"}, Cwd: "/tmp"}
	seq1, err := rec.WriteCommandStart(exec1)
	if err != nil {
		t.Fatalf("WriteCommandStart: %v", err)
	}
	if seq1 != 0 {
		t.Fatalf("seq1 = %d, want 0", seq1)
	}

	sendFrame := Frame{Type: FrameExec, Exec: exec1}
	if err := rec.WriteFrame(seq1, DirSend, &sendFrame); err != nil {
		t.Fatalf("WriteFrame send: %v", err)
	}

	recvFrame := Frame{Type: FrameData, Stream: 1, Data: []byte("hello\n")}
	if err := rec.WriteFrame(seq1, DirRecv, &recvFrame); err != nil {
		t.Fatalf("WriteFrame recv: %v", err)
	}

	if err := rec.WriteCommandEnd(seq1, 0); err != nil {
		t.Fatalf("WriteCommandEnd: %v", err)
	}

	// Command 2.
	exec2 := &ExecMsg{Path: "/bin/false", Argv: []string{"false"}}
	seq2, err := rec.WriteCommandStart(exec2)
	if err != nil {
		t.Fatalf("WriteCommandStart 2: %v", err)
	}
	if seq2 != 1 {
		t.Fatalf("seq2 = %d, want 1", seq2)
	}

	if err := rec.WriteCommandEnd(seq2, 1); err != nil {
		t.Fatalf("WriteCommandEnd 2: %v", err)
	}

	// Session footer.
	if err := rec.WriteSessionFooter(); err != nil {
		t.Fatalf("WriteSessionFooter: %v", err)
	}

	// Read it back.
	records, err := ReadRecording(&buf)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}

	// Expect: header, cmd1_start, frame_send, frame_recv, cmd1_end, cmd2_start, cmd2_end, footer
	if len(records) != 8 {
		t.Fatalf("got %d records, want 8", len(records))
	}

	// Verify header.
	if records[0].Type != RecordSessionHeader {
		t.Errorf("record[0] type = %d, want SessionHeader", records[0].Type)
	}
	if records[0].Header == nil || records[0].Header.Version != "0.1.0" {
		t.Errorf("record[0] header version mismatch")
	}
	if records[0].Header.LocalUser != "testuser" {
		t.Errorf("record[0] header local_user = %q", records[0].Header.LocalUser)
	}

	// Verify command 1 start.
	if records[1].Type != RecordCommandStart {
		t.Errorf("record[1] type = %d, want CommandStart", records[1].Type)
	}
	if records[1].CmdStart.SeqNum != 0 {
		t.Errorf("record[1] seq = %d, want 0", records[1].CmdStart.SeqNum)
	}
	if records[1].CmdStart.Exec.Path != "/bin/echo" {
		t.Errorf("record[1] exec.path = %q", records[1].CmdStart.Exec.Path)
	}

	// Verify frames.
	if records[2].Type != RecordFrame {
		t.Errorf("record[2] type = %d, want Frame", records[2].Type)
	}
	if records[2].Frame.Direction != DirSend {
		t.Errorf("record[2] dir = %d, want DirSend", records[2].Frame.Direction)
	}
	if records[3].Type != RecordFrame {
		t.Errorf("record[3] type = %d, want Frame", records[3].Type)
	}
	if records[3].Frame.Direction != DirRecv {
		t.Errorf("record[3] dir = %d, want DirRecv", records[3].Frame.Direction)
	}
	if !bytes.Equal(records[3].Frame.Frame.Data, []byte("hello\n")) {
		t.Errorf("record[3] data = %q", records[3].Frame.Frame.Data)
	}

	// Verify command 1 end.
	if records[4].Type != RecordCommandEnd {
		t.Errorf("record[4] type = %d, want CommandEnd", records[4].Type)
	}
	if records[4].CmdEnd.ExitCode != 0 {
		t.Errorf("record[4] exit_code = %d", records[4].CmdEnd.ExitCode)
	}

	// Verify command 2.
	if records[5].Type != RecordCommandStart {
		t.Errorf("record[5] type = %d, want CommandStart", records[5].Type)
	}
	if records[5].CmdStart.SeqNum != 1 {
		t.Errorf("record[5] seq = %d, want 1", records[5].CmdStart.SeqNum)
	}
	if records[6].Type != RecordCommandEnd {
		t.Errorf("record[6] type = %d, want CommandEnd", records[6].Type)
	}
	if records[6].CmdEnd.ExitCode != 1 {
		t.Errorf("record[6] exit_code = %d", records[6].CmdEnd.ExitCode)
	}

	// Verify footer.
	if records[7].Type != RecordSessionFooter {
		t.Errorf("record[7] type = %d, want SessionFooter", records[7].Type)
	}
	if records[7].Footer.CommandCount != 2 {
		t.Errorf("record[7] command_count = %d, want 2", records[7].Footer.CommandCount)
	}
}

func TestRecorderClose(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorderWriter(&buf)

	_ = rec.WriteSessionHeader(&SessionHeader{Version: "test"})
	_ = rec.Close()

	records, err := ReadRecording(&buf)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}

	// Header + footer from Close().
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if records[1].Type != RecordSessionFooter {
		t.Errorf("last record type = %d, want SessionFooter", records[1].Type)
	}
}

func TestMuxRecording(t *testing.T) {
	// Set up a pair of muxes with recording on the local side.
	ar, bw := io.Pipe()
	br, aw := io.Pipe()

	var recBuf bytes.Buffer
	rec := NewRecorderWriter(&recBuf)

	muxA := NewMux(ar, aw)
	muxA.SetRecorder(rec, 0)

	muxB := NewMux(br, bw)

	// B echoes back on stream 1 when stream 0 gets EOF.
	muxB.RegisterStream(0, &testHandler{
		eofFn: func() {
			_ = muxB.SendData(1, []byte("reply"))
			_ = muxB.Send(&Frame{Type: FrameExit, Code: 0})
		},
	})

	var gotReply bytes.Buffer
	muxA.RegisterStream(1, &testHandler{
		writeFn: func(data []byte) error {
			gotReply.Write(data)
			return nil
		},
	})

	go func() {
		_ = muxA.SendData(0, []byte("input"))
		_ = muxA.SendEOF(0)
	}()

	go muxB.ReadLoop(context.Background())

	code, err := muxA.ReadLoop(context.Background())
	if err != nil {
		t.Fatalf("ReadLoop: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	// Check recorded frames.
	records, err := ReadRecording(&recBuf)
	if err != nil {
		t.Fatalf("ReadRecording: %v", err)
	}

	// Should have: SendData(0), SendEOF(0), RecvData(1), RecvExit
	if len(records) != 4 {
		t.Fatalf("got %d recorded frames, want 4", len(records))
	}

	// All should be RecordFrame type.
	for i, r := range records {
		if r.Type != RecordFrame {
			t.Errorf("record[%d] type = %d, want RecordFrame", i, r.Type)
		}
	}

	// First two are sends, last two are receives.
	if records[0].Frame.Direction != DirSend {
		t.Errorf("record[0] dir = %d, want DirSend", records[0].Frame.Direction)
	}
	if records[1].Frame.Direction != DirSend {
		t.Errorf("record[1] dir = %d, want DirSend", records[1].Frame.Direction)
	}
	if records[2].Frame.Direction != DirRecv {
		t.Errorf("record[2] dir = %d, want DirRecv", records[2].Frame.Direction)
	}
	if records[3].Frame.Direction != DirRecv {
		t.Errorf("record[3] dir = %d, want DirRecv", records[3].Frame.Direction)
	}

	// Verify data content.
	if !bytes.Equal(records[0].Frame.Frame.Data, []byte("input")) {
		t.Errorf("record[0] data = %q, want %q", records[0].Frame.Frame.Data, "input")
	}
	if records[3].Frame.Frame.Type != FrameExit {
		t.Errorf("record[3] frame type = %d, want FrameExit", records[3].Frame.Frame.Type)
	}
}
