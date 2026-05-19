//go:build backend_docker

package docker

import (
	"errors"
	"syscall"
	"testing"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func TestErrnoToErrPreservesSpecificErrno(t *testing.T) {
	err := errnoToErr(&agentproto.FileOpResp{Errno: uint32(syscall.ENOTEMPTY), ErrMsg: "directory not empty"})

	if !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("errnoToErr(ENOTEMPTY) = %v, want errors.Is ENOTEMPTY", err)
	}
}
