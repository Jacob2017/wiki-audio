package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// runCost is a thin wrapper that mirrors runBuild from build_test.go
// so the cost subcommand stays test-symmetric with build.
func runCost(t *testing.T, configPath, envPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var so, se bytes.Buffer
	root.SetOut(&so)
	root.SetErr(&se)
	full := append([]string{"--config", configPath, "--env", envPath}, args...)
	root.SetArgs(full)
	err = root.Execute()
	return so.String(), se.String(), err
}

// --- cost (no flag) — manifest path -------------------------------------

func TestCost_ManifestEmptyMessage(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	stdout, _, err := runCost(t, cfgPath, envPath, "cost")
	if err != nil {
		t.Fatalf("cost (no flag) should not error in stub mode: %v", err)
	}
	if !strings.Contains(stdout, "manifest empty") {
		t.Errorf("expected 'manifest empty' message; got %q", stdout)
	}
	if !strings.Contains(stdout, "wiki-audio cost --all") {
		t.Errorf("manifest-empty message should suggest --all; got %q", stdout)
	}
}

// --- cost --all — three-line §3 format pin ------------------------------

func TestCost_AllFormatMatchesSection3(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
		"beta.md":  canonicalEssay("Beta", "Beta body."),
	})
	stdout, _, err := runCost(t, cfgPath, envPath, "cost", "--all")
	if err != nil {
		t.Fatalf("cost --all failed: %v", err)
	}
	// Line 1 contract:
	//   cleaned chars across <n> essays: <total>
	headerRe := regexp.MustCompile(`cleaned chars across \d+ essays: [\d,]+`)
	if !headerRe.MatchString(stdout) {
		t.Errorf("header line missing; want %s; got %q", headerRe, stdout)
	}
	// Line 2 contract — note the literal "flash_v2_5  (" with two spaces.
	flashRe := regexp.MustCompile(`flash_v2_5  \(0\.5 credits/char\): [\d,]+ credits → `)
	if !flashRe.MatchString(stdout) {
		t.Errorf("flash line missing; want %s; got %q", flashRe, stdout)
	}
	// Line 3 contract.
	mvRe := regexp.MustCompile(`multilingual_v2 \(1 credit/char\): [\d,]+ credits → `)
	if !mvRe.MatchString(stdout) {
		t.Errorf("multilingual_v2 line missing; want %s; got %q", mvRe, stdout)
	}
}

// --- cost --all — multilingual credits = 2 × flash credits --------------

func TestCost_AllMultilingualIsTwiceFlash(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	stdout, _, err := runCost(t, cfgPath, envPath, "cost", "--all")
	if err != nil {
		t.Fatal(err)
	}
	flashRe := regexp.MustCompile(`flash_v2_5  \(0\.5 credits/char\): ([\d,]+) credits`)
	mvRe := regexp.MustCompile(`multilingual_v2 \(1 credit/char\): ([\d,]+) credits`)
	flashMatch := flashRe.FindStringSubmatch(stdout)
	mvMatch := mvRe.FindStringSubmatch(stdout)
	if flashMatch == nil || mvMatch == nil {
		t.Fatalf("flash/mv lines missing; got %q", stdout)
	}
	flash := parseCommaInt(t, flashMatch[1])
	mv := parseCommaInt(t, mvMatch[1])
	// flash truncates float (chars × 0.5) downward; mv is exact chars.
	// For odd char totals mv == 2*flash + 1, otherwise mv == 2*flash.
	if mv != 2*flash && mv != 2*flash+1 {
		t.Errorf("multilingual_v2 (%d) should be 2×flash (%d) or 2×flash+1", mv, flash)
	}
}

// --- cost --all — total chars = sum across essays ------------------------

