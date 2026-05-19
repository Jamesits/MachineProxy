//go:build linux && amd64

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchX86_64 = 0xC000003E
	sysExecve       = 59
	sysExecveat     = 322
)

// SyscallRegs holds the register state for a syscall on amd64.
// On execve(pathname, argv, envp):
//   - Orig_rax = syscall number
//   - Rdi = pathname pointer
//   - Rsi = argv pointer
//   - Rdx = envp pointer
//     For execveat(dirfd, pathname, argv, envp, flags):
//   - Rdi = dirfd
//   - Rsi = pathname pointer
//   - Rdx = argv pointer
//   - R10 = envp pointer
//   - R8  = flags
type SyscallRegs struct {
	regs unix.PtraceRegs
}

func SeccompArch() uint32 { return auditArchX86_64 }
func SysExecve() uint32   { return sysExecve }
func SysExecveat() uint32 { return sysExecveat }

// GetRegs reads the tracee's register state.
func GetRegs(pid int) (*SyscallRegs, error) {
	var r SyscallRegs
	if err := unix.PtraceGetRegs(pid, &r.regs); err != nil {
		return nil, err
	}
	return &r, nil
}

// SetRegs writes the register state back to the tracee.
func (r *SyscallRegs) Set(pid int) error {
	return unix.PtraceSetRegs(pid, &r.regs)
}

// SyscallNum returns the invoked syscall number.
func (r *SyscallRegs) SyscallNum() uint64 {
	return r.regs.Orig_rax
}

// BlockSyscall replaces the pending syscall with an invalid syscall number so
// a failed rewrite cannot fall through and execute locally.
func (r *SyscallRegs) BlockSyscall() {
	r.regs.Orig_rax = ^uint64(0)
}

// PathAddr returns the pointer to the pathname argument.
// For execve this is arg0 (rdi), for execveat it's arg1 (rsi).
func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Rsi)
	}
	return uintptr(r.regs.Rdi)
}

// ArgvAddr returns the pointer to the argv array.
func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Rdx)
	}
	return uintptr(r.regs.Rsi)
}

// EnvpAddr returns the pointer to the envp array.
func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.R10)
	}
	return uintptr(r.regs.Rdx)
}

// SetPathAddr sets the pathname pointer.
func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Rsi = uint64(addr)
	} else {
		r.regs.Rdi = uint64(addr)
	}
}

// SetArgvAddr sets the argv pointer.
func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Rdx = uint64(addr)
	} else {
		r.regs.Rsi = uint64(addr)
	}
}

// SetEnvpAddr sets the envp pointer.
func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.R10 = uint64(addr)
	} else {
		r.regs.Rdx = uint64(addr)
	}
}

// StackPointer returns the current stack pointer.
func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Rsp)
}

// ptrSize is used for pointer arithmetic.
const ptrSize = int(unsafe.Sizeof(uintptr(0)))
