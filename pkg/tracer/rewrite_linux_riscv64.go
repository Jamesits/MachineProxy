//go:build linux && riscv64

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchRISCV64 = 0xC00000F3
	sysExecve        = 221
	sysExecveat      = 281
)

// SyscallRegs holds the register state for a syscall on riscv64.
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

func SeccompArch() uint32 { return auditArchRISCV64 }
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
	return r.regs.A7
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.A7 = ^uint64(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.A1)
	}
	return uintptr(r.regs.A0)
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.A2)
	}
	return uintptr(r.regs.A1)
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.A3)
	}
	return uintptr(r.regs.A2)
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.A1 = uint64(addr)
	} else {
		r.regs.A0 = uint64(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.A2 = uint64(addr)
	} else {
		r.regs.A1 = uint64(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.A3 = uint64(addr)
	} else {
		r.regs.A2 = uint64(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Sp)
}

func (r *SyscallRegs) Ret() (uintptr, bool) {
	return uintptr(r.regs.A0), int64(r.regs.A0) >= 0
}

func (r *SyscallRegs) SetRetSuccess(v uintptr) {
	r.regs.A0 = uint64(v)
}

func (r *SyscallRegs) SetRetError(errno uintptr) {
	r.regs.A0 = uint64(-int64(errno))
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
