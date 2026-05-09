package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/extract"
	"github.com/Jacob2017/wiki-audio/internal/model"
)

// errExtractOnlyRequiresSlug is returned when `--extract-only` is set
// without `--slug X`. wa-kyn.15's contract is single-essay
// inspection — printing all 53 essay bodies to stdout would be
// useless and surprising.
var errExtractOnlyRequiresSlug = errors.New(
	"build --extract-only requires --slug X (single-essay inspection per §7 Phase D gate)")

// runBuildExtractOnly implements `wiki-audio build --extract-only --slug X`
// per wa-kyn.15. Walks the configured source dir, finds the single
// essay matching --slug, runs the §5.1 extraction chain (steps 1-12,
// no §5.2 chunking, no TTS, no file writes), and prints the cleaned
// body to stdout. Used for hand-verifying extraction quality on
// individual essays during development.
//
// Output: the doc.Body string, terminated by a single trailing
// newline. Pipe-friendly — `wiki-audio build --extract-only --slug X | wc -w`
// gives a sane word count without parsing a header. Slog stderr
// messages still fire (e.g. wa-kyn.11 overlong-paragraph warnings).
//
// If the essay is found but extraction returns an error, the error
// propagates to cobra (non-zero exit). If the essay is missing
// metadata headers (Readwise canonical format), that surfaces as the
// extractor's own error message rather than a generic "extraction
// failed" — wa-kyn.19 F1 lives at this seam.
func runBuildExtractOnly(cmd *cobra.Command, flags *buildFlags) error {
	if flags.slug == "" {
		return errExtractOnlyRequiresSlug
	}

	configPath, _ := cmd.Flags().GetString("config")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return err
	}

	files, err := listMarkdownFiles(cfg.Wiki.SourceDir)
	if err != nil {
		return fmt.Errorf("extract-only: scan %s: %w", cfg.Wiki.SourceDir, err)
	}
	files = filterBySlug(files, flags.slug)
	if len(files) == 0 {
		return fmt.Errorf("extract-only: no essay matched --slug %q under %s",
			flags.slug, cfg.Wiki.SourceDir)
	}
	if len(files) > 1 {
		// slugFromPath collisions are theoretically possible if two
		// .md files differ only by case or punctuation. wa-6la F4
		// will tighten slug derivation; for now surface the
		// collision rather than silently picking one.
		return fmt.Errorf("extract-only: --slug %q matched %d files; rename source files to disambiguate",
			flags.slug, len(files))
	}

	doc, err := extractOnlyDoc(files[0], cfg)
	if err != nil {
		return fmt.Errorf("extract-only: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), doc.Body)
	return nil
}

// extractOnlyDoc runs the §5.1 chain (parse → split → footnotes →
// strip → weave → finalize) on path. Mirrors buildOneEssay's chain
// but returns the full CleanedDocument (not just stats) and skips
// the §5.2 chunker — the bead's contract is "no chunking".
//
// Errors from earlier in the chain (e.g. ParseFile rejecting a
// non-Readwise format) propagate verbatim so the operator can
// diagnose extraction faults visible to the build flow.
func extractOnlyDoc(path string, cfg *model.Config) (model.CleanedDocument, error) {
	parsed, err := extract.ParseFile(path)
	if err != nil {
		return model.CleanedDocument{}, err
	}
	prose, notesPart := extract.SplitNotes(parsed.RawBody)
	footnotes := extract.ParseFootnotes(notesPart)
	cleaned, skipped := extract.StripMarkdown(prose)
	woven, _ := extract.WeaveFootnotes(cleaned, footnotes)

	doc := extract.Finalize(woven, model.EssayMeta{
		Slug:       slugFromPath(path),
		Title:      parsed.Title,
		Author:     model.DefaultAuthor,
		SourcePath: path,
		SourceURL:  parsed.Meta.SourceURL,
		Summary:    parsed.Meta.Summary,
	}, skipped, extract.FinalOpts{
		VoiceID:               cfg.TTS.VoiceID,
		ModelID:               cfg.TTS.ModelID,
		FootnotePolicyVersion: model.FootnotePolicyVersion,
	})
	return doc, nil
}
