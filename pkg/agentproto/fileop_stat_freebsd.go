//go:build freebsd

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
		Bavail:  uint64(st.Bavail),
		Files:   st.Files,
		Ffree:   uint64(st.Ffree),
		Bsize:   uint32(st.Bsize),
		Frsize:  uint32(st.Iosize),
		NameLen: 255,
	}
}
