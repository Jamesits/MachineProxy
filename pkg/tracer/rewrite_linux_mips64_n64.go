//go:build linux && (mips64 || mips64le)

package tracer

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// MIPS n64 syscall numbers (5000 base).
const (
	sysExecve   = 5057
	sysExecveat = 5316
)

// Register indices in the MIPS Regs[32] layout.
const (
	mipsV0 = 2  // syscall number
	mipsA0 = 4  // arg0
	mipsA1 = 5  // arg1
	mipsA2 = 6  // arg2
	mipsA3 = 7  // arg3
	mipsA4 = 8  // arg4 (n64 has 8 argument registers a0..a7)
	mipsSP = 29 // stack pointer
)

// SyscallRegs holds the register state for a syscall on MIPS n64.
// On execve(pathname, argv, envp):
//   - v0 = syscall number (with offset 5000)
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
	return r.regs.Regs[mipsV0]
}

func (r *SyscallRegs) BlockSyscall() {
	r.regs.Regs[mipsV0] = ^uint64(0)
}

func (r *SyscallRegs) PathAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[mipsA1])
	}
	return uintptr(r.regs.Regs[mipsA0])
}

func (r *SyscallRegs) ArgvAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[mipsA2])
	}
	return uintptr(r.regs.Regs[mipsA1])
}

func (r *SyscallRegs) EnvpAddr(isExecveat bool) uintptr {
	if isExecveat {
		return uintptr(r.regs.Regs[mipsA3])
	}
	return uintptr(r.regs.Regs[mipsA2])
}

func (r *SyscallRegs) SetPathAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[mipsA1] = uint64(addr)
	} else {
		r.regs.Regs[mipsA0] = uint64(addr)
	}
}

func (r *SyscallRegs) SetArgvAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[mipsA2] = uint64(addr)
	} else {
		r.regs.Regs[mipsA1] = uint64(addr)
	}
}

func (r *SyscallRegs) SetEnvpAddr(isExecveat bool, addr uintptr) {
	if isExecveat {
		r.regs.Regs[mipsA3] = uint64(addr)
	} else {
		r.regs.Regs[mipsA2] = uint64(addr)
	}
}

func (r *SyscallRegs) StackPointer() uintptr {
	return uintptr(r.regs.Regs[mipsSP])
}

// MIPS signals a syscall error by setting a3 (Regs[mipsA3]) non-zero and
// placing errno in v0, rather than returning a negative result.
func (r *SyscallRegs) Ret() (uintptr, bool) {
	return uintptr(r.regs.Regs[mipsV0]), r.regs.Regs[mipsA3] == 0
}

func (r *SyscallRegs) SetRetSuccess(v uintptr) {
	r.regs.Regs[mipsV0] = uint64(v)
	r.regs.Regs[mipsA3] = 0
}

func (r *SyscallRegs) SetRetError(errno uintptr) {
	r.regs.Regs[mipsV0] = uint64(errno)
	r.regs.Regs[mipsA3] = 1
}

const ptrSize = int(unsafe.Sizeof(uintptr(0)))
