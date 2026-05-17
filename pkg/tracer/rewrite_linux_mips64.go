package tracer

const auditArchMIPS64 = 0x80000008

func SeccompArch() uint32 { return auditArchMIPS64 }
