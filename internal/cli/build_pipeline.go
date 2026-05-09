package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	atomicwrite "github.com/Jacob2017/wiki-audio/internal/atomic"
	"github.com/Jacob2017/wiki-audio/internal/cache"
	"github.com/Jacob2017/wiki-audio/internal/chunk"
	"github.com/Jacob2017/wiki-audio/internal/concat"
	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/id3"
	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/tts"
)

// runBuildFull is the §5 full build path: extract → hash check →
// chunk → TTS → concat → ID3 tag → place under
// ~/.cache/wiki-audio/out/<slug>.mp3 → update manifest snapshot.
//
// Pipeline orchestration is here; per-stage primitives live in their
// own packages (extract, chunk, tts, concat, id3, atomic, cache).
//
// Resumability: the local manifest is written atomically after each
// successful essay. A crash mid-essay re-extracts and re-renders that
// one essay on restart but doesn't redo the prior ones — body_hash
// match in the manifest skips the re-render.
//
// R2 manifest sync is intentionally NOT here. Pane-2's wa-76r.1 +
// wa-i1l.1 land the manifest schema and the minio-go client; once
// those are in, this function's TODO comments mark the call sites.
//
// Concurrency: serial across essays (per the bead — ElevenLabs rate
// limits + concat is inherently sequential per essay; total wall
// clock is hours not days).
func runBuildFull(cmd *cobra.Command, flags *buildFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	configPath, _ := cmd.Flags().GetString("config")
	envPath, _ := cmd.Flags().GetString("env")
	envLocal, _ := cmd.Flags().GetBool("env-local")
	if envLocal {
		envPath = ".env"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := config.LoadEnv(envPath); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	files, err := listMarkdownFiles(cfg.Wiki.SourceDir)
	if err != nil {
		return fmt.Errorf("build: scan %s: %w", cfg.Wiki.SourceDir, err)
	}
	if flags.slug != "" {
		files = filterBySlug(files, flags.slug)
		if len(files) == 0 {
			return fmt.Errorf("build: no essay matched --slug %q under %s",
				flags.slug, cfg.Wiki.SourceDir)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("build: no .md essays found under %s", cfg.Wiki.SourceDir)
	}

	if err := cache.EnsureDirs(); err != nil {
		return fmt.Errorf("build: %w", err)
	}

	// Manifest read: local cache mirror for resumability.
	// TODO: replace with R2 fetch once internal/r2 (wa-i1l.1) +
	// internal/manifest (wa-76r.1) land.
	manifestPath := filepath.Join(cache.Dir(), "manifest.json")
	manifest, err := readLocalManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("build: read manifest %s: %w", manifestPath, err)
	}

	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	if apiKey == "" {
		return errors.New("build: ELEVENLABS_API_KEY is empty (run wiki-audio doctor)")
	}
	client := tts.NewClient(cfg.TTS, apiKey)

	out := cmd.OutOrStdout()
	logger := slog.With("phase", "build", "n_essays", len(files))
	logger.Info("starting full build", "source_dir", cfg.Wiki.SourceDir, "force", flags.force)

	for i, path := range files {
		slug := slugFromPath(path)
		essayLogger := logger.With("essay_slug", slug, "index", i+1)

		entry, err := buildOneEssayFull(ctx, cfg, client, manifest, path, flags.force, essayLogger)
		if err != nil {
			fmt.Fprintf(out, "[%d/%d] %s: ERROR %s\n", i+1, len(files), slug, err)
			essayLogger.Error("build essay failed", "err", err.Error())
			continue
		}
		if entry == nil {
			// Skip-on-hash-match path; nothing to update.
			fmt.Fprintf(out, "[%d/%d] %s: skip (hash matches manifest)\n", i+1, len(files), slug)
			continue
		}

		manifest.Entries[slug] = *entry
		now := time.Now().UTC()
		manifest.LastBuildAt = &now

		if err := writeLocalManifestAtomic(manifestPath, manifest); err != nil {
			essayLogger.Error("manifest snapshot write failed",
				"err", err.Error(), "path", manifestPath)
			return fmt.Errorf("build: persist manifest after %s: %w", slug, err)
		}
		essayLogger.Info("essay built",
			"chars", entry.CharCount,
			"chunks", entry.ChunkCount,
			"file_size_bytes", entry.FileSizeBytes,
			"out", cache.OutPath(slug))

		fmt.Fprintf(out, "[%d/%d] %s: built (chars=%d chunks=%d size=%s)\n",
			i+1, len(files), slug,
			entry.CharCount, entry.ChunkCount,
			formatBytes(entry.FileSizeBytes))
	}

	logger.Info("build complete",
		"manifest_path", manifestPath,
		"manifest_entries", len(manifest.Entries))
	fmt.Fprintf(out, "build complete: %d entries in manifest %s\n",
		len(manifest.Entries), manifestPath)
	return nil
}

// buildOneEssayFull runs the full §5 pipeline for ONE essay. Returns
// (nil, nil) when the manifest hash matches and force is false (skip
// signal — caller logs and continues). Returns (entry, nil) on
// success. Returns (nil, err) on any pipeline failure; the caller
// keeps the run going (per-essay errors are non-fatal).
//
// On concat or tag failure, tmp/<slug>/ is preserved per §6 so an
// operator can rerun ffmpeg by hand. CleanupTmp is only called on
// the success path.
func buildOneEssayFull(
	ctx context.Context,
	cfg *model.Config,
	client *tts.Client,
	manifest *model.Manifest,
	path string,
	force bool,
	logger *slog.Logger,
) (*model.ManifestEntry, error) {
	slug := slugFromPath(path)

	doc, err := extractOnlyDoc(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	if doc.Malformed {
		// Per §6: append slug to skipped.txt; don't fail the run.
		_ = appendSkipped(slug, doc.MalformedReason)
		return nil, fmt.Errorf("malformed (chars=%d): %s", doc.CharCount, doc.MalformedReason)
	}

	// Hash check.
	if existing, ok := manifest.Entries[slug]; ok && !force && existing.BodyHash == doc.BodyHash {
		logger.Info("hash match — skipping render",
			"body_hash", doc.BodyHash[:16]+"...",
			"prev_chunk_count", existing.ChunkCount)
		return nil, nil
	}

	chunks := chunk.Chunk(doc.Body, cfg.TTS.ChunkMaxChars)
	if len(chunks) == 0 {
		return nil, fmt.Errorf("chunk: zero chunks for non-empty body (chars=%d)", doc.CharCount)
	}
	logger.Info("chunked",
		"n_chunks", len(chunks),
		"chunk_max_chars", cfg.TTS.ChunkMaxChars)

	tmpDir := cache.TmpDir(slug)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir tmp: %w", err)
	}

	// TTS each chunk → tmp/<slug>/<idx>.mp3
	chunkPaths := make([]string, len(chunks))
	for i, c := range chunks {
		chunkPath := filepath.Join(tmpDir, fmt.Sprintf("%03d.mp3", i))
		chunkLogger := logger.With("chunk_index", i, "chunk_chars", c.CharCount)
		if err := synthesizeChunkWithRetry(ctx, client, c.Text, chunkPath, cfg.TTS, chunkLogger); err != nil {
			return nil, fmt.Errorf("tts chunk %d: %w", i, err)
		}
		chunkPaths[i] = chunkPath
	}

	// Concat → cache.OutPath(slug). concat.Concat handles
	// single-chunk fast path (bytewise copy via atomic write) and
	// pairwise acrossfade for N>=2.
	outPath := cache.OutPath(slug)
	concatTmp := filepath.Join(tmpDir, "concat")
	if err := concat.Concat(ctx, chunkPaths, outPath, concatTmp, concat.Options{Logger: logger}); err != nil {
		return nil, fmt.Errorf("concat: %w", err)
	}

	// ID3 tag the final MP3.
	tagMeta := id3.TagMeta{
		Title:  doc.Meta.Title,
		Artist: doc.Meta.Author,
		Album:  cfg.Feed.Title,
		Year:   yearForEssay(doc.Meta.PublishDateText),
		Genre:  "Audiobook",
	}
	if err := id3.Tag(ctx, outPath, tagMeta); err != nil {
		return nil, fmt.Errorf("id3: %w", err)
	}

	// Cleanup tmp/ ONLY on full success per §6.
	if err := cache.CleanupTmp(slug); err != nil {
		logger.Warn("tmp cleanup failed (non-fatal)", "err", err.Error())
	}

	info, err := os.Stat(outPath)
	if err != nil {
		return nil, fmt.Errorf("stat out: %w", err)
	}
	now := time.Now().UTC()
	entry := &model.ManifestEntry{
		Slug:          slug,
		Title:         doc.Meta.Title,
		BodyHash:      doc.BodyHash,
		VoiceID:       cfg.TTS.VoiceID,
		ModelID:       cfg.TTS.ModelID,
		CharCount:     doc.CharCount,
		ChunkCount:    len(chunks),
		FileSizeBytes: info.Size(),
		// DurationSeconds: deferred — needs ffprobe or a duration
		// extractor pass. Fillable in wa-4cw.5's polish or in
		// publish (Phase F can lazy-fill before RSS gen).
		GeneratedAt: now,
		// R2Key/R2ETag/PublishedAt set in the publish phase.
	}
	return entry, nil
}

// retryAfterCap bounds how long the build pipeline will sleep on a
// server-supplied Retry-After hint. Values above this fall back to
// the cap. ElevenLabs occasionally returns very long Retry-After on
// hard quota-exceeded; capping protects bulk runs from a single
// chunk wedging the run for minutes. The §5.3 spec just says
// "honor Retry-After if present" without a cap; 60s is the
// orchestrator's wa-3gf guidance for the call site.
//
// Variable (not const) so unit tests can shrink it; production code
// never mutates this.
var retryAfterCap = 60 * time.Second

// computeRetryDelay derives the sleep duration before the next TTS
// retry attempt. Returns (delay, source) where source is one of:
//
//	"retry-after"  — server hint honored (capped at retryAfterCap)
//	"backoff"      — no hint (or zero/negative) → exponential backoff
//	                 with jitter, clamped at 60s
//
// hint > retryAfterCap collapses to retryAfterCap; the caller logs
// the clamp at WARN level so the operator sees the gap. attempt is
// the 1-based retry index (0 is the initial call, never reaches
// here).
func computeRetryDelay(hint time.Duration, attempt int, baseSeconds float64, rng *rand.Rand) (time.Duration, string) {
	if hint > 0 {
		if hint > retryAfterCap {
			return retryAfterCap, "retry-after"
		}
		return hint, "retry-after"
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	seconds := baseSeconds*math.Pow(2, float64(attempt-1)) + rng.Float64()
	if seconds > 60 {
		seconds = 60
	}
	return time.Duration(seconds * float64(time.Second)), "backoff"
}

// synthesizeChunkWithRetry calls client.Synthesize with retry on
// retryable APIErrors. cfg.RetryAttempts is the total attempt
// budget (including the first).
//
// Sleep policy:
//   - APIError carries Retry-After honor it (capped at
//     retryAfterCap to bound bulk-run wall clock). pane-9's
//     parseRetryAfter already filters values outside [0, 300s]
//     and rejects malformed headers, so apiErr.RetryAfter > 0
//     means the server gave us a real, sane hint.
//   - No hint (RetryAfter == 0, or transport-level error)
//     fall back to exponential backoff with jitter, capped at 60s.
//
// pane-9's retry classifier internals (classifyHTTPResponse,
// classifyTransportError) are package-private to internal/tts. We
// consume the public surface — APIError.Retryable + RetryAfter —
// which the client populates per the same policy. When pane-9
// exposes a public retry helper, this loop can be replaced with
// one call (TODO).
func synthesizeChunkWithRetry(
	ctx context.Context,
	client *tts.Client,
	text string,
	outPath string,
	cfgTTS model.TTSConfig,
	logger *slog.Logger,
) error {
	// Cache-skip: if a non-empty chunk file already exists at outPath,
	// it's a write-through hit from a prior partially-failed run
	// (concat/ID3 failure, operator Ctrl-C, ffmpeg timeout — see
	// wa-cfx). Re-synthesizing burns ElevenLabs credits for no
	// reason; trust the on-disk bytes and return success.
	//
	// Size > 0 is the load-bearing check: a zero-byte file means a
	// prior run crashed mid-write (or wrote nothing), and that's
	// indistinguishable from a missing chunk for our purposes — we
	// re-synthesize either way. atomic.WriteAtomic (the producer
	// here) only renames a fully-written tmp onto outPath, so any
	// non-empty file at outPath was completely written by a prior
	// successful Synthesize call.
	if info, err := os.Stat(outPath); err == nil && info.Size() > 0 {
		logger.Info("chunk cache hit, skipping synth",
			"path", outPath, "bytes", info.Size())
		return nil
	}

	attempts := cfgTTS.RetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	base := cfgTTS.RetryBackoffBase
	if base <= 0 {
		base = 2
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	var lastErr error
	var nextSleep time.Duration // 0 → use computed backoff at top of loop

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay, source := computeRetryDelay(nextSleep, attempt, base, rng)
			if nextSleep > retryAfterCap {
				logger.Warn("Retry-After exceeds cap; clamping",
					"hint", nextSleep.String(), "cap", retryAfterCap.String())
			}
			logger.Info("retrying tts chunk",
				"attempt", attempt+1, "of", attempts,
				"delay", delay.String(), "delay_source", source)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			nextSleep = 0
		}

		body, err := client.Synthesize(ctx, text)
		if err == nil {
			// Stream MP3 bytes to outPath atomically.
			writeErr := atomicwrite.WriteAtomic(outPath, func(w io.Writer) error {
				_, copyErr := io.Copy(w, body)
				_ = body.Close()
				return copyErr
			}, 0o644)
			if writeErr != nil {
				return fmt.Errorf("write chunk: %w", writeErr)
			}
			return nil
		}

		lastErr = err
		var apiErr *tts.APIError
		if errors.As(err, &apiErr) {
			if !apiErr.Retryable {
				return fmt.Errorf("fatal tts (status %d): %w", apiErr.StatusCode, err)
			}
			logger.Warn("retryable tts error",
				"status", apiErr.StatusCode,
				"attempt", attempt+1,
				"retry_after", apiErr.RetryAfter.String())
			nextSleep = apiErr.RetryAfter
			continue
		}
		// Transport-level error. Pane-9's classifier marks DNS,
		// timeout, ECONNRESET etc. as retryable; we use a coarser
		// "any non-APIError -> retryable" approximation here. When
		// pane-9 exports a public classifier, swap in.
		logger.Warn("transport tts error (treating as retryable)",
			"err", err.Error(), "attempt", attempt+1)
		nextSleep = 0
	}
	return fmt.Errorf("tts gave up after %d attempts: %w", attempts, lastErr)
}

// readLocalManifest reads ~/.cache/wiki-audio/manifest.json. A
// missing file yields a fresh empty manifest at ManifestSchemaVersion
// with no entries.
//
// §6 schema-mismatch guard: a manifest with Version greater than
// model.ManifestSchemaVersion (i.e. written by a newer binary) is
// refused — the older binary must not overwrite it. The user's path
// forward is to upgrade the binary.
func readLocalManifest(path string) (*model.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &model.Manifest{
				Version: model.ManifestSchemaVersion,
				Entries: map[string]model.ManifestEntry{},
			}, nil
		}
		return nil, err
	}
	var m model.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	if m.Version > model.ManifestSchemaVersion {
		return nil, fmt.Errorf("manifest version %d > known %d — upgrade wiki-audio (§6 schema-mismatch guard)",
			m.Version, model.ManifestSchemaVersion)
	}
	if m.Version == 0 {
		m.Version = model.ManifestSchemaVersion
	}
	if m.Entries == nil {
		m.Entries = map[string]model.ManifestEntry{}
	}
	return &m, nil
}

