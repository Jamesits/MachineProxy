//go:build linux && mips

package tracer

const auditArchMIPS = 0x00000008

func SeccompArch() uint32 { return auditArchMIPS }
