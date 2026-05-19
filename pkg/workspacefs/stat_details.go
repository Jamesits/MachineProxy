package workspacefs

import (
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/jamesits/machineproxy/pkg/agentproto"
)

func applyStatDetails(out *fuse.Attr, sys any) {
	switch st := sys.(type) {
	case agentproto.FileStat:
		applyAgentStat(out, st)
	case *agentproto.FileStat:
		if st != nil {
			applyAgentStat(out, *st)
		}
	}
	applyStatDetailsOS(out, sys)
}

func applyAgentStat(out *fuse.Attr, st agentproto.FileStat) {
	if st.ATimeNanos != 0 {
		setFuseTime(&out.Atime, &out.Atimensec, time.Unix(0, st.ATimeNanos))
	}
	if st.MTimeNanos != 0 {
		setFuseTime(&out.Mtime, &out.Mtimensec, time.Unix(0, st.MTimeNanos))
	}
	if st.CTimeNanos != 0 {
		setFuseTime(&out.Ctime, &out.Ctimensec, time.Unix(0, st.CTimeNanos))
	}
	out.Uid = st.UID
	out.Gid = st.GID
	if st.Nlink != 0 {
		out.Nlink = st.Nlink
	}
	out.Rdev = st.Rdev
	if st.Blocks != 0 {
		out.Blocks = st.Blocks
	}
	if st.Blksize != 0 {
		out.Blksize = st.Blksize
	}
	if st.Ino != 0 {
		out.Ino = st.Ino
	}
}

func setFuseTime(sec *uint64, nsec *uint32, t time.Time) {
	*sec = uint64(t.Unix())
	*nsec = uint32(t.Nanosecond())
}
