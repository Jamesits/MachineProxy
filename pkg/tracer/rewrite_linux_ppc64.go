package tracer

const auditArchPPC64 = 0x80000015

func SeccompArch() uint32 { return auditArchPPC64 }