func TestCost_AllTotalReflectsAllEssays(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
		"beta.md":  canonicalEssay("Beta", "Body of beta with extra text."),
		"gamma.md": canonicalEssay("Gamma", "Gamma body content."),
	})
	stdout, _, err := runCost(t, cfgPath, envPath, "cost", "--all")
	if err != nil {
		t.Fatal(err)
	}
	headerRe := regexp.MustCompile(`cleaned chars across (\d+) essays: ([\d,]+)`)
	m := headerRe.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("header line missing; got %q", stdout)
	}
	if m[1] != "3" {
		t.Errorf("essay count = %s; want 3", m[1])
	}
	totalChars := parseCommaInt(t, m[2])
	if totalChars < 100 {
		t.Errorf("total chars suspiciously low: %d", totalChars)
	}
}

// --- cost --all — malformed essays excluded from count + total ----------

func TestCost_AllExcludesMalformedEssays(t *testing.T) {
	tooShort := "# Short\n\n## Metadata\n- Author: x\n\n## Full Document\nShort.\n"
	cfgPath, envPath := buildFixture(t, map[string]string{
		"good.md":  canonicalEssay("Good", "Good body."),
		"short.md": tooShort,
	})
	stdout, _, err := runCost(t, cfgPath, envPath, "cost", "--all")
	if err != nil {
		t.Fatal(err)
	}
	headerRe := regexp.MustCompile(`cleaned chars across (\d+) essays:`)
	m := headerRe.FindStringSubmatch(stdout)
	if m == nil || m[1] != "1" {
		t.Errorf("malformed essay should be excluded from count; got %q", stdout)
	}
}

// --- tierMessage boundaries -----------------------------------------------

func TestTierMessage_Boundaries(t *testing.T) {
	cases := []struct {
		credits int
		want    string
	}{
		{0, "fits Pro $99/mo"},
		{500_000, "fits Pro $99/mo"},
		{500_001, "Scale $330/mo (1 month) or Pro $99 × 2"},
		{880_344, "Scale $330/mo (1 month) or Pro $99 × 2"}, // §3 sample
		{1_000_000, "Scale $330/mo (1 month) or Pro $99 × 2"},
		{1_000_001, "Scale $330/mo (1 month) or Pro $99 × 3"},
		{2_000_000, "Scale $330/mo (1 month) or Pro $99 × 4"},
		{2_000_001, "Scale $330/mo × 2 months or Enterprise quote"},
		{5_000_000, "Scale $330/mo × 3 months or Enterprise quote"},
	}
	for _, c := range cases {
		got := tierMessage(c.credits)
		if got != c.want {
			t.Errorf("tierMessage(%d) = %q; want %q", c.credits, got, c.want)
		}
	}
}

// --- §3 sample exact reproduction ----------------------------------------
// The comment on wa-kyn.16 pins this exact output for 53 essays at
// 880,344 chars. Verifies the format string assembles correctly.

func TestPrintCostReport_Section3SampleExact(t *testing.T) {
	var buf bytes.Buffer
	printCostReport(&buf, 53, 880_344)
	want := "cleaned chars across 53 essays: 880,344\n" +
		"flash_v2_5  (0.5 credits/char): 440,172 credits → fits Pro $99/mo\n" +
		"multilingual_v2 (1 credit/char): 880,344 credits → Scale $330/mo (1 month) or Pro $99 × 2\n"
	if buf.String() != want {
		t.Errorf("§3 sample mismatch:\n got: %q\nwant: %q", buf.String(), want)
	}
}

// --- ceilDiv -------------------------------------------------------------

func TestCeilDiv(t *testing.T) {
	cases := []struct{ n, d, want int }{
		{0, 5, 0},
		{1, 5, 1},
		{4, 5, 1},
		{5, 5, 1},
		{6, 5, 2},
		{10, 5, 2},
		{11, 5, 3},
		{0, 0, 0},
	}
	for _, c := range cases {
		if got := ceilDiv(c.n, c.d); got != c.want {
			t.Errorf("ceilDiv(%d, %d) = %d; want %d", c.n, c.d, got, c.want)
		}
	}
}
