//go:build unix

package atomic

import "syscall"

func enospcSentinel() error { return syscall.ENOSPC }
