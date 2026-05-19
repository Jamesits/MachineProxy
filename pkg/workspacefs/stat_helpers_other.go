//go:build !linux && !darwin && !freebsd

package workspacefs

import "time"

func currentATimeOS(any) (time.Time, bool) { return time.Time{}, false }
func currentUIDOS(any) (uint32, bool)      { return 0, false }
func currentGIDOS(any) (uint32, bool)      { return 0, false }
