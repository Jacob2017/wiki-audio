package config

import "github.com/Jacob2017/wiki-audio/internal/model"

// applyDefaults fills zero-value Config fields with the §2 spec
// defaults from model.Default*. Called between toml.DecodeFile and
// validate(), so explicit TOML values win — the loader only writes a
// default when the user omitted (or wrote a Go zero value for) the
// field.
//
// Single source of truth lives in internal/model — this function
// knows only how to map fields to constants, not what the values
// are. Bumping a default is a one-line change in model and is
// automatically picked up here.
func applyDefaults(cfg *model.Config) {
	if cfg.TTS.ModelID == "" {
		cfg.TTS.ModelID = model.DefaultModelID
	}
	if cfg.TTS.ChunkMaxChars == 0 {
		cfg.TTS.ChunkMaxChars = model.DefaultChunkMaxChars
	}
	if cfg.TTS.RequestTimeoutS == 0 {
		cfg.TTS.RequestTimeoutS = model.DefaultRequestTimeoutS
	}
	if cfg.TTS.RetryAttempts == 0 {
		cfg.TTS.RetryAttempts = model.DefaultRetryAttempts
	}
	if cfg.TTS.RetryBackoffBase == 0 {
		cfg.TTS.RetryBackoffBase = model.DefaultRetryBackoffBase
	}
	if cfg.TTS.OutputFormat == "" {
		cfg.TTS.OutputFormat = model.DefaultOutputFormat
	}
	if cfg.Feed.FeedPath == "" {
		cfg.Feed.FeedPath = model.DefaultFeedPath
	}
	if cfg.Feed.Language == "" {
		cfg.Feed.Language = model.DefaultLanguage
	}
}
