package agentproto

import (
	"os"
)

func fileModeFromPOSIX(mode uint32) os.FileMode {
	out := os.FileMode(mode & 0o777)
	in := os.FileMode(mode)
	if mode&0o4000 != 0 || in&os.ModeSetuid != 0 {
		out |= os.ModeSetuid
	}
	if mode&0o2000 != 0 || in&os.ModeSetgid != 0 {
		out |= os.ModeSetgid
	}
	if mode&0o1000 != 0 || in&os.ModeSticky != 0 {
		out |= os.ModeSticky
	}
	return out
}

func modeFromUnix(mode uint32) os.FileMode {
	out := fileModeFromPOSIX(mode)
	switch mode & unixSIFMT {
	case unixSIFDIR:
		out |= os.ModeDir
	case unixSIFLNK:
		out |= os.ModeSymlink
	case unixSIFIFO:
		out |= os.ModeNamedPipe
	case unixSIFSOCK:
		out |= os.ModeSocket
	case unixSIFBLK:
		out |= os.ModeDevice
	case unixSIFCHR:
		out |= os.ModeDevice | os.ModeCharDevice
	}
	return out
}

const (
	unixSIFMT   = 0o170000
	unixSIFIFO  = 0o010000
	unixSIFCHR  = 0o020000
	unixSIFDIR  = 0o040000
	unixSIFBLK  = 0o060000
	unixSIFREG  = 0o100000
	unixSIFLNK  = 0o120000
	unixSIFSOCK = 0o140000
)
