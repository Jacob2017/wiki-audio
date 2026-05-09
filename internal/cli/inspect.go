package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Jacob2017/wiki-audio/internal/chunk"
	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/extract"
	"github.com/Jacob2017/wiki-audio/internal/manifest"
	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// errInspectNoSlug surfaces when --slug is empty. We refuse rather
// than fall through to "inspect everything" because the bead pinned
// inspect to single-essay scope; a future bead can extend.
var errInspectNoSlug = errors.New("inspect: --slug X is required")

type inspectFlags struct {
	slug string
}

func newInspectCmd() *cobra.Command {
	flags := &inspectFlags{}
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "Read-only diagnostics for one essay",
		Long: "inspect runs the extractor + chunker on one essay and " +
			"reports it against the R2 manifest entry: chars, dropped " +
			"segments, chunk count, last build/publish timestamps, the " +
			"token-stamped R2 URL, and runtime duration.\n\n" +
			"Pure read-only — no TTS calls, no R2 writes. Useful for " +
			"\"why isn't this essay regenerating?\" debugging where the " +
			"answer is usually \"the body_hash hasn't changed\".",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd, flags)
		},
	}
	cmd.Flags().StringVar(&flags.slug, "slug", "", "essay slug under [wiki].source_dir to inspect (required)")
	_ = cmd.MarkFlagRequired("slug")
	return cmd
}

// runInspect wires the cobra command into runInspectCore. Flag parse
// → config + env → r2.Client → core. The core is pure-logic so
// inspect_test.go can drive it with an r2.Fake.
func runInspect(cmd *cobra.Command, flags *inspectFlags) error {
	if flags.slug == "" {
		return errInspectNoSlug
	}

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
		return fmt.Errorf("inspect: %w", err)
	}

	// Token may legitimately be empty here — inspect is read-only and
	// reports "not yet uploaded" when the manifest entry lacks an R2
	// key, in which case the URL is unprintable anyway. We pass the
	// trimmed value through; an empty token shows up in the URL line
	// as the literal "set WIKI_AUDIO_ACCESS_TOKEN" message.
	token := strings.TrimSpace(os.Getenv("WIKI_AUDIO_ACCESS_TOKEN"))

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2.AccountID)
	store, err := r2.New(endpoint,
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		cfg.R2.Bucket)
	if err != nil {
		return fmt.Errorf("inspect: r2.New: %w", err)
	}

	return runInspectCore(ctx, cmd.OutOrStdout(), store, cfg, token, flags.slug)
}

// runInspectCore is the pure logic, exposed for testing. Driven by
// inspect_test.go with an r2.Fake to verify each line of the format
// without standing up a live R2.
func runInspectCore(
	ctx context.Context,
	out io.Writer,
	store r2.Storage,
	cfg *model.Config,
	token string,
	slug string,
) error {
	// 1. Locate the source .md by slug.
	files, err := listMarkdownFiles(cfg.Wiki.SourceDir)
	if err != nil {
		return fmt.Errorf("inspect: scan %s: %w", cfg.Wiki.SourceDir, err)
	}
	matches := filterBySlug(files, slug)
	if len(matches) == 0 {
		return fmt.Errorf("inspect: no essay matched --slug %q under %s "+
			"(check the spelling, or list available slugs with `wiki-audio cost --all`)",
			slug, cfg.Wiki.SourceDir)
	}
	path := matches[0]

	// 2. Run the extractor chain (same as build_pipeline). Pure read,
	//    no TTS, no R2 writes.
	doc, err := extractOnlyDoc(path, cfg)
	if err != nil {
		return fmt.Errorf("inspect: extract: %w", err)
	}

	// 3. Re-derive footnote count from the parsed source so the
	//    "footnotes: N" line carries signal independent of whether
	//    the body got woven cleanly. Cheap; same chain extractOnlyDoc
	//    runs internally with the intermediates discarded.
	rawBytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspect: re-read source: %w", err)
	}
	rawCharCount := utf8.RuneCountInString(string(rawBytes))
	parsed, _ := extract.ParseFile(path)
	_, notesPart := extract.SplitNotes(parsed.RawBody)
	footnoteCount := len(extract.ParseFootnotes(notesPart))

	// 4. Chunker.
	chunks := chunk.Chunk(doc.Body, cfg.TTS.ChunkMaxChars)

	// 5. Manifest lookup. Manifest absence is non-fatal; the missing
	//    entry just means "not yet built / published".
	m, mErr := manifest.Load(ctx, store)
	var entry *model.ManifestEntry
	if mErr == nil {
		if e, ok := m.Entries[slug]; ok {
			ec := e
			entry = &ec
		}
	}

	// 6. Format. The spec sample (§3) pins these labels; preserve
	//    the spacing so a future "looks the same as build's output"
	//    diff is grep-able.
	fmt.Fprintf(out, "title:        %s\n", doc.Meta.Title)
	fmt.Fprintf(out, "chars:        %s (cleaned) / %s (raw)\n",
		formatThousands(doc.CharCount), formatThousands(rawCharCount))
	fmt.Fprintf(out, "dropped:      %s\n", formatDropped(doc.SkippedSegments, footnoteCount))
	fmt.Fprintf(out, "chunks:       %s\n", formatChunks(chunks))

	if entry == nil {
		// No manifest entry: the essay has been extracted (we just
		// did) but never built into an MP3. Last-build / last-publish
		// / r2 / duration all read as "not yet ...".
		fmt.Fprintln(out, "last build:   not yet built")
		fmt.Fprintln(out, "last publish: not yet published")
		fmt.Fprintln(out, "r2 url:       not yet uploaded")
		fmt.Fprintln(out, "duration:     —")
		if mErr != nil {
			fmt.Fprintf(out, "(manifest load: %v)\n", mErr)
		}
		return nil
	}

	fmt.Fprintf(out, "last build:   %s\n", formatBuildTime(entry.GeneratedAt))
	fmt.Fprintf(out, "last publish: %s\n", formatPublishTime(entry.PublishedAt))
	fmt.Fprintf(out, "r2 url:       %s\n", formatR2URL(cfg.Feed.BaseURL, entry.R2Key, token))
	fmt.Fprintf(out, "duration:     %s\n", formatDuration(entry.DurationSeconds))
	return nil
}

