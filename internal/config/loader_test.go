package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Smoke tests for wa-kyn.2. The full §2/§6 validation matrix lives
// in wa-kyn.21; here we pin only what wa-kyn.2 itself owns:
// happy-path decode, defaults are applied, each required field's
// missing-value path returns an error pointing at its TOML key, and
// unknown keys produce a forward-compat warning rather than failure.

func writeTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// validTOML returns a minimal complete config TOML pointing at
// sourceDir for [wiki].source_dir.
func validTOML(sourceDir string) string {
	return fmt.Sprintf(`
[wiki]
source_dir = %q

[tts]
voice_id = "G17SuINrv2H9FC6nvetn"
voice_label = "Christopher"

[r2]
account_id = "abc123"
bucket = "wiki-audio"

[feed]
title = "PG"
description = "Paul Graham essays"
author = "Paul Graham"
owner_email = "me@example.com"
base_url = "https://wiki-audio.example.workers.dev"
`, sourceDir)
}

func TestLoadConfigHappyPathAppliesDefaults(t *testing.T) {
	src := t.TempDir() // exists, is a dir
	path := writeTOML(t, validTOML(src))

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Every default-able field with no user value should now equal
	// the model.Default* constant.
	wantDefaults := []struct {
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
	for _, c := range wantDefaults {
		if c.got != c.want {
			t.Errorf("%s: got %v want %v", c.name, c.got, c.want)
		}
	}

	// Explicit TOML values are preserved.
	if cfg.Wiki.SourceDir != src {
		t.Errorf("SourceDir: got %q want %q", cfg.Wiki.SourceDir, src)
	}
	if cfg.TTS.VoiceID != "G17SuINrv2H9FC6nvetn" {
		t.Errorf("VoiceID: got %q", cfg.TTS.VoiceID)
	}
}

// Explicit TOML values must NOT be clobbered by applyDefaults. This
// is the wa-kyn.21 row "explicit_override_wins".
func TestLoadConfigExplicitOverrideWins(t *testing.T) {
	src := t.TempDir()
	tomlBody := validTOML(src) + `
chunk_max_chars = 2000
model_id = "eleven_multilingual_v2"
`
	// "chunk_max_chars" + "model_id" land under [tts] only because
	// [tts] is the last table opened in validTOML — verify by
	// rewriting the TOML cleanly.
	tomlBody = fmt.Sprintf(`
[wiki]
source_dir = %q

[tts]
voice_id = "G17SuINrv2H9FC6nvetn"
chunk_max_chars = 2000
model_id = "eleven_multilingual_v2"

[r2]
account_id = "abc123"
bucket = "wiki-audio"

[feed]
base_url = "https://example.com"
`, src)
	path := writeTOML(t, tomlBody)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.TTS.ChunkMaxChars != 2000 {
		t.Errorf("ChunkMaxChars: got %d want 2000 (default would be %d)",
			cfg.TTS.ChunkMaxChars, model.DefaultChunkMaxChars)
	}
	if cfg.TTS.ModelID != "eleven_multilingual_v2" {
		t.Errorf("ModelID: got %q want explicit override", cfg.TTS.ModelID)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig("/no/such/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "wiki-audio init") {
		t.Errorf("error should mention init command: %v", err)
	}
}

func TestLoadConfigInvalidTOMLSyntax(t *testing.T) {
	path := writeTOML(t, "this is not valid TOML = = =")
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should mention parse: %v", err)
	}
}

func TestLoadConfigSourceDirRequired(t *testing.T) {
	tomlBody := `
[wiki]

[tts]
voice_id = "v"

[r2]
account_id = "a"
bucket = "b"

[feed]
base_url = "https://example.com"
`
	path := writeTOML(t, tomlBody)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing source_dir")
	}
	if !strings.Contains(err.Error(), "[wiki].source_dir") {
		t.Errorf("error should mention [wiki].source_dir: %v", err)
	}
}

func TestLoadConfigSourceDirMustExist(t *testing.T) {
	path := writeTOML(t, validTOML("/no/such/dir"))
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for non-existent source_dir")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found': %v", err)
	}
}

func TestLoadConfigSourceDirMustBeDirectory(t *testing.T) {
	dir := t.TempDir()
	regularFile := filepath.Join(dir, "regular.txt")
	if err := os.WriteFile(regularFile, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeTOML(t, validTOML(regularFile))
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error when source_dir is a regular file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error should say 'not a directory': %v", err)
	}
}

func TestLoadConfigVoiceIDRequired(t *testing.T) {
	src := t.TempDir()
	tomlBody := fmt.Sprintf(`
[wiki]
source_dir = %q

[tts]

[r2]
account_id = "a"
bucket = "b"

[feed]
base_url = "https://example.com"
`, src)
	path := writeTOML(t, tomlBody)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "[tts].voice_id") {
		t.Fatalf("expected [tts].voice_id error; got: %v", err)
	}
}

func TestLoadConfigR2FieldsRequired(t *testing.T) {
	src := t.TempDir()
	cases := []struct {
		name      string
		r2Body    string
		wantField string
	}{
		{"missing account_id", `bucket = "b"`, "[r2].account_id"},
		{"missing bucket", `account_id = "a"`, "[r2].bucket"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tomlBody := fmt.Sprintf(`
[wiki]
source_dir = %q

[tts]
voice_id = "v"

[r2]
%s

[feed]
base_url = "https://example.com"
`, src, c.r2Body)
			path := writeTOML(t, tomlBody)
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), c.wantField) {
				t.Errorf("expected %s error; got: %v", c.wantField, err)
			}
		})
	}
}

func TestLoadConfigBaseURLValidation(t *testing.T) {
	src := t.TempDir()
	cases := []struct {
		name    string
		baseURL string
		wantErr string
	}{
		{"missing", "", "is required"},
		{"no scheme", "example.com/path", "missing scheme or host"},
		{"scheme only", "https://", "missing scheme or host"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := strings.Replace(validTOML(src),
				`base_url = "https://wiki-audio.example.workers.dev"`,
				fmt.Sprintf(`base_url = %q`, c.baseURL), 1)
			path := writeTOML(t, body)
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("baseURL=%q: got %v, want error containing %q", c.baseURL, err, c.wantErr)
			}
		})
	}
}

// Forward-compat: a TOML file with keys this binary doesn't know
// about should NOT be rejected. Future schema bumps land in
// production before older binaries are upgraded; refusing to load is
// a footgun.
func TestLoadConfigUnknownKeysWarnNotFail(t *testing.T) {
	src := t.TempDir()
	body := validTOML(src) + `
[future]
some_new_key = "value"
nested = { x = 1 }
`
	path := writeTOML(t, body)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Errorf("unknown TOML keys must not fail: %v", err)
	}
	if cfg == nil {
		t.Fatal("config nil after success")
	}
}
