package model

import "time"

// EssayMeta is parsed from the Readwise-style markdown header of each
// raw file (§2). Author defaults to DefaultAuthor when no author is
// present in the source — applied by the extractor, not by Go struct
// initialization.
type EssayMeta struct {
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	SourcePath      string `json:"source_path"`
	SourceURL       string `json:"source_url,omitempty"`
	PublishDateText string `json:"publish_date_text,omitempty"`
}

// CleanedDocument is the output of the extraction stage — what gets
// sent to TTS (§2 + §5.1).
//
// CharCount is a Unicode rune count (utf8.RuneCountInString of Body),
// not a byte length. ElevenLabs bills per rune; matching that here
// avoids over-estimating cost on essays containing em-dashes or smart
// quotes. Chunkers that report char counts on AudioChunk MUST match
// this convention so wa-kyn.22's sum-equality assertion holds.
//
// Malformed signals that the document failed a §6 sanity check
// (typically Body length below MinBodyChars after extraction). The
// build loop skips Malformed documents and records them in
// ~/.cache/wiki-audio/skipped.txt without failing the run.
type CleanedDocument struct {
	Meta            EssayMeta `json:"meta"`
	Body            string    `json:"body"`
	BodyHash        string    `json:"body_hash"`
	CharCount       int       `json:"char_count"`
	WordCount       int       `json:"word_count"`
	SkippedSegments []string  `json:"skipped_segments,omitempty"`

	Malformed       bool   `json:"malformed,omitempty"`
	MalformedReason string `json:"malformed_reason,omitempty"`
}

// AudioChunk is one TTS request worth of text (§2 + §5.2). Index is
// 0-based; the build progress UI displays Index+1. CharCount is a
// rune count, matching CleanedDocument.CharCount.
type AudioChunk struct {
	Index     int    `json:"index"`
	Text      string `json:"text"`
	CharCount int    `json:"char_count"`
}

// ManifestEntry is one row per essay in pg.manifest.json (§2). Stored
// in R2 keyed by Slug.
//
// PublishedAt is a pointer because the entry exists from the moment
// the audio is generated, but PublishedAt is set only after the file
// is uploaded to R2 in the publish phase.
type ManifestEntry struct {
	Slug            string     `json:"slug"`
	Title           string     `json:"title"`
	BodyHash        string     `json:"body_hash"`
	VoiceID         string     `json:"voice_id"`
	ModelID         string     `json:"model_id"`
	CharCount       int        `json:"char_count"`
	ChunkCount      int        `json:"chunk_count"`
	DurationSeconds float64    `json:"duration_seconds"`
	FileSizeBytes   int64      `json:"file_size_bytes"`
	R2Key           string     `json:"r2_key,omitempty"`
	R2ETag          string     `json:"r2_etag,omitempty"`
	GeneratedAt     time.Time  `json:"generated_at"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
}

// Manifest is the R2-hosted index of every published episode (§2).
// Version starts at ManifestSchemaVersion; older binaries that load a
// Manifest with Version > the value they know refuse to overwrite
// (§6 "Tool version mismatch") — that guard lives in
// internal/manifest/, not here.
type Manifest struct {
	Version       int                      `json:"version"`
	Entries       map[string]ManifestEntry `json:"entries"`
	LastBuildAt   *time.Time               `json:"last_build_at,omitempty"`
	LastPublishAt *time.Time               `json:"last_publish_at,omitempty"`
}

// Config is loaded from ~/.wiki-audio/config.toml (§2). Sub-config
// validation and default-application live in internal/config/.
type Config struct {
	Wiki WikiConfig `toml:"wiki" json:"wiki"`
	TTS  TTSConfig  `toml:"tts"  json:"tts"`
	R2   R2Config   `toml:"r2"   json:"r2"`
	Feed FeedConfig `toml:"feed" json:"feed"`
}

// WikiConfig points the extractor at the source markdown directory.
type WikiConfig struct {
	SourceDir string `toml:"source_dir" json:"source_dir"`
}

// TTSConfig controls the ElevenLabs request shape. ChunkMaxChars,
// ModelID, RequestTimeoutS, RetryAttempts, RetryBackoffBase, and
// OutputFormat carry defaults from the Default* constants in this
// package; the loader applies them when the TOML omits the field.
type TTSConfig struct {
	VoiceID          string  `toml:"voice_id"             json:"voice_id"`
	VoiceLabel       string  `toml:"voice_label"          json:"voice_label"`
	ModelID          string  `toml:"model_id"             json:"model_id"`
	ChunkMaxChars    int     `toml:"chunk_max_chars"      json:"chunk_max_chars"`
	RequestTimeoutS  float64 `toml:"request_timeout_s"    json:"request_timeout_s"`
	RetryAttempts    int     `toml:"retry_attempts"       json:"retry_attempts"`
	RetryBackoffBase float64 `toml:"retry_backoff_base_s" json:"retry_backoff_base_s"`
	OutputFormat     string  `toml:"output_format"        json:"output_format"`
}

// R2Config addresses the Cloudflare R2 bucket. The endpoint URL is
// derived from AccountID at use-site (https://<id>.r2.cloudflarestorage.com).
type R2Config struct {
	AccountID string `toml:"account_id" json:"account_id"`
	Bucket    string `toml:"bucket"     json:"bucket"`
}

// FeedConfig drives RSS generation. BaseURL is the public Worker URL
// (e.g. https://wiki-audio.example.workers.dev) — episode enclosure
// URLs are built as BaseURL/<R2Key>?t=<token>.
type FeedConfig struct {
	Title         string `toml:"title"                     json:"title"`
	Description   string `toml:"description"               json:"description"`
	Author        string `toml:"author"                    json:"author"`
	OwnerEmail    string `toml:"owner_email"               json:"owner_email"`
	BaseURL       string `toml:"base_url"                  json:"base_url"`
	FeedPath      string `toml:"feed_path"                 json:"feed_path"`
	CoverImageURL string `toml:"cover_image_url,omitempty" json:"cover_image_url,omitempty"`
	Language      string `toml:"language"                  json:"language"`
}

// Required env vars loaded from ~/.wiki-audio/.env (or, with the
// --env-local flag, from .env in the current working directory):
//
//	ELEVENLABS_API_KEY
//	R2_ACCESS_KEY_ID
//	R2_SECRET_ACCESS_KEY
//	WIKI_AUDIO_ACCESS_TOKEN  (embedded into feed enclosure URLs)
//
// Secrets never appear on Config — internal/config/ exposes them via
// dedicated getters so a stray json.Marshal of Config can't leak them.

// Spec-pinned defaults from §2. The TOML loader (wa-kyn.2) applies
// each value when the corresponding field is the Go zero value.
const (
	DefaultAuthor           = "Paul Graham"
	DefaultModelID          = "eleven_flash_v2_5"
	DefaultChunkMaxChars    = 4000
	DefaultRequestTimeoutS  = 60.0
	DefaultRetryAttempts    = 3
	DefaultRetryBackoffBase = 2.0
	DefaultOutputFormat     = "mp3_44100_64"
	DefaultFeedPath         = "pg.xml"
	DefaultLanguage         = "en-us"
)

// MinBodyChars is the §6 sanity-check threshold. A CleanedDocument
// with Body shorter than this is marked Malformed.
const MinBodyChars = 200

// ManifestSchemaVersion is the current on-the-wire manifest schema.
// Bumping this is a deliberate, breaking act — older binaries that
// load a manifest with Version > the constant they know refuse to
// overwrite it (§6 "Tool version mismatch").
const ManifestSchemaVersion = 1
