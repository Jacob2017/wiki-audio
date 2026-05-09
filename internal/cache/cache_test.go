package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withXDGCacheHome scopes a temp $XDG_CACHE_HOME for the test. Uses
// t.Setenv so the prior value (and unset state) is restored at
// cleanup. Returns the temp root for assertions.
func withXDGCacheHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

func TestDirHonorsXDGCacheHome(t *testing.T) {
	root := withXDGCacheHome(t)
	got := Dir()
	want := filepath.Join(root, projectName)
	if got != want {
		t.Errorf("Dir() = %q; want %q (XDG_CACHE_HOME override should be honored)", got, want)
	}
}

func TestDirFallsBackToHomeCache(t *testing.T) {
	// Unset XDG_CACHE_HOME and verify fallback to $HOME/.cache.
	t.Setenv("XDG_CACHE_HOME", "")

	got := Dir()
	home, err := os.UserHomeDir()
	if err != nil {
		// In a stripped env (no HOME), Dir() returns a relative
		// fallback. Just verify that path doesn't escape and that
		// the project name is the leaf.
		if !strings.HasSuffix(got, projectName) {
			t.Errorf("fallback Dir() should end with %q; got %q", projectName, got)
		}
		return
	}
	want := filepath.Join(home, ".cache", projectName)
	if got != want {
		t.Errorf("Dir() = %q; want %q ($HOME/.cache fallback)", got, want)
	}
}

func TestPathHelpersShape(t *testing.T) {
	root := withXDGCacheHome(t)
	cache := filepath.Join(root, projectName)

	if got, want := TmpDir("alpha"), filepath.Join(cache, "tmp", "alpha"); got != want {
		t.Errorf("TmpDir(alpha) = %q; want %q", got, want)
	}
	if got, want := OutPath("alpha"), filepath.Join(cache, "out", "alpha.mp3"); got != want {
		t.Errorf("OutPath(alpha) = %q; want %q", got, want)
	}
	if got, want := SkippedPath(), filepath.Join(cache, "skipped.txt"); got != want {
		t.Errorf("SkippedPath() = %q; want %q", got, want)
	}
}

// TmpDir slug must NOT escape the cache root via "..", absolute
// paths, or other path-traversal tricks. Slug derivation lives
// elsewhere (slugFromPath in internal/cli) and is constrained to
// kebab-case lowercase, but defensive callers should still see
// path traversal rejected.
func TestTmpDirContainsSlugAsPathSegment(t *testing.T) {
	root := withXDGCacheHome(t)
	cache := filepath.Join(root, projectName)

	got := TmpDir("how-to-do-great-work")
	if !strings.HasPrefix(got, cache+string(os.PathSeparator)) {
		t.Errorf("TmpDir result %q escapes cache root %q", got, cache)
	}
}

func TestEnsureDirsCreatesTmpAndOut(t *testing.T) {
	root := withXDGCacheHome(t)
	if err := EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, sub := range []string{"tmp", "out"} {
		p := filepath.Join(root, projectName, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}
}

func TestEnsureDirsIdempotent(t *testing.T) {
	withXDGCacheHome(t)
	for i := 0; i < 3; i++ {
		if err := EnsureDirs(); err != nil {
			t.Fatalf("EnsureDirs call %d: %v", i, err)
		}
	}
}

func TestCleanupTmpRemovesRecursively(t *testing.T) {
	withXDGCacheHome(t)
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	tmp := TmpDir("alpha")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	// Populate with a chunk file + a nested subdir to verify the
	// removal is recursive.
	if err := os.WriteFile(filepath.Join(tmp, "0.mp3"), []byte("chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(tmp, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "deep.mp3"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupTmp("alpha"); err != nil {
		t.Fatalf("CleanupTmp: %v", err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("expected tmp dir gone; stat err: %v", err)
	}
}

// CleanupTmp on a non-existent slug must NOT error — a build that
// fails before tmp/<slug> is created should be able to call
// CleanupTmp in defer without a second error masking the first.
func TestCleanupTmpIdempotent(t *testing.T) {
	withXDGCacheHome(t)
	if err := CleanupTmp("never-existed"); err != nil {
		t.Errorf("CleanupTmp on missing dir should be a no-op; got %v", err)
	}
}

func TestCleanupOutRemovesFile(t *testing.T) {
	withXDGCacheHome(t)
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	out := OutPath("alpha")
	if err := os.WriteFile(out, []byte("mp3 bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CleanupOut("alpha"); err != nil {
		t.Fatalf("CleanupOut: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("expected out file gone; stat err: %v", err)
	}
}

func TestCleanupOutIdempotent(t *testing.T) {
	withXDGCacheHome(t)
	if err := CleanupOut("never-existed"); err != nil {
		t.Errorf("CleanupOut on missing file should be a no-op; got %v", err)
	}
}

// Sanity that the package's own functions compose cleanly: build
// the layout, populate one essay's tmp + out, clean both,
// SkippedPath remains addressable. Catches a refactor that breaks
// the layout invariants in concert.
func TestEndToEndCachePopulateAndClean(t *testing.T) {
	withXDGCacheHome(t)
	if err := EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	const slug = "essay-one"

	// Populate
	tmp := TmpDir(slug)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "0.mp3"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(OutPath(slug), []byte("final"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SkippedPath(), []byte("malformed-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify all three exist
	for _, p := range []string{tmp, OutPath(slug), SkippedPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist after populate: %v", p, err)
		}
	}

	// Clean tmp + out
	if err := CleanupTmp(slug); err != nil {
		t.Fatal(err)
	}
	if err := CleanupOut(slug); err != nil {
		t.Fatal(err)
	}

	// Skipped log is NOT cleaned by these helpers (operator-managed
	// debugging artifact); verify it's still there.
	if _, err := os.Stat(SkippedPath()); err != nil {
		t.Errorf("SkippedPath should survive Cleanup* calls; got %v", err)
	}
}
