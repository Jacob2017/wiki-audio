package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Tests for wa-kyn.12. Cover: fresh scaffold, idempotent refuse,
// --force overwrite, token shape (43 chars URL-safe), .env mode
// 0o600, top-up of existing .env with empty token line, preservation
// of operator-entered values during top-up.

func newInitOpts(t *testing.T) (initOpts, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	var out bytes.Buffer
	return initOpts{
		configPath: filepath.Join(dir, "config.toml"),
		envPath:    filepath.Join(dir, ".env"),
		out:        &out,
	}, &out
}

// --- token shape ---

var tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestGenerateAccessTokenShape(t *testing.T) {
	tok, err := generateAccessToken(nil)
	if err != nil {
		t.Fatalf("generateAccessToken: %v", err)
	}
	if !tokenRE.MatchString(tok) {
		t.Errorf("token %q does not match §9.1 format [A-Za-z0-9_-]{43}", tok)
	}
}

func TestGenerateAccessTokenDeterministicWithFixedRand(t *testing.T) {
	// 32 zero bytes → known base64 RawURLEncoding output: 43 'A's.
	zeros := bytes.NewReader(make([]byte, 32))
	tok, err := generateAccessToken(zeros)
	if err != nil {
		t.Fatalf("generateAccessToken: %v", err)
	}
	if tok != strings.Repeat("A", 43) {
		t.Errorf("zero-bytes token = %q; want 43 'A's (RawURLEncoding of 32 zeros)", tok)
	}
}

func TestGenerateAccessTokenShortReadFails(t *testing.T) {
	short := bytes.NewReader(make([]byte, 10)) // not enough bytes
	_, err := generateAccessToken(short)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("short rand source should error with ErrUnexpectedEOF; got %v", err)
	}
}

// --- fresh scaffold ---

func TestRunInitFreshScaffoldCreatesBothFiles(t *testing.T) {
	opts, out := newInitOpts(t)

	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	for _, p := range []string{opts.configPath, opts.envPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file should exist after init: %s: %v", p, err)
		}
	}

	gotOut := out.String()
	for _, want := range []string{
		"created " + opts.configPath + " (with placeholders)",
		"chmod 600",
		"next: edit config.toml and populate .env, then run `wiki-audio doctor`",
		"WIKI_AUDIO_ACCESS_TOKEN generated:",
		"wrangler secret put ACCESS_TOKEN",
	} {
		if !strings.Contains(gotOut, want) {
			t.Errorf("output missing %q:\n%s", want, gotOut)
		}
	}
}

func TestRunInitEnvFileMode0o600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mode bits — see PLAN §6 chmod 600 enforcement")
	}
	opts, _ := newInitOpts(t)
	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	info, err := os.Stat(opts.envPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf(".env mode = %#o, want 0600", got)
	}
}

func TestRunInitConfigContainsPlaceholders(t *testing.T) {
	opts, _ := newInitOpts(t)
	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	body, err := os.ReadFile(opts.configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`[wiki]`, `source_dir = ""`,
		`[tts]`, `voice_id = ""`,
		`[r2]`, `account_id = ""`, `bucket = ""`,
		`[feed]`, `base_url = "https://`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config.toml missing %q in body", want)
		}
	}
}

func TestRunInitEnvContainsTokenAndPlaceholders(t *testing.T) {
	opts, _ := newInitOpts(t)
	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	body, err := os.ReadFile(opts.envPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)

	for _, want := range []string{
		"ELEVENLABS_API_KEY=",
		"R2_ACCESS_KEY_ID=",
		"R2_SECRET_ACCESS_KEY=",
		"WIKI_AUDIO_ACCESS_TOKEN=",
	} {
		if !strings.Contains(s, want) {
			t.Errorf(".env missing key %q:\n%s", want, s)
		}
	}

	// Token line must have a non-empty value matching §9.1 format.
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "WIKI_AUDIO_ACCESS_TOKEN=") {
			val := strings.TrimPrefix(line, "WIKI_AUDIO_ACCESS_TOKEN=")
			if !tokenRE.MatchString(val) {
				t.Errorf("token line value %q does not match expected format", val)
			}
			return
		}
	}
	t.Error("WIKI_AUDIO_ACCESS_TOKEN line not found")
}

// --- idempotency ---

