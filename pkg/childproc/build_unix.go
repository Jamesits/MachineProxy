//go:build unix

package childproc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// Build creates an *exec.Cmd from spec with:
//   - A fresh process group (Setpgid) so signals can be delivered to
//     the whole tree.
//   - Optional credential drop based on $SUDO_UID/$SUDO_GID.
//   - Pipes for stdin/stdout/stderr.
//   - Unix socketpairs for each requested extra fd.
//
// The returned Pipes holds the parent-side ends. The caller must Close
// them when done.
func Build(spec Spec) (*exec.Cmd, *Pipes, error) {
	cmd := newCmd(spec)

	sysProcAttr := &syscall.SysProcAttr{
		Setpgid: true,
	}

	if spec.DropSudoCredentials {
		if sudoUID := os.Getenv("SUDO_UID"); sudoUID != "" {
			uid, err := strconv.ParseUint(sudoUID, 10, 32)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid SUDO_UID %q: %w", sudoUID, err)
			}
			gid := uid // fall back to UID if no GID is present
			if sudoGID := os.Getenv("SUDO_GID"); sudoGID != "" {
				g, err := strconv.ParseUint(sudoGID, 10, 32)
				if err != nil {
					return nil, nil, fmt.Errorf("invalid SUDO_GID %q: %w", sudoGID, err)
				}
				gid = g
			}
			sysProcAttr.Credential = &syscall.Credential{
				Uid: uint32(uid),
				Gid: uint32(gid),
			}
		}
	}

	cmd.SysProcAttr = sysProcAttr

	pipes, err := setupStdPipes(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("std pipes: %w", err)
	}

	for _, fdNum := range spec.ExtraFDs {
		agentEnd, childEnd, err := socketpair()
		if err != nil {
			pipes.Close()
			return nil, nil, fmt.Errorf("socketpair for fd %d: %w", fdNum, err)
		}
		// ExtraFiles[i] becomes fd 3+i in the child; pad to align.
		idx := int(fdNum) - 3
		for len(cmd.ExtraFiles) <= idx {
			cmd.ExtraFiles = append(cmd.ExtraFiles, nil)
		}
		cmd.ExtraFiles[idx] = childEnd
		pipes.Extra[fdNum] = agentEnd
	}

	return cmd, pipes, nil
}

func socketpair() (parent *os.File, child *os.File, err error) {
	// macOS does not expose SOCK_CLOEXEC, so set FD_CLOEXEC explicitly
	// on the parent end after creation. The child end stays inheritable.
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}
	parent = os.NewFile(uintptr(fds[0]), "socketpair-parent")
	child = os.NewFile(uintptr(fds[1]), "socketpair-child")

	if _, err := unix.FcntlInt(uintptr(fds[0]), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		_ = parent.Close()
		_ = child.Close()
		return nil, nil, err
	}
	return parent, child, nil
}
