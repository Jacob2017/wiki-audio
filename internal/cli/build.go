package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Jacob2017/wiki-audio/internal/chunk"
	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/extract"
	"github.com/Jacob2017/wiki-audio/internal/model"
)

// creditsPerCharFlash is the §3 / wa-4cw billing rate for the
// eleven_flash_v2_5 model. Pinned here (not derived from cfg) because
// the dry-run estimate is supposed to match the §3 sample output
// verbatim. If a future model ships at a different rate, add a
// per-model lookup; for v1 the project is single-model.
const creditsPerCharFlash = 0.5

// proTierCredits / proTierDollars convert credits into dollars for
// the second summary line. ElevenLabs' Pro plan is $99 / 500k credits;
// we report dollars proportional to that ratio (effectively $/credit).
// Going off-ratio for high credit volumes is the user's tier-shopping
// problem; planFitMessage flags those buckets.
const (
	proTierCredits   = 500_000
	scaleTierCredits = 2_000_000
	proTierDollars   = 99
)

type buildFlags struct {
	dryRun          bool
	extractOnly     bool
	slug            string
	forceRegression bool
}

func newBuildCmd() *cobra.Command {
	flags := &buildFlags{}
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Extract + synthesize stale essays",
		Long: "Build extracts every .md under [wiki].source_dir and (eventually) " +
			"synthesizes audio for the ones whose body_hash is missing or stale.\n\n" +
			"--dry-run skips synthesis entirely: it prints the per-essay extraction " +
			"summary plus a total credit/cost estimate. Use it as a cost preflight " +
			"before bulk runs (§7 Phase G) and as a sanity check on the extractor.",
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case flags.extractOnly:
				return runBuildExtractOnly(cmd, flags)
			case flags.dryRun:
				return runBuildDryRun(cmd, flags)
			default:
				return notImplemented("build")(cmd, args)
			}
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"extract + estimate cost only; no API calls, no file writes")
	cmd.Flags().BoolVar(&flags.extractOnly, "extract-only", false,
		"extract one essay (selected by --slug) and print the cleaned body to stdout; "+
			"no chunking, no TTS, no file writes (§7 Phase D gate; wa-kyn.15)")
	cmd.Flags().StringVar(&flags.slug, "slug", "",
		"narrow to a single essay by slug (basename without .md, lowercased)")
	cmd.Flags().BoolVar(&flags.forceRegression, "force-regression", false,
		"downgrade extractor regression detection to a warning (regression check itself "+
			"depends on r2 manifest fetch — currently a no-op pending wa-cfn.* / wa-76r.1)")
	cmd.MarkFlagsMutuallyExclusive("dry-run", "extract-only")
	return cmd
}

// runBuildDryRun walks the configured source directory, runs the §5.1
// extractor + §5.2 chunker on each .md file, and prints a per-essay
// line plus a two-line summary in the format pinned by §3 (see the
// polish-pass-6 comment on wa-kyn.14).
//
// No HTTP calls are issued. No files are written. The only outputs
// are stdout (the report) and stderr (slog messages from the
// extractor and chunker — e.g. wa-kyn.11 overlong-paragraph warnings).
func runBuildDryRun(cmd *cobra.Command, flags *buildFlags) error {
	configPath, _ := cmd.Flags().GetString("config")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}

	files, err := listMarkdownFiles(cfg.Wiki.SourceDir)
	if err != nil {
		return fmt.Errorf("dry-run: scan %s: %w", cfg.Wiki.SourceDir, err)
	}
	if flags.slug != "" {
		files = filterBySlug(files, flags.slug)
		if len(files) == 0 {
			return fmt.Errorf("dry-run: no essay matched --slug %q under %s", flags.slug, cfg.Wiki.SourceDir)
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("dry-run: no .md essays found under %s", cfg.Wiki.SourceDir)
	}

	out := cmd.OutOrStdout()
	logger := slog.With("phase", "dry-run", "n_essays", len(files))
	logger.Info("starting dry-run extraction sweep", "source_dir", cfg.Wiki.SourceDir)

	var totalChars int
	for _, path := range files {
		r := buildOneEssay(path, cfg)
		printEssayLine(out, r)
		if r.err != nil {
			logger.Warn("dry-run: extraction error",
				"slug", r.slug, "err", r.err.Error())
			continue
		}
		if r.malformed {
			logger.Warn("dry-run: skipping malformed essay",
				"slug", r.slug, "reason", r.malformedReason)
			continue
		}
		totalChars += r.charCount
	}

	if flags.forceRegression {
		logger.Info("--force-regression set; regression check is a no-op until r2 manifest fetch lands")
	}

	printSummary(out, cfg.TTS.ModelID, totalChars)
	return nil
}

// essayResult captures everything dry-run needs to print for one
// essay. err is non-nil for read/parse failures. malformed is true
// when the body cleared parsing but failed §6's MinBodyChars gate.
type essayResult struct {
	path            string
	slug            string
	title           string
	charCount       int
	chunkCount      int
	skippedSegments []string
	malformed       bool
	malformedReason string
	err             error
}

