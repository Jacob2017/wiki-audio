package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fixture builds a temp source_dir + config.toml + .env suitable for
// invoking `wiki-audio build --dry-run`. Returns paths the test feeds
// into the cobra command.
func buildFixture(t *testing.T, essays map[string]string) (configPath, envPath string) {
	t.Helper()
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "source")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range essays {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	configPath = filepath.Join(dir, "config.toml")
	configBody := fmt.Sprintf(`[wiki]
source_dir = %q

[tts]
voice_id = "test-voice"

[r2]
account_id = "test-account"
bucket = "test-bucket"

[feed]
base_url = "https://example.com"
`, srcDir)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	envPath = filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("DUMMY=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, envPath
}

// canonicalEssay assembles a synthetic Readwise-export essay. Caller
// supplies title and a prose paragraph; padding is added so the body
// clears model.MinBodyChars.
func canonicalEssay(title, prose string) string {
	const padding = "Filler line filler line filler line filler line filler line. " +
		"Filler line filler line filler line filler line filler line. " +
		"Filler line filler line filler line filler line filler line. " +
		"Filler line filler line filler line filler line filler line. "
	return "# " + title + "\n\n" +
		"## Metadata\n" +
		"- Author: Test\n" +
		"- URL: https://example.com\n\n" +
		"## Full Document\n" +
		prose + "\n\n" + padding + "\n"
}

func runBuild(t *testing.T, configPath, envPath string, args ...string) (stdout, stderr string, err error) {
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

// --- Per-essay output line ------------------------------------------------

func TestBuildDryRun_PerEssayLineFormat(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"essay-one.md": canonicalEssay("Essay One", "First paragraph of essay one."),
	})
	stdout, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	if !strings.Contains(stdout, "essay-one: chars=") {
		t.Errorf("expected per-essay line for essay-one; got %q", stdout)
	}
	if !strings.Contains(stdout, "chunks=") {
		t.Errorf("expected chunks= field; got %q", stdout)
	}
}

// --- Summary lines pinned to §3 sample format -----------------------------

func TestBuildDryRun_SummaryFormatMatchesSection3(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"essay-one.md": canonicalEssay("Essay One", "Body of essay one."),
	})
	stdout, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	// Line 1 contract:
	//   estimate: <chars> chars × <rate> credits/char (<model>) = <credits> credits
	creditsLine := regexp.MustCompile(`estimate: [\d,]+ chars × 0\.5 credits/char \(\S+\) = [\d,]+ credits`)
	if !creditsLine.MatchString(stdout) {
		t.Errorf("credits line format mismatch; want %s; got %q", creditsLine, stdout)
	}
	// Line 2 contract:
	//   estimate: ~$<n> on Pro tier overage; <fit-message>
	dollarsLine := regexp.MustCompile(`estimate: ~\$\d+ on Pro tier overage; (fits within Pro monthly quota|exceeds Pro; Scale tier required for one-shot run|requires multi-month split or Enterprise quote)`)
	if !dollarsLine.MatchString(stdout) {
		t.Errorf("dollars line format mismatch; want %s; got %q", dollarsLine, stdout)
	}
}

// --- --slug filter narrows to a single essay -----------------------------

func TestBuildDryRun_SlugFilterNarrowsToOne(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
		"beta.md":  canonicalEssay("Beta", "Beta body."),
		"gamma.md": canonicalEssay("Gamma", "Gamma body."),
	})
	stdout, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run", "--slug", "beta")
	if err != nil {
		t.Fatalf("dry-run --slug failed: %v", err)
	}
	if !strings.Contains(stdout, "beta: chars=") {
		t.Errorf("beta line missing; got %q", stdout)
	}
	if strings.Contains(stdout, "alpha: chars=") || strings.Contains(stdout, "gamma: chars=") {
		t.Errorf("alpha/gamma should be filtered out; got %q", stdout)
	}
}

func TestBuildDryRun_SlugMissesReturnsError(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	_, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run", "--slug", "nonexistent")
	if err == nil {
		t.Fatal("expected error when --slug doesn't match any essay")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should name the missing slug; got %q", err.Error())
	}
}

// --- Multiple essays sum to a coherent total -----------------------------

func TestBuildDryRun_TotalCharsSumOfPerEssay(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Body of alpha."),
		"beta.md":  canonicalEssay("Beta", "Body of beta with a bit more text to differ from alpha."),
	})
	stdout, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}
	perEssayRe := regexp.MustCompile(`(?m)^\w+: chars=([\d,]+) chunks=`)
	matches := perEssayRe.FindAllStringSubmatch(stdout, -1)
	if len(matches) != 2 {
		t.Fatalf("expected 2 per-essay lines; got %d in %q", len(matches), stdout)
	}
	var perEssaySum int
	for _, m := range matches {
		n := parseCommaInt(t, m[1])
		perEssaySum += n
	}
	totalRe := regexp.MustCompile(`estimate: ([\d,]+) chars`)
	totalMatch := totalRe.FindStringSubmatch(stdout)
	if totalMatch == nil {
		t.Fatalf("summary line not found in %q", stdout)
	}
	totalReported := parseCommaInt(t, totalMatch[1])
	if totalReported != perEssaySum {
		t.Errorf("summary total %d != sum of per-essay %d", totalReported, perEssaySum)
	}
}

