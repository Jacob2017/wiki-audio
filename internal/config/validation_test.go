package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// wa-kyn.21 closes out the §6 / §9 test matrix on top of the smoke
// suites already in loader_test.go (wa-kyn.2) and env_test.go
// (wa-kyn.3). The two existing files cover most rows from the bead's
// table; this file adds the rows they didn't:
//
//   TOML loader rows added here:
//     - http_base_url_warns_or_errors  → policy pinned (current: accept)
//     - unknown_keys_warn              → captured slog WARN payload
//     - invalid_toml_syntax            → strengthened to assert line:col
//
//   .env loader rows added here:
//     - optional_r2_token_absent       → explicit test
//     - values_not_logged              → real slog capture, not placeholder
//
//   Bonus invariants beyond the bead:
//     - composed LoadConfig + LoadEnv  → integration smoke
//     - unicode in secret values       → round-trips through .env
//     - secrets in shell env stay out of slog when LoadEnv runs

// captureSlog redirects slog.Default() to an in-memory buffer at the
// given level. Returns the buffer; cleanup is registered with t.
func captureSlog(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: level,
	})))
	return &buf
}

// --- TOML loader: gap rows ---

// http_base_url_warns_or_errors — bead says "currently: warn; doc the
// choice". As shipped, the loader accepts both http:// and https://
// silently and validate() only checks scheme + host are present.
// This test pins the current behavior so a future change that adds
// the warn (or upgrades to a hard error) breaks the test loudly,
// forcing a deliberate decision.
//
// Follow-up surfaced in the wa-kyn.21 close-out: add slog.Warn for
// non-https BaseURL when implementing wa-kyn.13 doctor (which is the
// natural site for this kind of policy nudge).
func TestLoadConfigHTTPBaseURLAcceptedAsShipped(t *testing.T) {
	src := t.TempDir()
	body := strings.Replace(validTOML(src),
		"https://wiki-audio.jabyrne.workers.dev",
		"http://wiki-audio.jabyrne.workers.dev", 1)
	path := writeTOML(t, body)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("http BaseURL was rejected (current behavior is to accept): %v", err)
	}
	if cfg.Feed.BaseURL != "http://wiki-audio.jabyrne.workers.dev" {
		t.Errorf("BaseURL not preserved: %q", cfg.Feed.BaseURL)
	}
}

// unknown_keys_warn — strengthened to verify the slog.Warn ACTUALLY
// fires with the expected keys list, not just that the load
// succeeds. A future refactor that downgrades the warn to a debug
// (or drops it entirely) would silently break the operator's only
// signal that they have a typo in their TOML.
func TestLoadConfigUnknownKeysFireSlogWarn(t *testing.T) {
	buf := captureSlog(t, slog.LevelWarn)

	src := t.TempDir()
	body := validTOML(src) + `
[future]
totally_made_up_key = "value"
`
	path := writeTOML(t, body)
	if _, err := LoadConfig(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN-level log; got: %s", out)
	}
	if !strings.Contains(out, "unknown TOML keys") {
		t.Errorf("expected message about unknown keys; got: %s", out)
	}
	if !strings.Contains(out, "future") {
		t.Errorf("expected the unknown key name in the log; got: %s", out)
	}
}

// invalid_toml_syntax — strengthen the existing test by asserting
// the error includes a position marker. BurntSushi/toml errors look
// like "(line, col): ..." and surfacing that to the user is the
// whole point of the bead row.
func TestLoadConfigInvalidTOMLIncludesLineColumn(t *testing.T) {
	// Two valid lines then a malformed one — gives the parser
	// something to point at on line 3.
	body := `[wiki]
source_dir = "/tmp"
this is not = = valid
`
	path := writeTOML(t, body)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	msg := err.Error()
	if !strings.ContainsAny(msg, "0123456789") {
		t.Errorf("error must include a line/column number for the user to locate the syntax error; got: %v", err)
	}
}

// --- .env loader: gap rows ---

// optional_r2_token_absent — R2_TOKEN is informational; absence must
// not fail the load. Existing happy-path test includes it; this test
// pins the negative case.
func TestLoadEnvOptionalR2TokenAbsent(t *testing.T) {
	snapshotEnv(t, append(RequiredEnvVars, "R2_TOKEN")...)

	body := `ELEVENLABS_API_KEY=ok
R2_ACCESS_KEY_ID=ok
R2_SECRET_ACCESS_KEY=ok
WIKI_AUDIO_ACCESS_TOKEN=ok
`
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadEnv(path); err != nil {
		t.Fatalf("absence of R2_TOKEN must not fail; got: %v", err)
	}
	if v := os.Getenv("R2_TOKEN"); v != "" {
		t.Errorf("R2_TOKEN unexpectedly populated: %q", v)
	}
}

