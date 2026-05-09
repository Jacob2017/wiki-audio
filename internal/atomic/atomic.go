package atomic

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteFile is the atomic mirror of os.WriteFile. data is buffered
// into a temp file in filepath.Dir(path), fsynced, chmod'd to perm,
// then renamed onto path. A crash, ENOSPC, or permission error
// before the Rename leaves any pre-existing target unchanged.
//
// The signature matches os.WriteFile so call sites can swap one for
// the other without rewriting their argument list. perm is honored
// regardless of the process umask (CreateTemp's default 0o600 is
// overwritten by an explicit Chmod before Rename), which means
// secrets-adjacent files like .env keep their 0o600 mode without a
// follow-up os.Chmod call.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return WriteAtomic(path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}, perm)
}

// WriteAtomic is the streamed variant. write is invoked with an
// io.Writer that points at a temp file in filepath.Dir(path). On a
// successful return the temp file is fsynced, closed, chmod'd to
// perm, and atomically renamed onto path. On any error the temp is
// removed and path is unchanged.
//
// Use WriteAtomic when the content does not naturally fit a []byte
// (e.g. an MP3 piped from io.Copy) so the file does not have to be
// buffered in memory before flush.
//
// Atomicity caveat: os.Rename is atomic only within a single
// filesystem. The temp file is always created in the SAME directory
// as the target, so within-fs atomicity is the common case. A
// cross-fs target would surface as a Rename error here rather than
// a silent partial replacement.
func WriteAtomic(path string, write func(io.Writer) error, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, ".tmp-"+base+"-*")
	if err != nil {
		return fmt.Errorf("atomic: create tmp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Belt-and-suspenders: any error after this point removes the
	// temp file. Successful path returns before the deferred remove
	// would matter (Rename has already disposed of tmpName).
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if err := write(tmp); err != nil {
		return fmt.Errorf("atomic: write %s: %w", path, err)
	}

	// fsync ensures the data hits the disk before Rename publishes
	// the new inode at path. Without it a power-loss after Rename
	// but before the kernel flushed the data could leave a
	// zero-length file at path.
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("atomic: sync %s: %w", tmpName, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic: close %s: %w", tmpName, err)
	}

	// Chmod BEFORE Rename so the file is published with its
	// intended mode atomically; doing it after has a window where
	// the target carries CreateTemp's restrictive 0o600.
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("atomic: chmod %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomic: rename %s → %s: %w", tmpName, path, err)
	}
	committed = true
	return nil
}

// IsSpaceError returns true when err matches an "out of space"
// signal (ENOSPC). Provided so callers — e.g. wa-cfn.2's bulk
// build — can distinguish a benign "user's disk filled up" from a
// real bug. errors.Is is used under the hood so wrapped errors are
// detected.
func IsSpaceError(err error) bool {
	return errors.Is(err, errENOSPC)
}

// errENOSPC is the sentinel that IsSpaceError checks. We stash it
// in a package-level var rather than syscalling at every check site
// so the Go cross-platform constant table is consulted exactly once.
var errENOSPC = enospcSentinel()
