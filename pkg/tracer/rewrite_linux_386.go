//go:build linux && 386

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchI386 = 0x40000003
	sysExecve     = 11
	sysExecveat   = 358
)

// SyscallRegs holds the register state for a syscall on 386 (i386).
// On execve(pathname, argv, envp):
//   - Orig_eax = syscall number
//   - Ebx = pathname pointer
//   - Ecx = argv pointer
//   - Edx = envp pointer
//
// For execveat(dirfd, pathname, argv, envp, flags):
//   - Ebx = dirfd
//   - Ecx = pathname pointer
//   - Edx = argv pointer
//   - Esi = envp pointer
//   - Edi = flags
type SyscallRegs struct {
	regs unix.PtraceRegs
}

func SeccompArch() uint32 { return auditArchI386 }
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
	return uint64(uint32(r.regs.Orig_eax))
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.Orig_eax = -1
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(uint32(r.regs.Ecx))
	}
	return uintptr(uint32(r.regs.Ebx))
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(uint32(r.regs.Edx))
	}
	return uintptr(uint32(r.regs.Ecx))
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(uint32(r.regs.Esi))
	}
	return uintptr(uint32(r.regs.Edx))
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Ecx = int32(addr)
	} else {
		r.regs.Ebx = int32(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Edx = int32(addr)
	} else {
		r.regs.Ecx = int32(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Esi = int32(addr)
	} else {
		r.regs.Edx = int32(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(uint32(r.regs.Esp))
}

func (r *SyscallRegs) Ret() (uintptr, bool) {
	return uintptr(uint32(r.regs.Eax)), r.regs.Eax >= 0
}

func (r *SyscallRegs) SetRetSuccess(v uintptr) {
	r.regs.Eax = int32(v)
}

func (r *SyscallRegs) SetRetError(errno uintptr) {
	r.regs.Eax = -int32(errno)
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