// formatDropped categorizes SkippedSegments by type-prefix and pairs
// the count with the parsed-footnote total. The bead's sample shows
// "4 code blocks (Lisp), 0 footnotes"; we omit the language hint
// (Lisp) — language detection is out of scope for v1, the count is
// what the operator actually needs for "did the extractor drop too
// much?" diagnosis.
//
// SkippedSegments comes from internal/extract/strip.go's
// StripMarkdown, which currently emits descriptors like
// "code block:..." and "image:..." (one per dropped segment).
func formatDropped(skipped []string, footnoteCount int) string {
	if len(skipped) == 0 && footnoteCount == 0 {
		return "0 segments, 0 footnotes"
	}

	codeBlocks, images, other := 0, 0, 0
	for _, s := range skipped {
		switch {
		case strings.HasPrefix(s, "code block"):
			codeBlocks++
		case strings.HasPrefix(s, "image"):
			images++
		default:
			other++
		}
	}

	parts := make([]string, 0, 4)
	if codeBlocks > 0 {
		parts = append(parts, pluralize(codeBlocks, "code block", "code blocks"))
	}
	if images > 0 {
		parts = append(parts, pluralize(images, "image", "images"))
	}
	if other > 0 {
		parts = append(parts, pluralize(other, "other segment", "other segments"))
	}
	parts = append(parts, pluralize(footnoteCount, "footnote", "footnotes"))
	return strings.Join(parts, ", ")
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// formatChunks renders "N × ~M chars" — N=count, M=avg-chars per
// chunk rounded to nearest hundred. Matches the §3 sample format
// "3 × ~2,700 chars".
func formatChunks(chunks []model.AudioChunk) string {
	if len(chunks) == 0 {
		return "0"
	}
	total := 0
	for _, c := range chunks {
		total += c.CharCount
	}
	avg := total / len(chunks)
	// Round to nearest 100 so the output is stable across small
	// edits to the source — operators recognize "~2,700" not
	// "2,743".
	rounded := ((avg + 50) / 100) * 100
	return fmt.Sprintf("%d × ~%s chars", len(chunks), formatThousands(rounded))
}

// formatBuildTime renders GeneratedAt as RFC3339 UTC. The bead
// sample uses "2026-05-08T14:21:03Z" — Go's time.RFC3339 format
// produces that exactly when the value is in UTC.
func formatBuildTime(t time.Time) string {
	if t.IsZero() {
		return "not yet built"
	}
	return t.UTC().Format(time.RFC3339)
}

// formatPublishTime: PublishedAt is *time.Time on ManifestEntry —
// nil means "uploaded never happened" (e.g. build complete but
// publish not run).
func formatPublishTime(p *time.Time) string {
	if p == nil {
		return "not yet published"
	}
	return p.UTC().Format(time.RFC3339)
}

// formatR2URL composes the token-stamped enclosure URL the same way
// the publish flow does, so an operator can paste this URL into a
// browser to verify access. Empty R2Key surfaces as a clear "not
// yet uploaded" rather than emitting a bogus URL.
func formatR2URL(baseURL, r2Key, token string) string {
	if r2Key == "" {
		return "not yet uploaded"
	}
	if token == "" {
		return strings.TrimRight(baseURL, "/") + "/" + r2Key + " (set WIKI_AUDIO_ACCESS_TOKEN to stamp the token)"
	}
	return fmt.Sprintf("%s/%s?t=%s", strings.TrimRight(baseURL, "/"), r2Key, token)
}

// formatDuration renders DurationSeconds as "MMmSSs" (e.g. "12m04s").
// 0 / negative → "—" so a build-not-yet-run essay's duration line
// reads cleanly.
func formatDuration(seconds float64) string {
	if seconds <= 0 {
		return "—"
	}
	total := int(seconds + 0.5)
	mins := total / 60
	secs := total % 60
	return fmt.Sprintf("%dm%02ds", mins, secs)
}
