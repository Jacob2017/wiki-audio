package feed

// wa-i1l.15 golden-file test. Generates pg.xml from a 3-entry fixture
// (mirroring the project's 3 example essays) and compares byte-for-
// byte to a checked-in golden file. Regenerate with:
//
//	go test ./internal/feed -run TestGoldenRSS_3Essays -update
//
// The -update flag convention matches internal/e2e and internal/cli
// — same name, same semantics, same project-wide muscle memory.

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// update is the -update flag for regenerating the golden file. Lives
// in this _test.go because flag.Bool is process-wide and we want it
// only when the feed test binary is built (not the e2e binary which
// has its own `update` flag, naturally).
var update = flag.Bool("update", false, "regenerate golden files from current generator output")

// goldenFixtureChannel is the fixed-input Channel. All values are
// hand-picked so the generated XML is fully deterministic — varying
// any of them would change the golden bytes.
func goldenFixtureChannel() Channel {
	return Channel{
		Title:       "PG Essays",
		Description: "Paul Graham essays read aloud.",
		Author:      "Paul Graham",
		OwnerEmail:  "owner@example.test",
		Language:    "en-us",
		Link:        "https://feed.example.test",
		SelfLinkURL: "https://feed.example.test/pg.xml?t=GOLDEN",
	}
}

// goldenFixtureEntries returns the 3-entry fixture mirroring the
// project's example essays. PublishedAt times are pinned to fixed
// UTC instants so the golden bytes are stable across runs and CI
// hosts. Sizes and durations come from the wa-fse spike output
// (rounded), so a future PR that breaks frame preservation will
// also surface here as a golden drift.
func goldenFixtureEntries() []model.ManifestEntry {
	pubGreatWork := time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC)
	pubStartupIdeas := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	pubHighAgency := time.Date(2026, 5, 10, 9, 30, 0, 0, time.UTC)
	return []model.ManifestEntry{
		entry("how-to-do-great-work", "How to Do Great Work", 3664.27, 58629016, pubGreatWork),
		entry("how-to-get-startup-ideas", "How to Get Startup Ideas", 1800, 24000000, pubStartupIdeas),
		entry("high-agency-in-30-minutes", "High Agency", 1500, 18000000, pubHighAgency),
	}
}

// goldenFixtureURL is a URL builder that produces stable, predictable
// URLs without any token stamping. wa-i1l.6 will layer on top; the
// goldens here represent the pre-token-stamp output.
func goldenFixtureURL(e model.ManifestEntry) string {
	return "https://feed.example.test/" + e.R2Key
}

// goldenPath is where the golden lives. Project convention is
// `<package>/testdata/golden/...`.
var goldenPath = filepath.Join("testdata", "golden", "3essays.xml")

func TestGoldenRSS_3Essays(t *testing.T) {
	got, err := Generate(goldenFixtureChannel(), goldenFixtureEntries(), goldenFixtureURL)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("regenerated golden: %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v\nrun `go test ./internal/feed -run TestGoldenRSS_3Essays -update` to regenerate", err)
	}
	if !bytes.Equal(got, want) {
		i := firstDiffByte(got, want)
		t.Errorf(`golden mismatch at byte %d
  got  bytes: %d
  want bytes: %d
context (got, ±40 around the divergence):
%q
context (want, ±40 around the divergence):
%q
run "go test ./internal/feed -run TestGoldenRSS_3Essays -update" if the change is intentional`,
			i, len(got), len(want),
			byteContext(got, i, 40),
			byteContext(want, i, 40))
	}
}

// firstDiffByte returns the index of the first byte where a and b
// differ, or min(len(a), len(b)) if one is a prefix of the other.
func firstDiffByte(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// byteContext returns up to `radius` bytes on each side of i, rendered
// for display.
func byteContext(s []byte, i, radius int) string {
	lo := i - radius
	if lo < 0 {
		lo = 0
	}
	hi := i + radius
	if hi > len(s) {
		hi = len(s)
	}
	return string(s[lo:hi])
}

// TestXmlValidates — bead row `xml_validates`. Runs xmllint against
// the golden file (the canonical fixture). Skips with a clear message
// if xmllint is not on PATH; the structural assertions in
// feed_table_test.go cover the same ground without the binary.
func TestXmlValidates(t *testing.T) {
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skipf("xmllint not on PATH; structural validation runs in feed_table_test.go: %v", err)
	}
	if _, err := os.Stat(goldenPath); err != nil {
		t.Skipf("golden %s missing; run `go test -run TestGoldenRSS_3Essays -update` first: %v", goldenPath, err)
	}
	cmd := exec.Command("xmllint", "--noout", goldenPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Errorf("xmllint --noout failed on %s: %v\nstderr:\n%s", goldenPath, err, stderr.String())
	}
}
