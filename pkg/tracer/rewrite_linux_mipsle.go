package tracer

const auditArchMIPSEL = 0x40000008

func SeccompArch() uint32 { return auditArchMIPSEL }
