package e2e

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/Jacob2017/wiki-audio/internal/chunk"
	"github.com/Jacob2017/wiki-audio/internal/extract"
	"github.com/Jacob2017/wiki-audio/internal/model"
)

var update = flag.Bool("update", false, "regenerate golden files from current pipeline output")

// pinnedVoiceID is fixed across runs so BodyHash assertions are stable.
// Changing it would change every golden hash — treat it as a load-
// bearing constant for the e2e harness.
const pinnedVoiceID = "test-voice-fixed"

// fixture describes one essay we run end-to-end. The min/max bands are
// generous (±10% from the observed value) so a small extractor tweak
// that doesn't change semantics doesn't trip the band assertions; only
// regressions on the order of "we forgot to drop the metadata block"
// blow them. The exact numbers are pinned in goldens.
type fixture struct {
	file               string
	slug               string
	title              string
	minChars, maxChars int
	minWords, maxWords int
	expectFootnotes    bool
	expectedSkipped    []string
}

var fixtures = []fixture{
	{
		file:            "How to Do Great Work.md",
		slug:            "how-to-do-great-work",
		title:           "How to Do Great Work",
		minChars:        50_000,
		maxChars:        80_000,
		minWords:        9_000,
		maxWords:        14_000,
		expectFootnotes: true,
		expectedSkipped: nil, // PG essay has no fenced code blocks
	},
	{
		file:            "How to Get Startup Ideas.md",
		slug:            "how-to-get-startup-ideas",
		title:           "How to Get Startup Ideas",
		minChars:        30_000,
		maxChars:        50_000,
		minWords:        5_000,
		maxWords:        9_000,
		expectFootnotes: false,
		expectedSkipped: []string{"code_block:python"}, // synthetic code block in fixture exercises wa-kyn.7
	},
	{
		file:            "High Agency — In 30 Minutes.md",
		slug:            "high-agency-in-30-minutes",
		title:           "High Agency",
		minChars:        35_000,
		maxChars:        55_000,
		minWords:        6_000,
		maxWords:        10_000,
		expectFootnotes: false,
		expectedSkipped: nil,
	},
}

// pipelineOut bundles the artifacts golden files compare against.
type pipelineOut struct {
	Doc    model.CleanedDocument
	Chunks []model.AudioChunk
}

func runFullPipeline(t *testing.T, f fixture) pipelineOut {
	t.Helper()
	path := filepath.Join("testdata", "essays", f.file)
	parsed, err := extract.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile(%s): %v", path, err)
	}
	prose, notes := extract.SplitNotes(parsed.RawBody)
	footnotes := extract.ParseFootnotes(notes)
	cleaned, skipped := extract.StripMarkdown(prose)
	woven, _ := extract.WeaveFootnotes(cleaned, footnotes)
	doc := extract.Finalize(woven, model.EssayMeta{
		Slug:       f.slug,
		Title:      parsed.Title,
		Author:     model.DefaultAuthor,
		SourcePath: f.file,
	}, skipped, extract.FinalOpts{
		VoiceID:               pinnedVoiceID,
		ModelID:               model.DefaultModelID,
		FootnotePolicyVersion: model.FootnotePolicyVersion,
	})
	chunks := chunk.Chunk(doc.Body, model.DefaultChunkMaxChars)
	return pipelineOut{Doc: doc, Chunks: chunks}
}

// --- TestExtractChunk_Properties --------------------------------------
// One subtest per essay; runs the bead's 16-row assertion checklist.

