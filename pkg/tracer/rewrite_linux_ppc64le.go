package tracer

const auditArchPPC64LE = 0xC0000015

func SeccompArch() uint32 { return auditArchPPC64LE }
