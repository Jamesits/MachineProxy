//go:build linux && loong64

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchLoongArch64 = 0xC0000102
	sysExecve            = 221
	sysExecveat          = 281
)

// Register indices for loong64 (loongarch64): a0..a7 are r4..r11, a7 is the
// syscall number register, sp is r3.
const (
	loongA0 = 4
	loongA1 = 5
	loongA2 = 6
	loongA3 = 7
	loongA4 = 8
	loongA7 = 11
	loongSP = 3
)

// SyscallRegs holds the register state for a syscall on loong64.
// On execve(pathname, argv, envp):
//   - a7 = syscall number
//   - a0 = pathname pointer
//   - a1 = argv pointer
//   - a2 = envp pointer
//
// For execveat(dirfd, pathname, argv, envp, flags):
//   - a0 = dirfd
//   - a1 = pathname pointer
//   - a2 = argv pointer
//   - a3 = envp pointer
//   - a4 = flags
type SyscallRegs struct {
	regs unix.PtraceRegs
}

func SeccompArch() uint32 { return auditArchLoongArch64 }
func SysExecve() uint32   { return sysExecve }
func SysExecveat() uint32 { return sysExecveat }

func GetRegs(pid int) (*SyscallRegs, error) {
	var r SyscallRegs
	if err := unix.PtraceGetRegs(pid, &r.regs); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *SyscallRegs) Set(pid int) error {
	return unix.PtraceSetRegs(pid, &r.regs)
}

func (r *SyscallRegs) SyscallNum() uint64 {
	return r.regs.Regs[loongA7]
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.Regs[loongA7] = ^uint64(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[loongA1])
	}
	return uintptr(r.regs.Regs[loongA0])
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[loongA2])
	}
	return uintptr(r.regs.Regs[loongA1])
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[loongA3])
	}
	return uintptr(r.regs.Regs[loongA2])
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[loongA1] = uint64(addr)
	} else {
		r.regs.Regs[loongA0] = uint64(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[loongA2] = uint64(addr)
	} else {
		r.regs.Regs[loongA1] = uint64(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[loongA3] = uint64(addr)
	} else {
		r.regs.Regs[loongA2] = uint64(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Regs[loongSP])
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