// Re-running init on a populated install must refuse, naming the
// offending file. The §3 sample shows the exact wording. Operators
// who are running init by mistake learn what's there without losing
// data.
func TestRunInitConfigExistsRefuses(t *testing.T) {
	opts, _ := newInitOpts(t)
	mustWriteFile(t, opts.configPath, "existing content")

	err := runInit(opts)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !errors.Is(err, errInitRefuse) {
		t.Errorf("error should wrap errInitRefuse; got %v", err)
	}
	if !strings.Contains(err.Error(), opts.configPath) {
		t.Errorf("error should name the offending file: %v", err)
	}

	body, _ := os.ReadFile(opts.configPath)
	if string(body) != "existing content" {
		t.Errorf("init clobbered existing config without --force: got %q", body)
	}
}

func TestRunInitFilledEnvRefuses(t *testing.T) {
	opts, _ := newInitOpts(t)
	// config doesn't exist (fine), .env exists with token already filled
	mustWriteFile(t, opts.envPath,
		"ELEVENLABS_API_KEY=existing-key\nWIKI_AUDIO_ACCESS_TOKEN=existing-token\n")
	if err := os.Chmod(opts.envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runInit(opts)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if !errors.Is(err, errInitRefuse) {
		t.Errorf("expected errInitRefuse; got %v", err)
	}

	// .env preserved verbatim.
	body, _ := os.ReadFile(opts.envPath)
	if !strings.Contains(string(body), "existing-token") {
		t.Errorf("init clobbered existing token without --force: %s", body)
	}
}

func TestRunInitForceOverwritesBoth(t *testing.T) {
	opts, _ := newInitOpts(t)
	mustWriteFile(t, opts.configPath, "old config")
	mustWriteFile(t, opts.envPath, "ELEVENLABS_API_KEY=old\nWIKI_AUDIO_ACCESS_TOKEN=old-token\n")
	if err := os.Chmod(opts.envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	opts.force = true
	if err := runInit(opts); err != nil {
		t.Fatalf("runInit --force: %v", err)
	}

	cfg, _ := os.ReadFile(opts.configPath)
	if !strings.Contains(string(cfg), "[wiki]") {
		t.Errorf("--force should overwrite config: %s", cfg)
	}

	env, _ := os.ReadFile(opts.envPath)
	if strings.Contains(string(env), "old-token") {
		t.Errorf("--force should regenerate token; old-token still present:\n%s", env)
	}
	if strings.Contains(string(env), "ELEVENLABS_API_KEY=old") {
		t.Errorf("--force overwrites .env entirely (expected); old key gone")
	}
}

// --- top-up ---

// .env exists with operator-entered API keys but token line empty.
// Init must surgically replace just the token line, preserving the
// operator's API keys. This is the scenario where init is run after
// the user manually populated some keys but before the token was
// generated.
func TestRunInitTopsUpEmptyTokenPreservingOtherValues(t *testing.T) {
	opts, _ := newInitOpts(t)
	body := `# operator notes
ELEVENLABS_API_KEY=user-set-this
R2_ACCESS_KEY_ID=user-set-r2-id
R2_SECRET_ACCESS_KEY=user-set-r2-secret
WIKI_AUDIO_ACCESS_TOKEN=
`
	mustWriteFile(t, opts.envPath, body)
	if err := os.Chmod(opts.envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	got, _ := os.ReadFile(opts.envPath)
	s := string(got)

	// Operator's existing values preserved.
	for _, must := range []string{
		"# operator notes",
		"ELEVENLABS_API_KEY=user-set-this",
		"R2_ACCESS_KEY_ID=user-set-r2-id",
		"R2_SECRET_ACCESS_KEY=user-set-r2-secret",
	} {
		if !strings.Contains(s, must) {
			t.Errorf("top-up clobbered operator value: missing %q", must)
		}
	}

	// Token line is now populated.
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "WIKI_AUDIO_ACCESS_TOKEN=") {
			val := strings.TrimPrefix(line, "WIKI_AUDIO_ACCESS_TOKEN=")
			if !tokenRE.MatchString(val) {
				t.Errorf("token after top-up = %q; want §9.1 format", val)
			}
			return
		}
	}
	t.Error("token line not found after top-up")
}

// .env exists but has NO WIKI_AUDIO_ACCESS_TOKEN line at all (e.g.
// hand-crafted by an operator who forgot the key). Init should
// append the line rather than refuse.
func TestRunInitTopsUpAppendsMissingTokenLine(t *testing.T) {
	opts, _ := newInitOpts(t)
	body := `ELEVENLABS_API_KEY=set
R2_ACCESS_KEY_ID=set
R2_SECRET_ACCESS_KEY=set
`
	mustWriteFile(t, opts.envPath, body)
	if err := os.Chmod(opts.envPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runInit(opts); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	got, _ := os.ReadFile(opts.envPath)
	if !strings.Contains(string(got), "WIKI_AUDIO_ACCESS_TOKEN=") {
		t.Errorf("expected token line to be appended:\n%s", got)
	}
	if !strings.Contains(string(got), "ELEVENLABS_API_KEY=set") {
		t.Errorf("operator-entered API key clobbered:\n%s", got)
	}
}

// --- helpers — pure-function unit tests for the .env parsing ---

func TestEnvHasFilledToken(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty content", "", false},
		{"only comments", "# nothing here", false},
		{"key absent", "ELEVENLABS_API_KEY=x", false},
		{"key empty", "WIKI_AUDIO_ACCESS_TOKEN=", false},
		{"key whitespace-only value treated as empty", "WIKI_AUDIO_ACCESS_TOKEN=   ", false},
		{"key filled with surrounding whitespace", "WIKI_AUDIO_ACCESS_TOKEN= abc", true},
		{"key filled", "WIKI_AUDIO_ACCESS_TOKEN=abc", true},
		{"key in comment ignored", "# WIKI_AUDIO_ACCESS_TOKEN=fake", false},
		{"second occurrence wins", "WIKI_AUDIO_ACCESS_TOKEN=\nWIKI_AUDIO_ACCESS_TOKEN=real", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := envHasFilledToken(c.content); got != c.want {
				t.Errorf("envHasFilledToken(%q) = %v, want %v", c.content, got, c.want)
			}
		})
	}
}

