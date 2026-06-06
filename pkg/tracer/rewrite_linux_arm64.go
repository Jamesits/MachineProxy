//go:build linux && arm64

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchARM64 = 0xC00000B7
	sysExecve      = 221
	sysExecveat    = 281
)

// SyscallRegs holds the register state for a syscall on arm64.
// On execve(pathname, argv, envp):
//   - Regs[8] = syscall number
//   - Regs[0] = pathname pointer
//   - Regs[1] = argv pointer
//   - Regs[2] = envp pointer
//     For execveat(dirfd, pathname, argv, envp, flags):
//   - Regs[0] = dirfd
//   - Regs[1] = pathname pointer
//   - Regs[2] = argv pointer
//   - Regs[3] = envp pointer
//   - Regs[4] = flags
type SyscallRegs struct {
	regs unix.PtraceRegsArm64
}

func SeccompArch() uint32 { return auditArchARM64 }
func SysExecve() uint32   { return sysExecve }
func SysExecveat() uint32 { return sysExecveat }

// GetRegs reads the tracee's register state via PTRACE_GETREGSET.
func GetRegs(pid int) (*SyscallRegs, error) {
	var r SyscallRegs
	if err := unix.PtraceGetRegSetArm64(pid, 1, &r.regs); err != nil {
		return nil, err
	}
	return &r, nil
}

// Set writes the register state back to the tracee.
func (r *SyscallRegs) Set(pid int) error {
	return unix.PtraceSetRegSetArm64(pid, 1, &r.regs)
}

// SyscallNum returns the invoked syscall number.
func (r *SyscallRegs) SyscallNum() uint64 {
	return r.regs.Regs[8]
}

// BlockSyscall replaces the pending syscall with an invalid syscall number so
// a failed rewrite cannot fall through and execute locally.
func (r *SyscallRegs) BlockSyscall() {
	r.regs.Regs[8] = ^uint64(0)
}

// PathAddr returns the pointer to the pathname argument.
func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[1])
	}
	return uintptr(r.regs.Regs[0])
}

// ArgvAddr returns the pointer to the argv array.
func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[2])
	}
	return uintptr(r.regs.Regs[1])
}

// EnvpAddr returns the pointer to the envp array.
func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[3])
	}
	return uintptr(r.regs.Regs[2])
}

// SetPathAddr sets the pathname pointer.
func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[1] = uint64(addr)
	} else {
		r.regs.Regs[0] = uint64(addr)
	}
}

// SetArgvAddr sets the argv pointer.
func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[2] = uint64(addr)
	} else {
		r.regs.Regs[1] = uint64(addr)
	}
}

// SetEnvpAddr sets the envp pointer.
func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[3] = uint64(addr)
	} else {
		r.regs.Regs[2] = uint64(addr)
	}
}

// StackPointer returns the current stack pointer.
func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Sp)
}

// Ret returns the syscall result register (x0) and whether the call
// succeeded (arm64 uses the negative-errno convention).
func (r *SyscallRegs) Ret() (uintptr, bool) {
	return uintptr(r.regs.Regs[0]), int64(r.regs.Regs[0]) >= 0
}

// SetRetSuccess overwrites the result register with a successful return value.
func (r *SyscallRegs) SetRetSuccess(v uintptr) {
	r.regs.Regs[0] = uint64(v)
}

// SetRetError overwrites the result register with -errno (failure).
func (r *SyscallRegs) SetRetError(errno uintptr) {
	r.regs.Regs[0] = uint64(-int64(errno))
}

// ptrSize is used for pointer arithmetic.
const ptrSize = int(unsafe.Sizeof(uintptr(0)))
