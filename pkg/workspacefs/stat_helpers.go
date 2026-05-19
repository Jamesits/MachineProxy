package workspacefs

import (
	"os"
	"time"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func currentATime(st os.FileInfo) time.Time {
	switch s := st.Sys().(type) {
	case agentproto.FileStat:
		if s.ATimeNanos != 0 {
			return time.Unix(0, s.ATimeNanos)
		}
	case *agentproto.FileStat:
		if s != nil && s.ATimeNanos != 0 {
			return time.Unix(0, s.ATimeNanos)
		}
	}
	if t, ok := currentATimeOS(st.Sys()); ok {
		return t
	}
	return st.ModTime()
}

func currentUID(sys any) uint32 {
	switch s := sys.(type) {
	case agentproto.FileStat:
		return s.UID
	case *agentproto.FileStat:
		if s != nil {
			return s.UID
		}
	}
	if uid, ok := currentUIDOS(sys); ok {
		return uid
	}
	return 0
}

func currentGID(sys any) uint32 {
	switch s := sys.(type) {
	case agentproto.FileStat:
		return s.GID
	case *agentproto.FileStat:
		if s != nil {
			return s.GID
		}
	}
	if gid, ok := currentGIDOS(sys); ok {
		return gid
	}
	return 0
}
