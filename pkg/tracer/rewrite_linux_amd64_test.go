//go:build linux && amd64

package tracer

import "testing"

func TestBlockSyscallSetsInvalidSyscallNumberAmd64(t *testing.T) {
	regs := &SyscallRegs{}
	regs.regs.Orig_rax = sysExecve

	regs.BlockSyscall()

	if regs.regs.Orig_rax != ^uint64(0) {
		t.Fatalf("Orig_rax = %#x, want invalid syscall number", regs.regs.Orig_rax)
	}
}
