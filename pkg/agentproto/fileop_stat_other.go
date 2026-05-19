//go:build !linux && !darwin && !freebsd

package agentproto

import "syscall"

func augmentStatFromSys(*FileStat, any) {}

func unixModeFromSys(any) (uint32, bool) { return 0, false }

func handleStatfs(*FileOpReq) *FileOpResp {
	return &FileOpResp{Errno: uint32(syscall.ENOSYS), ErrMsg: "statfs unsupported"}
}
