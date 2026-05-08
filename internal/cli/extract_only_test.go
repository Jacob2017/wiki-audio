package cli

import (
	"errors"
	"strings"
	"testing"
)

// Tests for wa-kyn.15 — `wiki-audio build --extract-only --slug X`.
// Reuses buildFixture / canonicalEssay / runBuild helpers from
// build_test.go (same package).

func TestBuildExtractOnly_HappyPathPrintsBody(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha Essay",
			"This is the first paragraph of Alpha. It has enough text to clear MinBodyChars."),
		"beta.md": canonicalEssay("Beta Essay", "Beta body, irrelevant to this test."),
	})

	stdout, _, err := runBuild(t, cfgPath, envPath,
		"build", "--extract-only", "--slug", "alpha")
	if err != nil {
		t.Fatalf("extract-only failed: %v", err)
	}

	// Body must contain prose from the requested essay.
	if !strings.Contains(stdout, "This is the first paragraph of Alpha.") {
		t.Errorf("alpha prose missing from extract-only output:\n%s", stdout)
	}
	// Must NOT contain the other essay's body.
	if strings.Contains(stdout, "Beta body") {
		t.Errorf("extract-only should narrow to --slug; got beta content:\n%s", stdout)
	}
	// Must NOT contain dry-run summary lines (no chunking, no
	// credits/dollars output).
	if strings.Contains(stdout, "credits/char") {
		t.Errorf("extract-only should NOT emit dry-run summary; got:\n%s", stdout)
	}
	if strings.Contains(stdout, "Pro tier overage") {
		t.Errorf("extract-only should NOT emit cost estimate; got:\n%s", stdout)
	}
}

func TestBuildExtractOnly_RequiresSlug(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body filler line for MinBodyChars."),
	})
	_, _, err := runBuild(t, cfgPath, envPath, "build", "--extract-only")
	if err == nil {
		t.Fatal("expected error when --extract-only used without --slug")
	}
	if !errors.Is(err, errExtractOnlyRequiresSlug) {
		t.Errorf("expected errExtractOnlyRequiresSlug; got %v", err)
	}
}

func TestBuildExtractOnly_SlugNotFound(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	_, _, err := runBuild(t, cfgPath, envPath,
		"build", "--extract-only", "--slug", "nonexistent")
	if err == nil {
		t.Fatal("expected error for unmatched slug")
	}
	if !strings.Contains(err.Error(), "no essay matched") {
		t.Errorf("error should explain no match; got %v", err)
	}
}

// --extract-only and --dry-run are mutually exclusive: cobra
// MarkFlagsMutuallyExclusive enforces. Verify the user gets a clear
// error rather than ambiguous behavior.
func TestBuildExtractOnly_MutuallyExclusiveWithDryRun(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	_, _, err := runBuild(t, cfgPath, envPath,
		"build", "--extract-only", "--dry-run", "--slug", "alpha")
	if err == nil {
		t.Fatal("expected error for incompatible flags")
	}
	msg := err.Error()
	if !strings.Contains(msg, "extract-only") || !strings.Contains(msg, "dry-run") {
		t.Errorf("error should name both conflicting flags; got %v", err)
	}
}

// The output is the exact body the dry-run pipeline would feed to
// the chunker — i.e. doc.Body terminated by a single "\n". An
// operator pipes the output to less or to a diff and expects no
// extra noise (no headers, no trailing blank lines beyond one).
func TestBuildExtractOnly_OutputHasSingleTrailingNewline(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha",
			"Alpha prose with enough content to satisfy MinBodyChars on extraction."),
	})
	stdout, _, err := runBuild(t, cfgPath, envPath,
		"build", "--extract-only", "--slug", "alpha")
	if err != nil {
		t.Fatalf("extract-only failed: %v", err)
	}
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("expected trailing newline; got %q", stdout[len(stdout)-min(20, len(stdout)):])
	}
	if strings.HasSuffix(stdout, "\n\n\n") {
		t.Errorf("output has too many trailing newlines: %q", stdout[len(stdout)-min(20, len(stdout)):])
	}
}

// --extract-only must not crash if the essay's body is below
// MinBodyChars. The extractor will mark it Malformed; we still print
// what we have so the operator can see the malformed result and
// decide whether to fix the source.
func TestBuildExtractOnly_MalformedEssayStillPrints(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		// Bypass the canonicalEssay padding to deliberately produce
		// a malformed body.
		"tiny.md": "# Tiny\n\n## Metadata\n- Author: Test\n\n## Full Document\nshort.\n",
	})
	stdout, _, err := runBuild(t, cfgPath, envPath,
		"build", "--extract-only", "--slug", "tiny")
	if err != nil {
		t.Fatalf("extract-only on malformed essay should not error; got %v", err)
	}
	// Body is whatever Finalize produced (likely just "short." after
	// cleanup) — we just want the command to complete cleanly.
	if stdout == "" {
		t.Errorf("expected some output even for a malformed essay")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