// buildOneEssay runs the full §5.1 extraction chain (steps 1-12) plus
// §5.2 chunking on a single source file. Errors before Finalize land
// in the returned essayResult.err so the caller can keep marching
// without aborting the whole sweep.
func buildOneEssay(path string, cfg *model.Config) essayResult {
	slug := slugFromPath(path)
	parsed, err := extract.ParseFile(path)
	if err != nil {
		return essayResult{path: path, slug: slug, err: err}
	}
	prose, notesPart := extract.SplitNotes(parsed.RawBody)
	footnotes := extract.ParseFootnotes(notesPart)
	cleaned, skipped := extract.StripMarkdown(prose)
	woven, _ := extract.WeaveFootnotes(cleaned, footnotes)
	doc := extract.Finalize(woven, model.EssayMeta{
		Slug:       slug,
		Title:      parsed.Title,
		Author:     model.DefaultAuthor,
		SourcePath: path,
	}, skipped, extract.FinalOpts{
		VoiceID:               cfg.TTS.VoiceID,
		ModelID:               cfg.TTS.ModelID,
		FootnotePolicyVersion: model.FootnotePolicyVersion,
	})
	chunks := chunk.Chunk(doc.Body, cfg.TTS.ChunkMaxChars)
	return essayResult{
		path:            path,
		slug:            slug,
		title:           doc.Meta.Title,
		charCount:       doc.CharCount,
		chunkCount:      len(chunks),
		skippedSegments: doc.SkippedSegments,
		malformed:       doc.Malformed,
		malformedReason: doc.MalformedReason,
	}
}

// listMarkdownFiles returns every .md file under dir, sorted
// lexicographically so dry-run output is reproducible across runs
// (golden-file ready).
func listMarkdownFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

// filterBySlug returns the subset of paths whose slug equals the
// requested filter (case-insensitive comparison via slugFromPath).
func filterBySlug(paths []string, slug string) []string {
	want := strings.ToLower(slug)
	var out []string
	for _, p := range paths {
		if slugFromPath(p) == want {
			out = append(out, p)
		}
	}
	return out
}

// slugFromPath derives a best-effort slug from the file basename.
// Pane-5's wa-6la review (F4) flags slug derivation as undefined in
// the spec; this implementation is the minimum useful default for
// dry-run / --slug filtering and may be replaced once F4 is resolved.
func slugFromPath(path string) string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	s := strings.ToLower(stem)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "—", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

func printEssayLine(w io.Writer, r essayResult) {
	switch {
	case r.err != nil:
		fmt.Fprintf(w, "%s: ERROR %s\n", r.slug, r.err.Error())
	case r.malformed:
		fmt.Fprintf(w, "%s: SKIPPED (malformed: %s)\n", r.slug, r.malformedReason)
	default:
		extra := ""
		if len(r.skippedSegments) > 0 {
			extra = fmt.Sprintf(" dropped=%s", strings.Join(r.skippedSegments, ","))
		}
		fmt.Fprintf(w, "%s: chars=%s chunks=%d%s\n",
			r.slug, formatThousands(r.charCount), r.chunkCount, extra)
	}
}

// printSummary emits the §3 two-line cost estimate. The exact
// punctuation / unit tags match the polish-pass-6 contract on
// wa-kyn.14 so future cost-vs-dry-run parity tests can string-match.
func printSummary(w io.Writer, modelID string, totalChars int) {
	credits := float64(totalChars) * creditsPerCharFlash
	fmt.Fprintf(w, "estimate: %s chars × %s credits/char (%s) = %s credits\n",
		formatThousands(totalChars),
		formatRate(creditsPerCharFlash),
		modelID,
		formatThousands(int(credits)),
	)

	dollars := credits / float64(proTierCredits) * float64(proTierDollars)
	fmt.Fprintf(w, "estimate: ~$%.0f on Pro tier overage; %s\n",
		dollars, planFitMessage(int(credits)))
}

func planFitMessage(credits int) string {
	switch {
	case credits <= proTierCredits:
		return "fits within Pro monthly quota"
	case credits <= scaleTierCredits:
		return "exceeds Pro; Scale tier required for one-shot run"
	default:
		return "requires multi-month split or Enterprise quote"
	}
}

// formatThousands renders n with comma separators. Used for the
// chars/credits columns where the §3 sample output is comma-grouped.
// Negative numbers (which shouldn't appear in this code path) print
// without a leading "-".
func formatThousands(n int) string {
	if n < 0 {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	head := len(s) % 3
	if head > 0 {
		b.WriteString(s[:head])
		if len(s) > head {
			b.WriteByte(',')
		}
	}
	for i := head; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// formatRate renders a rate like 0.5 with the minimal trailing
// zeros (so "0.5" not "0.50"). Used in the credits/char string in the
// summary line so the output matches the §3 sample verbatim.
func formatRate(r float64) string {
	s := fmt.Sprintf("%g", r)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}
