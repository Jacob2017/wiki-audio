package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/Jacob2017/wiki-audio/internal/model"
)

// LoadConfig reads ~/.wiki-audio/config.toml (or whatever path the
// caller passes — root.go threads --config through), decodes it into
// model.Config, fills zero-value fields with the §2 defaults from
// model.Default*, and validates required fields per wa-kyn.2.
//
// Required fields (errors point at the offending TOML key):
//   - [wiki].source_dir         — must exist and be a directory
//   - [tts].voice_id            — non-empty
//   - [r2].account_id, .bucket  — non-empty
//   - [feed].base_url           — parses to a URL with scheme + host
//
// Unknown TOML keys produce a forward-compat warning rather than an
// error; an older binary reading a newer config file shouldn't fail
// because the newer file added a key.
//
// Env-var loading (.env / chmod 600) lives in a separate function
// added by wa-kyn.3. wiki-audio doctor (wa-kyn.13) calls both.
func LoadConfig(path string) (*model.Config, error) {
	var cfg model.Config
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config: file not found at %s — run `wiki-audio init` to scaffold", path)
		}
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		slog.Warn("config: unknown TOML keys ignored (forward-compat)",
			"keys", keys, "path", path)
	}

	applyDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w (in %s)", err, path)
	}

	return &cfg, nil
}

// validate returns an error pointing at the first missing or invalid
// required field. Messages use TOML table syntax (e.g.
// "[wiki].source_dir") so the user can locate the field in their
// config.toml without translation. Validation runs AFTER defaults
// are applied, so a default-able field with no user value passes.
func validate(cfg *model.Config) error {
	if cfg.Wiki.SourceDir == "" {
		return errors.New("[wiki].source_dir is required")
	}
	info, err := os.Stat(cfg.Wiki.SourceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("[wiki].source_dir not found: %s", cfg.Wiki.SourceDir)
		}
		return fmt.Errorf("[wiki].source_dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("[wiki].source_dir is not a directory: %s", cfg.Wiki.SourceDir)
	}

	if cfg.TTS.VoiceID == "" {
		return errors.New("[tts].voice_id is required")
	}

	if cfg.R2.AccountID == "" {
		return errors.New("[r2].account_id is required")
	}
	if cfg.R2.Bucket == "" {
		return errors.New("[r2].bucket is required")
	}

	if cfg.Feed.BaseURL == "" {
		return errors.New("[feed].base_url is required")
	}
	u, err := url.Parse(cfg.Feed.BaseURL)
	if err != nil {
		return fmt.Errorf("[feed].base_url: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("[feed].base_url is not a valid URL (missing scheme or host): %q", cfg.Feed.BaseURL)
	}

	return nil
}