// --- Malformed essay reported, doesn't crash, doesn't count toward total --

func TestBuildDryRun_MalformedEssayReportedSeparately(t *testing.T) {
	tooShort := "# Short\n\n## Metadata\n- Author: x\n\n## Full Document\nShort.\n"
	cfgPath, envPath := buildFixture(t, map[string]string{
		"good.md":  canonicalEssay("Good", "Good body."),
		"short.md": tooShort,
	})
	stdout, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run")
	if err != nil {
		t.Fatalf("dry-run should not error on malformed essay: %v", err)
	}
	if !strings.Contains(stdout, "short: SKIPPED (malformed:") {
		t.Errorf("expected SKIPPED line for short essay; got %q", stdout)
	}
	if !strings.Contains(stdout, "good: chars=") {
		t.Errorf("good essay line missing; got %q", stdout)
	}
}

// --- Without --dry-run, build returns 'not yet implemented' --------------

// As of wa-4cw.5, plain `build` (no flags) runs the full §5 pipeline.
// The fixture's stub .env intentionally omits the four required
// secrets, so plain build surfaces a clear missing-env error from
// LoadEnv. This is the user-facing error path: run `wiki-audio doctor`
// to diagnose.
func TestBuildPlainSurfacesMissingEnvError(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	_, _, err := runBuild(t, cfgPath, envPath, "build")
	if err == nil {
		t.Fatal("plain `build` should error when required env vars are missing")
	}
	if !strings.Contains(err.Error(), "ELEVENLABS_API_KEY") {
		t.Errorf("error should name the missing env var; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "wiki-audio doctor") {
		t.Errorf("error should hint `wiki-audio doctor`; got %q", err.Error())
	}
}

// --- Plan-fit boundary check (the message string switches at thresholds) -

func TestPlanFitMessage_Boundaries(t *testing.T) {
	cases := []struct {
		credits int
		want    string
	}{
		{0, "fits within Pro monthly quota"},
		{500_000, "fits within Pro monthly quota"},
		{500_001, "exceeds Pro; Scale tier required for one-shot run"},
		{2_000_000, "exceeds Pro; Scale tier required for one-shot run"},
		{2_000_001, "requires multi-month split or Enterprise quote"},
	}
	for _, c := range cases {
		got := planFitMessage(c.credits)
		if got != c.want {
			t.Errorf("planFitMessage(%d) = %q; want %q", c.credits, got, c.want)
		}
	}
}

// --- Helpers --------------------------------------------------------------

func TestFormatThousands(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1_000, "1,000"},
		{12_345, "12,345"},
		{880_344, "880,344"},
		{1_000_000, "1,000,000"},
	}
	for _, c := range cases {
		if got := formatThousands(c.n); got != c.want {
			t.Errorf("formatThousands(%d) = %q; want %q", c.n, got, c.want)
		}
	}
}

func TestFormatRate(t *testing.T) {
	cases := []struct {
		r    float64
		want string
	}{
		{0.5, "0.5"},
		{1.0, "1.0"},
		{0.25, "0.25"},
	}
	for _, c := range cases {
		if got := formatRate(c.r); got != c.want {
			t.Errorf("formatRate(%g) = %q; want %q", c.r, got, c.want)
		}
	}
}

func TestSlugFromPath_BasicFormatting(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"How to Do Great Work.md", "how-to-do-great-work"},
		{"high-agency.md", "high-agency"},
		{"/abs/path/Some Essay.md", "some-essay"},
		{"essay—with—em-dash.md", "essay-with-em-dash"},
	}
	for _, c := range cases {
		got := slugFromPath(c.path)
		if got != c.want {
			t.Errorf("slugFromPath(%q) = %q; want %q", c.path, got, c.want)
		}
	}
}

// --- Hermetic invariants: dry-run does not write any extra files --------

func TestBuildDryRun_NoFilesCreated(t *testing.T) {
	cfgPath, envPath := buildFixture(t, map[string]string{
		"alpha.md": canonicalEssay("Alpha", "Alpha body."),
	})
	srcDir := filepath.Dir(cfgPath)
	beforeFiles := snapshotFiles(t, srcDir)

	if _, _, err := runBuild(t, cfgPath, envPath, "build", "--dry-run"); err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}

	afterFiles := snapshotFiles(t, srcDir)
	if len(afterFiles) != len(beforeFiles) {
		t.Errorf("dry-run created or removed files: before=%v after=%v", beforeFiles, afterFiles)
	}
	for k, v := range beforeFiles {
		if afterFiles[k] != v {
			t.Errorf("dry-run mutated %s (size %d → %d)", k, v, afterFiles[k])
		}
	}
}

func snapshotFiles(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = info.Size()
		return nil
	})
	return out
}

func parseCommaInt(t *testing.T, s string) int {
	t.Helper()
	clean := strings.ReplaceAll(s, ",", "")
	var n int
	if _, err := fmt.Sscanf(clean, "%d", &n); err != nil {
		t.Fatalf("parseCommaInt(%q): %v", s, err)
	}
	return n
}
