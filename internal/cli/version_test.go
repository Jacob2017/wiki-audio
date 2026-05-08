package cli

import (
	"regexp"
	"strings"
	"testing"
)

// Tests for wa-8gt.4: --version format pinned to
// "wiki-audio <Version> (commit <Commit>, built <BuildTime>)" with
// graceful fallback to runtime/debug VCS info when ldflags weren't
// injected.

// TestVersionLineDefaultsAreSafe — without ldflags injection, the
// Version variable is "dev" and the format must still produce a
// single, non-empty, well-formed line. Catches a future refactor
// that introduces a panic on uninitialised globals.
func TestVersionLineDefaultsAreSafe(t *testing.T) {
	got := VersionLine()
	if got == "" {
		t.Fatal("VersionLine returned empty string")
	}
	if !strings.HasPrefix(got, "wiki-audio ") {
		t.Errorf("expected 'wiki-audio ' prefix; got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("expected single-line output; got %q", got)
	}
}

// Pin the format string against the §3 sample so a future field
// reorder is loud. Construct VersionLine output with known values
// and assert the exact shape.
func TestVersionLineFormatMatchesSpec(t *testing.T) {
	// Save and restore globals — tests must be hermetic.
	origV, origC, origB := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = origV, origC, origB })

	Version = "v0.3.1"
	Commit = "abc1234"
	BuildTime = "2026-05-08T14:00:00Z"

	got := VersionLine()
	want := "wiki-audio v0.3.1 (commit abc1234, built 2026-05-08T14:00:00Z)"
	if got != want {
		t.Errorf("VersionLine = %q\nwant         = %q", got, want)
	}
}

// Dev build with no ldflags injection AND no VCS info available
// should render the unobtrusive fallback. Tests reach in to set the
// globals directly because forcing debug.ReadBuildInfo to return
// nothing requires a non-test binary path.
func TestVersionLineDevWithUnknownsFallback(t *testing.T) {
	origV, origC, origB := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = origV, origC, origB })

	Version = "dev"
	// Inside `go test`, ReadBuildInfo() does return the test
	// binary's VCS info if the source is in a git repo. We can
	// verify either: (a) the format is correct OR (b) the fallback
	// fires. Both branches are valid; assert structure rather than
	// exact strings.
	Commit, BuildTime = "unknown", "unknown"

	got := VersionLine()
	if !strings.HasPrefix(got, "wiki-audio dev (") {
		t.Errorf("expected 'wiki-audio dev (' prefix; got %q", got)
	}
	if !strings.HasSuffix(got, ")") {
		t.Errorf("expected trailing ')'; got %q", got)
	}
}

// shortCommit must yield a 7-char prefix on a typical git SHA, and
// must not panic on short or non-hex input.
func TestShortCommit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abcdef1234567890", "abcdef1"},
		{"ABCDEF1234567890", "abcdef1"}, // lowercased
		{"abc", "abc"},                  // shorter than 7 — return as-is
		{"", ""},
		{"not-a-hex-string", "not-a-h"}, // non-hex — still cut at 7
	}
	for _, c := range cases {
		if got := shortCommit(c.in); got != c.want {
			t.Errorf("shortCommit(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Format-shape regex against arbitrary global state — any
// permutation of injected/uninjected values must yield a string
// matching the spec contract.
var versionRE = regexp.MustCompile(`^wiki-audio \S+ \(commit \S+, built \S+\)$`)

func TestVersionLineMatchesShapeRegexAcrossPermutations(t *testing.T) {
	origV, origC, origB := Version, Commit, BuildTime
	t.Cleanup(func() { Version, Commit, BuildTime = origV, origC, origB })

	cases := []struct {
		v, c, b string
	}{
		{"dev", "unknown", "unknown"},
		{"v0.0.1", "abc1234", "2026-01-01T00:00:00Z"},
		{"v1.2.3", "deadbeef", "now"},
		{"snapshot", "0123abc", "today"},
	}
	for _, c := range cases {
		Version, Commit, BuildTime = c.v, c.c, c.b
		got := VersionLine()
		if !versionRE.MatchString(got) {
			t.Errorf("output doesn't match shape: %q", got)
		}
	}
}
