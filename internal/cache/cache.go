package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// projectName is the leaf directory under the XDG cache root.
// Pinned here (not derived from cmd/) so a future binary rename
// requires a deliberate edit.
const projectName = "wiki-audio"

// Dir returns the absolute path to the wiki-audio cache directory,
// honoring $XDG_CACHE_HOME when set. Per the XDG basedir spec the
// default is $HOME/.cache. If neither $XDG_CACHE_HOME nor a usable
// home directory is available (e.g. a stripped CI image), Dir falls
// back to a relative ".cache/wiki-audio" so callers can still
// proceed; that path resolves against the current working directory
// at use-site.
func Dir() string {
	if base := os.Getenv("XDG_CACHE_HOME"); base != "" {
		return filepath.Join(base, projectName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".cache", projectName)
	}
	return filepath.Join(home, ".cache", projectName)
}

// TmpDir returns the per-essay scratch directory under tmp/. Build
// stages write per-chunk intermediates (e.g. tmp/<slug>/0.mp3) here
// and CleanupTmp(slug) removes the whole subtree after a successful
// concat. On concat failure the directory is preserved so the
// operator can rerun ffmpeg by hand against the raw chunks (§6).
func TmpDir(slug string) string {
	return filepath.Join(Dir(), "tmp", slug)
}

// OutPath returns the absolute path of the final concatenated MP3
// for essay slug, awaiting publish. The publish pipeline (Phase F)
// reads from this path and uploads to R2.
func OutPath(slug string) string {
	return filepath.Join(Dir(), "out", slug+".mp3")
}

// SkippedPath returns the absolute path of the malformed-essay log
// (§6). One slug per line — append-only. Reads at the start of a
// build give the operator a list of essays that have been quietly
// skipped across runs.
func SkippedPath() string {
	return filepath.Join(Dir(), "skipped.txt")
}

// EnsureDirs creates the tmp/ and out/ subdirectories with
// permissive 0o755 (regeneratable data — readable by other users on
// a shared machine is fine; secrets do NOT live here, they live in
// ~/.wiki-audio/.env). The cache root is created implicitly via
// MkdirAll. Idempotent: safe to call at every build start.
func EnsureDirs() error {
	for _, p := range []string{
		filepath.Join(Dir(), "tmp"),
		filepath.Join(Dir(), "out"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("cache: mkdir %s: %w", p, err)
		}
	}
	return nil
}

// CleanupTmp removes tmp/<slug>/ recursively. Called by the build
// pipeline after a successful concat (Phase E call site:
// internal/cli/build.go). Idempotent — removing a non-existent
// directory is not an error, so a rerun on a clean cache is safe.
func CleanupTmp(slug string) error {
	p := TmpDir(slug)
	if err := os.RemoveAll(p); err != nil {
		return fmt.Errorf("cache: cleanup tmp %s: %w", p, err)
	}
	return nil
}

// CleanupOut removes out/<slug>.mp3. NOT called automatically by
// the publish pipeline; the package-level cleanup policy (see
// doc.go) is to KEEP out/ artifacts for fast republish on token
// rotation. Operators who hit disk pressure can call this manually
// (or wire it behind a future --prune-out flag in publish).
// Idempotent: a missing file is not an error.
func CleanupOut(slug string) error {
	p := OutPath(slug)
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cache: cleanup out %s: %w", p, err)
	}
	return nil
}