// writeLocalManifestAtomic persists the manifest JSON via the §6
// atomic write helper. Indented for diff-friendliness; sorted keys
// would be nicer but Go's json package doesn't expose a sort hook.
func writeLocalManifestAtomic(path string, m *model.Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicwrite.WriteFile(path, body, 0o644)
}

// appendSkipped tacks one slug + reason to ~/.cache/wiki-audio/skipped.txt
// per §6. Best-effort; an error here is logged and ignored upstream
// because the build must continue past malformed essays.
func appendSkipped(slug, reason string) error {
	if err := cache.EnsureDirs(); err != nil {
		return err
	}
	f, err := os.OpenFile(cache.SkippedPath(),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\n",
		time.Now().UTC().Format(time.RFC3339),
		slug,
		strings.ReplaceAll(reason, "\n", " "))
	return err
}

// yearForEssay returns the 4-digit year for ID3 TYER. Tries to parse
// "July 2023" / "2023" / "2023-07-15" out of PublishDateText. Returns
// the current year on parse miss — TYER must be non-empty for the
// frame to be written, but doc.Meta.PublishDateText is informational
// today so a fallback is fine.
//
// wa-6la F5 still flags PublishDateText extraction as unwired; until
// pane-2 lands that, this function nearly always falls back to "now".
func yearForEssay(publishDateText string) string {
	if publishDateText != "" {
		if m := yearRe.FindString(publishDateText); m != "" {
			return m
		}
	}
	return strconv.Itoa(time.Now().UTC().Year())
}

var yearRe = regexp.MustCompile(`(19|20)\d{2}`)

// formatBytes renders a size in B / KiB / MiB / GiB for the
// per-essay summary line. Powers-of-two tiers because operators
// of disk-full diagnostics expect "MiB" not marketing-MB.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
