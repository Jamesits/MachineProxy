//go:build windows

package subreaper

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Setup creates a Job Object configured to terminate every process in
// the job when the current process exits, then assigns the current
// process to it. Children created without CREATE_BREAKAWAY_FROM_JOB
// inherit job membership, so the whole tree dies with us — the
// Windows analogue of the unix subreaper + cleanup behavior.
func Setup() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}

	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	// Intentionally leak the job handle: it must outlive any children.
	// When this process exits the OS closes it for us and
	// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE terminates every member.
	return nil
}
