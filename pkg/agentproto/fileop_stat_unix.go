//go:build linux || darwin || freebsd

package agentproto

import "syscall"

func augmentStatFromSys(out *FileStat, sys any) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok || st == nil {
		return
	}
	out.UID = st.Uid
	out.GID = st.Gid
	out.Ino = uint64(st.Ino)
	out.Rdev = uint32(st.Rdev)
	out.Blocks = uint64(st.Blocks)
	out.Blksize = uint32(st.Blksize)
	out.Nlink = uint32(st.Nlink)
	fillStatTimes(out, st)
}

func unixModeFromSys(sys any) (uint32, bool) {
	st, ok := sys.(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return uint32(st.Mode), true
}

func statfsFromSyscall(st *syscall.Statfs_t) *FileStatfs {
	return statfsFromSyscallOS(st)
}

func handleStatfs(req *FileOpReq) *FileOpResp {
	var st syscall.Statfs_t
	if err := syscall.Statfs(req.Path, &st); err != nil {
		return errResp(err)
	}
	return &FileOpResp{Statfs: statfsFromSyscall(&st)}
}
