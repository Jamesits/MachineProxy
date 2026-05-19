//go:build darwin

package agentproto

import "syscall"

func fillStatTimes(out *FileStat, st *syscall.Stat_t) {
	out.ATimeNanos = st.Atimespec.Nano()
	out.MTimeNanos = st.Mtimespec.Nano()
	out.CTimeNanos = st.Ctimespec.Nano()
}

func statfsFromSyscallOS(st *syscall.Statfs_t) *FileStatfs {
	return &FileStatfs{
		Blocks:  st.Blocks,
		Bfree:   st.Bfree,
		Bavail:  st.Bavail,
		Files:   st.Files,
		Ffree:   st.Ffree,
		Bsize:   st.Bsize,
		Frsize:  uint32(st.Iosize),
		NameLen: 1024,
	}
}
