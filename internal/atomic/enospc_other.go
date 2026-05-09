//go:build !unix

package atomic

import "errors"

// On non-unix platforms ENOSPC isn't a stable syscall constant;
// IsSpaceError will simply never report true, which is a fine
// degradation given wiki-audio's primary targets are Linux + macOS.
func enospcSentinel() error { return errors.New("atomic: ENOSPC sentinel (non-unix)") }
