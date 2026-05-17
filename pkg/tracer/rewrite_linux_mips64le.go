package tracer

const auditArchMIPSEL64 = 0xC0000008

func SeccompArch() uint32 { return auditArchMIPSEL64 }