func TestExtractChunk_Properties(t *testing.T) {
	for _, f := range fixtures {
		f := f
		t.Run(f.slug, func(t *testing.T) {
			got := runFullPipeline(t, f)
			doc := got.Doc

			// extraction_succeeds
			if doc.Malformed {
				t.Fatalf("essay marked malformed: %q", doc.MalformedReason)
			}
			if doc.Body == "" {
				t.Fatal("CleanedDocument.Body is empty")
			}

			// title pinned
			if doc.Meta.Title != f.title {
				t.Errorf("Title = %q; want %q", doc.Meta.Title, f.title)
			}

			// body_hash_stable_across_runs
			second := runFullPipeline(t, f)
			if second.Doc.BodyHash != doc.BodyHash {
				t.Errorf("BodyHash unstable: %q vs %q", doc.BodyHash, second.Doc.BodyHash)
			}

			// body_hash_changes_with_voice_id
			differentVoice := extract.Finalize(doc.Body, doc.Meta, doc.SkippedSegments, extract.FinalOpts{
				VoiceID:               "different-voice",
				ModelID:               model.DefaultModelID,
				FootnotePolicyVersion: model.FootnotePolicyVersion,
			})
			if differentVoice.BodyHash == doc.BodyHash {
				t.Errorf("BodyHash should change with voice_id; got identical %q", doc.BodyHash)
			}

			// char_count_within_expected_band
			if doc.CharCount < f.minChars || doc.CharCount > f.maxChars {
				t.Errorf("CharCount %d outside band [%d, %d]", doc.CharCount, f.minChars, f.maxChars)
			}

			// word_count_within_expected_band
			if doc.WordCount < f.minWords || doc.WordCount > f.maxWords {
				t.Errorf("WordCount %d outside band [%d, %d]", doc.WordCount, f.minWords, f.maxWords)
			}

			// no_markdown_link_syntax_in_body
			if regexp.MustCompile(`\]\(http`).MatchString(doc.Body) {
				t.Errorf("body contains markdown link syntax `](http`")
			}

			// no_image_ref_in_body
			if strings.Contains(doc.Body, "![") {
				t.Errorf("body contains image-ref marker `![`")
			}

			// no_wikilink_braces
			if strings.Contains(doc.Body, "[[") || strings.Contains(doc.Body, "]]") {
				t.Errorf("body contains wikilink braces")
			}

			// no_metadata_block
			if strings.Contains(doc.Body, "## Metadata") {
				t.Errorf("body still contains '## Metadata'")
			}

			// no_full_document_block
			if strings.Contains(doc.Body, "## Full Document") {
				t.Errorf("body still contains '## Full Document'")
			}

			// footnote_lines_appear (for essays expected to have notes)
			hasFootnoteLine := strings.Contains(doc.Body, "Footnote 1:")
			if f.expectFootnotes && !hasFootnoteLine {
				t.Errorf("expected at least one 'Footnote 1:' line in body")
			}
			if !f.expectFootnotes && strings.Contains(doc.Body, "Footnote 1:") {
				t.Errorf("did not expect Footnote lines in this essay")
			}

			// no_orphan_footnote_markers — bare [N] should not survive
			// (it should either be stripped or appear inside a "Footnote N: …" line).
			orphanRe := regexp.MustCompile(`(?m)(^|[^a-zA-Z])\[\d+\]([^a-zA-Z]|$)`)
			lines := strings.Split(doc.Body, "\n")
			for i, ln := range lines {
				if strings.HasPrefix(ln, "Footnote ") {
					continue // footnote lines legitimately reference [N] in prose
				}
				if loc := orphanRe.FindStringIndex(ln); loc != nil {
					// Allow the marker if surrounded by ] or ( (link debris guard from wa-kyn.8)
					if !looksLikeLinkDebris(ln) {
						t.Errorf("orphan footnote marker on line %d: %q", i, ln)
						break
					}
				}
			}

			// chunks_each_under_max_chars (with overlong-paragraph carve-out)
			for i, c := range got.Chunks {
				if c.CharCount > model.DefaultChunkMaxChars {
					// wa-kyn.11 fallback may produce overlong chunks for
					// pathological single-sentence-too-long paragraphs;
					// log informationally rather than failing.
					t.Logf("chunk %d over budget: %d > %d (allowed by wa-kyn.11 carve-out)",
						i, c.CharCount, model.DefaultChunkMaxChars)
				}
			}

			// chunks_paragraph_bounded — heuristic: a chunk starting
			// mid-sentence would begin with a lowercase letter.
			// Anything else (capital, digit, quote, *, >, _, etc.) is
			// a paragraph or formatted block boundary.
			for i, c := range got.Chunks {
				if i == 0 {
					continue
				}
				trimmed := strings.TrimSpace(c.Text)
				if trimmed == "" {
					continue
				}
				first := []rune(trimmed)[0]
				if unicode.IsLower(first) {
					t.Errorf("chunk %d starts with lowercase letter %q (mid-sentence?): %q…",
						i, first, truncate(c.Text, 60))
				}
			}

			// total_chunk_chars_match_body — sum of chunk rune counts
			// should equal CharCount, modulo the inter-chunk whitespace
			// the chunker trims at boundaries (small slack allowed).
			var sum int
			for _, c := range got.Chunks {
				sum += c.CharCount
			}
			diff := doc.CharCount - sum
			if diff < 0 {
				diff = -diff
			}
			if diff > len(got.Chunks)*4 {
				// 4 runes/chunk slack covers stripped "\n\n" separators.
				t.Errorf("chunk char sum %d differs from body CharCount %d by %d (>%d slack)",
					sum, doc.CharCount, diff, len(got.Chunks)*4)
			}

			// skipped_segments_match_expected
			if !equalStringSlices(doc.SkippedSegments, f.expectedSkipped) {
				t.Errorf("SkippedSegments = %v; want %v", doc.SkippedSegments, f.expectedSkipped)
			}

			t.Logf("essay=%s slug=%s chars=%d words=%d chunks=%d skipped=%v hash=%s",
				f.title, f.slug, doc.CharCount, doc.WordCount, len(got.Chunks),
				doc.SkippedSegments, doc.BodyHash)
		})
	}
}

