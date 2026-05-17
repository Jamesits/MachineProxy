package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	auditArchS390X = 0x80000016
	sysExecve      = 11
	sysExecveat    = 354
)

// SyscallRegs holds the register state for a syscall on s390x.
// At syscall-entry stop:
//   - Gprs[1]  holds the syscall number (kernel loads it from svc operand
//     or from r1 for indirect svc)
//   - Gprs[2..7] hold args 1..6 (r2 also receives the return value)
//   - Orig_gpr2 preserves the original r2 (arg0) across the syscall
//   - Psw.Addr is the instruction pointer; Gprs[15] is the stack pointer
//
// For execve(pathname, argv, envp):
//   - r2 = pathname pointer
//   - r3 = argv pointer
//   - r4 = envp pointer
//
// For execveat(dirfd, pathname, argv, envp, flags):
//   - r2 = dirfd
//   - r3 = pathname pointer
//   - r4 = argv pointer
//   - r5 = envp pointer
//   - r6 = flags
//
// Modifying Gprs[1] via NT_PRSTATUS does not reliably change the dispatched
// syscall on every kernel version. Where exact cancellation is required,
// the canonical mechanism is PTRACE_SETREGSET(NT_S390_SYSTEM_CALL); the
// fallback poison here is best-effort.
type SyscallRegs struct {
	regs unix.PtraceRegs
}

func SeccompArch() uint32 { return auditArchS390X }
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
	return r.regs.Gprs[1]
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.Gprs[1] = ^uint64(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gprs[3])
	}
	return uintptr(r.regs.Gprs[2])
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gprs[4])
	}
	return uintptr(r.regs.Gprs[3])
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gprs[5])
	}
	return uintptr(r.regs.Gprs[4])
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gprs[3] = uint64(addr)
	} else {
		r.regs.Gprs[2] = uint64(addr)
		r.regs.Orig_gpr2 = uint64(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gprs[4] = uint64(addr)
	} else {
		r.regs.Gprs[3] = uint64(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gprs[5] = uint64(addr)
	} else {
		r.regs.Gprs[4] = uint64(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Gprs[15])
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
