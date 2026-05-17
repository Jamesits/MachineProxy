package main

import (
	"os"
	"testing"
)

func TestFDFileSetReusesWrapperForSameFD(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()

	files := newFDFileSet()
	defer files.Close()

	first := files.Get(uint32(writeEnd.Fd()))
	second := files.Get(uint32(writeEnd.Fd()))

	if first == nil || second == nil {
		t.Fatalf("expected file wrappers")
	}
	if first != second {
		t.Fatalf("expected same wrapper for repeated fd")
	}

	files.Close()
	if _, err := writeEnd.Write([]byte("x")); err != nil {
		t.Fatalf("fd file set closed descriptor it does not own: %v", err)
	}
}