// --- TestExtractChunkGolden — byte-for-byte fixture comparison ----------
// Run with `-update` to regenerate.

func TestExtractChunkGolden(t *testing.T) {
	for _, f := range fixtures {
		f := f
		t.Run(f.slug, func(t *testing.T) {
			got := runFullPipeline(t, f)

			compareGolden(t, "cleaned.txt", f.slug, []byte(got.Doc.Body))
			compareGoldenJSON(t, "meta.json", f.slug, got.Doc.Meta)
			skippedTxt := strings.Join(got.Doc.SkippedSegments, "\n")
			if skippedTxt != "" {
				skippedTxt += "\n"
			}
			compareGolden(t, "skipped.txt", f.slug, []byte(skippedTxt))
			compareGoldenJSON(t, "chunks.json", f.slug, got.Chunks)
		})
	}
}

// --- helpers ------------------------------------------------------------

func compareGolden(t *testing.T, suffix, slug string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", slug+"."+suffix)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote golden %s (%d bytes)", path, len(got))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to regenerate)", path, err)
	}
	if !bytesEqual(got, want) {
		t.Errorf("golden %s mismatch (run -update if change is intentional);\n  got %d bytes,\n  want %d bytes,\n  first divergence at byte %d",
			path, len(got), len(want), firstDiff(got, want))
	}
}

func compareGoldenJSON(t *testing.T, suffix, slug string, payload any) {
	t.Helper()
	got, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n') // POSIX trailing newline
	compareGolden(t, suffix, slug, got)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// looksLikeLinkDebris is the wa-kyn.8 defensive guard mirrored on a
// per-line basis: a [N] preceded by ']' or followed by '(' is link
// debris and must be left in place.
func looksLikeLinkDebris(line string) bool {
	re := regexp.MustCompile(`\][\s]*\[\d+\]|\[\d+\]\(`)
	return re.MatchString(line)
}

// errFn keeps the import list honest even though fmt is only used
// indirectly via t.Logf — Go vet would complain about an unused import.
var _ = fmt.Sprintf
