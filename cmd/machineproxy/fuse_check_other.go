//go:build !darwin

package main

// preflightFUSE is a no-op on non-darwin hosts. Linux provides FUSE
// in-kernel via the package's "fuse" runtime dependency; any failure
// surfaces clearly through the kernel's own error path.
func preflightFUSE() error { return nil }
