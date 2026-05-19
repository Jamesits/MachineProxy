//go:build !linux && !darwin && !freebsd

package workspacefs

import "github.com/hanwen/go-fuse/v2/fuse"

func applyStatDetailsOS(*fuse.Attr, any) {}
