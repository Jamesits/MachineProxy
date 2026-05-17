//go:build linux && (ppc64 || ppc64le)

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	sysExecve   = 11
	sysExecveat = 362
)

// SyscallRegs holds the register state for a syscall on ppc64 / ppc64le.
// On execve(pathname, argv, envp):
//   - r0 = syscall number
//   - r3 = pathname pointer
//   - r4 = argv pointer
//   - r5 = envp pointer
//
// For execveat(dirfd, pathname, argv, envp, flags):
//   - r3 = dirfd
//   - r4 = pathname pointer
//   - r5 = argv pointer
//   - r6 = envp pointer
//   - r7 = flags
//
// At a syscall-entry stop, r3 (Gpr[3]) still holds the original arg0; the
// kernel preserves it in Orig_gpr3 for restart purposes. We update both
// Gpr[3] and Orig_gpr3 when writing arg0 to keep them consistent.
type SyscallRegs struct {
	regs unix.PtraceRegs
}

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
	return r.regs.Gpr[0]
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.Gpr[0] = ^uint64(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gpr[4])
	}
	return uintptr(r.regs.Gpr[3])
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gpr[5])
	}
	return uintptr(r.regs.Gpr[4])
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Gpr[6])
	}
	return uintptr(r.regs.Gpr[5])
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gpr[4] = uint64(addr)
	} else {
		r.regs.Gpr[3] = uint64(addr)
		r.regs.Orig_gpr3 = uint64(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gpr[5] = uint64(addr)
	} else {
		r.regs.Gpr[4] = uint64(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Gpr[6] = uint64(addr)
	} else {
		r.regs.Gpr[5] = uint64(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Gpr[1])
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
