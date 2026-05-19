//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "mproxy-tracer is supported only on Linux (uses ptrace syscall-stop interception)")
	os.Exit(1)
}
