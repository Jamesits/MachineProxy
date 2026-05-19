//go:build darwin

package main

import (
	"errors"
	"os"
)

// preflightFUSE verifies that macFUSE (or its predecessor osxfuse) is
// installed before any FUSE mount is attempted. go-fuse will exec one
// of these helpers under /Library/Filesystems/<bundle>/Contents/
// Resources/ to talk to the FUSE kext; if neither exists the mount
// fails with an opaque fork/exec error. Surfacing the missing
// dependency up-front lets us point the operator at the install page.
func preflightFUSE() error {
	candidates := []string{
		"/Library/Filesystems/macfuse.fs/Contents/Resources/mount_macfuse",
		"/Library/Filesystems/osxfuse.fs/Contents/Resources/mount_osxfuse",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return nil
		}
	}
	return errors.New("macFUSE is not installed; the workspace FUSE mount requires it. " +
		"Install via 'brew install --cask macfuse' or download from https://macfuse.io")
}
