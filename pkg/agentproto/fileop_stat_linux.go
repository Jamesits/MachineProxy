//go:build linux

package agentproto

import "syscall"

func fillStatTimes(out *FileStat, st *syscall.Stat_t) {
	out.ATimeNanos = st.Atim.Nano()
	out.MTimeNanos = st.Mtim.Nano()
	out.CTimeNanos = st.Ctim.Nano()
}

func statfsFromSyscallOS(st *syscall.Statfs_t) *FileStatfs {
	return &FileStatfs{
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Bsize:   uint32(st.Bsize),
		Frsize:  uint32(st.Frsize),
		NameLen: uint32(st.Namelen),
	}
}