// values_not_logged — the real test the wa-kyn.3 placeholder
// flagged. Capture slog output across LoadEnv and assert no secret
// VALUE strings (the high-entropy garbage on the right of the `=`)
// appear in the output bytes.
func TestLoadEnvDoesNotLeakSecretsToSlog(t *testing.T) {
	snapshotEnv(t, append(RequiredEnvVars, "R2_TOKEN")...)

	// Use highly distinctive secret values so a substring search
	// for them is unambiguous if they leak.
	const (
		elKey      = "el-secret-CANARY-elevenlabs-99887"
		r2Key      = "r2-id-CANARY-keystr-12345"
		r2Sec      = "r2-secret-CANARY-superprivate-77777"
		feedToken  = "wat-CANARY-token-aaaaa-bbbbb-ccccc-ddddd"
		r2OptToken = "r2-token-CANARY-optional-xyzzy"
	)
	body := fmt.Sprintf(`ELEVENLABS_API_KEY=%s
R2_ACCESS_KEY_ID=%s
R2_SECRET_ACCESS_KEY=%s
WIKI_AUDIO_ACCESS_TOKEN=%s
R2_TOKEN=%s
`, elKey, r2Key, r2Sec, feedToken, r2OptToken)

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	// Capture at Debug — the loader's success path emits
	// slog.Debug. If we captured at Warn-only the test would be
	// trivially green even if values were logged at Debug.
	buf := captureSlog(t, slog.LevelDebug)

	if err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}

	out := buf.String()
	for _, secret := range []string{elKey, r2Key, r2Sec, feedToken, r2OptToken} {
		if strings.Contains(out, secret) {
			t.Errorf("secret value leaked into slog output: %s\n--- log output ---\n%s",
				secret, out)
		}
	}

	// Sanity: the loader DID log SOMETHING — otherwise the test is
	// vacuous. Should mention the path or the var count.
	if !strings.Contains(out, "loaded .env") {
		t.Errorf("expected loader to log success message; got: %s", out)
	}
}

// --- Bonus: invariants beyond the bead ---

// Composed load — the eventual `Load(configPath, envPath)` facade
// will wire LoadConfig and LoadEnv together. Sanity-check that they
// don't accidentally interfere when called back-to-back. Catches
// bugs like "LoadEnv mutates os.Environ in a way that LoadConfig
// re-reads" and similar.
func TestComposedLoadConfigAndEnv(t *testing.T) {
	snapshotEnv(t, append(RequiredEnvVars, "R2_TOKEN")...)

	src := t.TempDir()

	cfgPath := writeTOML(t, validTOML(src))
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte(validEnvBody()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := LoadEnv(envPath); err != nil {
		t.Fatalf("LoadEnv after LoadConfig: %v", err)
	}

	// LoadConfig didn't accidentally consume an env var that
	// LoadEnv now needs to set:
	if got := os.Getenv("ELEVENLABS_API_KEY"); got != "el-test-key" {
		t.Errorf("ELEVENLABS_API_KEY = %q; LoadEnv didn't populate as expected", got)
	}
	// LoadEnv didn't blow away the loaded Config:
	if cfg.TTS.VoiceID != "G17SuINrv2H9FC6nvetn" {
		t.Errorf("VoiceID lost: %q", cfg.TTS.VoiceID)
	}
}

// Unicode round-trip — secrets occasionally contain non-ASCII
// (passphrases, OAuth tokens with embedded URL-decoded characters).
// godotenv handles UTF-8 transparently; this test pins that the
// loader doesn't accidentally re-encode or strip on its way through.
func TestLoadEnvPreservesUnicodeInSecretValues(t *testing.T) {
	snapshotEnv(t, RequiredEnvVars...)

	body := `ELEVENLABS_API_KEY=key-café-résumé-✓
R2_ACCESS_KEY_ID=ok
R2_SECRET_ACCESS_KEY=ok
WIKI_AUDIO_ACCESS_TOKEN=ok
`
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadEnv(path); err != nil {
		t.Fatalf("LoadEnv: %v", err)
	}
	const want = "key-café-résumé-✓"
	if got := os.Getenv("ELEVENLABS_API_KEY"); got != want {
		t.Errorf("unicode value lost: got %q want %q", got, want)
	}
}

// applyDefaults must populate every default-able field on a
// zero-value Config. Existing TestLoadConfigHappyPathAppliesDefaults
// covers the same fields via the loader integration; this test
// pins applyDefaults at the unit boundary so a refactor that splits
// the helper or routes it through a different call site can't leave
// a field unset without a unit-level signal.
func TestApplyDefaultsPopulatesEveryDefaultableField(t *testing.T) {
	var cfg model.Config
	applyDefaults(&cfg)

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"TTS.ModelID", cfg.TTS.ModelID, model.DefaultModelID},
		{"TTS.ChunkMaxChars", cfg.TTS.ChunkMaxChars, model.DefaultChunkMaxChars},
		{"TTS.RequestTimeoutS", cfg.TTS.RequestTimeoutS, model.DefaultRequestTimeoutS},
		{"TTS.RetryAttempts", cfg.TTS.RetryAttempts, model.DefaultRetryAttempts},
		{"TTS.RetryBackoffBase", cfg.TTS.RetryBackoffBase, model.DefaultRetryBackoffBase},
		{"TTS.OutputFormat", cfg.TTS.OutputFormat, model.DefaultOutputFormat},
		{"Feed.FeedPath", cfg.Feed.FeedPath, model.DefaultFeedPath},
		{"Feed.Language", cfg.Feed.Language, model.DefaultLanguage},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}
}