func TestTopUpTokenLineReplacesExisting(t *testing.T) {
	in := "FOO=bar\nWIKI_AUDIO_ACCESS_TOKEN=\nBAZ=qux\n"
	out := topUpTokenLine(in, "NEWTOKEN")
	if !strings.Contains(out, "WIKI_AUDIO_ACCESS_TOKEN=NEWTOKEN") {
		t.Errorf("token not replaced: %s", out)
	}
	if !strings.Contains(out, "FOO=bar") || !strings.Contains(out, "BAZ=qux") {
		t.Errorf("other lines lost: %s", out)
	}
	// Old empty token line should NOT remain.
	if strings.Contains(out, "WIKI_AUDIO_ACCESS_TOKEN=\n") {
		t.Errorf("empty token line not replaced: %s", out)
	}
}

func TestTopUpTokenLineAppendsWhenMissing(t *testing.T) {
	in := "FOO=bar\n"
	out := topUpTokenLine(in, "T")
	if !strings.HasSuffix(out, "WIKI_AUDIO_ACCESS_TOKEN=T\n") {
		t.Errorf("expected append at end: %s", out)
	}
	if !strings.Contains(out, "FOO=bar") {
		t.Errorf("preserved line lost: %s", out)
	}
}

func TestTopUpTokenLineAppendsWhenInputHasNoTrailingNewline(t *testing.T) {
	in := "FOO=bar"
	out := topUpTokenLine(in, "T")
	if !strings.HasSuffix(out, "WIKI_AUDIO_ACCESS_TOKEN=T\n") {
		t.Errorf("expected append at end with separator: %s", out)
	}
}

// TestConfigTemplateMatchesRepoExample is the wa-76r.9 drift guard:
// the embedded `configTemplate` const (used by `wiki-audio init` to
// scaffold ~/.wiki-audio/config.toml) MUST stay in lock-step with
// the human-readable `config.example.toml` checked in at the repo
// root. If you edit one, edit the other in the same PR.
//
// Test runs from the package dir (internal/cli/), so the example
// file is two levels up.
func TestConfigTemplateMatchesRepoExample(t *testing.T) {
	const examplePath = "../../config.example.toml"
	bytes, err := os.ReadFile(examplePath)
	if err != nil {
		t.Skipf("config.example.toml not found at %s (running from outside the repo?): %v", examplePath, err)
	}
	if string(bytes) != configTemplate {
		t.Errorf("config.example.toml drifts from configTemplate; sync one to the other.\n" +
			"  init.go's `configTemplate` const seeds new installs.\n" +
			"  config.example.toml is the human-readable reference at the repo root.\n" +
			"They MUST match byte-for-byte (wa-76r.9).")
	}
}
