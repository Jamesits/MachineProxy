//go:build linux && arm

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchARM = 0x40000028
	sysExecve    = 11
	sysExecveat  = 387
)

// Register indices in the arm Uregs[18] layout.
const (
	armR0    = 0
	armR1    = 1
	armR2    = 2
	armR3    = 3
	armR4    = 4
	armR7    = 7  // EABI syscall number register
	armSp    = 13 // r13
	armOrigR = 17 // orig_r0
)

// SyscallRegs holds the register state for a syscall on arm (32-bit EABI).
// On execve(pathname, argv, envp):
//   - r7 = syscall number
//   - r0 = pathname pointer
//   - r1 = argv pointer
//   - r2 = envp pointer
//
// For execveat(dirfd, pathname, argv, envp, flags):
//   - r0 = dirfd
//   - r1 = pathname pointer
//   - r2 = argv pointer
//   - r3 = envp pointer
//   - r4 = flags
type SyscallRegs struct {
	regs unix.PtraceRegs
}

func SeccompArch() uint32 { return auditArchARM }
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
	return uint64(r.regs.Uregs[armR7])
}

// BlockSyscall sets r7 to an invalid syscall number. On ARM, modifying r7
// via PTRACE_SETREGSET(NT_PRSTATUS) reliably alters dispatch only when the
// kernel re-reads r7; on newer kernels prefer PTRACE_SET_SYSCALL or
// NT_ARM_SYSTEM_CALL. We use the r7 write as a best-effort poison.
func (r *SyscallRegs) BlockSyscall() {
	r.regs.Uregs[armR7] = ^uint32(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Uregs[armR1])
	}
	return uintptr(r.regs.Uregs[armR0])
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Uregs[armR2])
	}
	return uintptr(r.regs.Uregs[armR1])
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Uregs[armR3])
	}
	return uintptr(r.regs.Uregs[armR2])
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Uregs[armR1] = uint32(addr)
	} else {
		r.regs.Uregs[armR0] = uint32(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Uregs[armR2] = uint32(addr)
	} else {
		r.regs.Uregs[armR1] = uint32(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Uregs[armR3] = uint32(addr)
	} else {
		r.regs.Uregs[armR2] = uint32(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Uregs[armSp])
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
