package extract

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// pipelineResult bundles every intermediate from the §5.1 chain so a
// single edge-case test can assert against the relevant stage(s).
type pipelineResult struct {
	Parsed    Parsed
	Prose     string
	Notes     string
	Footnotes map[int]string
	Cleaned   string
	Skipped   []string
	Woven     string
	Stats     WeaveStats
	Doc       model.CleanedDocument
}

// runPipeline runs the full §5.1 chain (steps 1-12) on essay and
// returns every intermediate. slug is caller-supplied (F4 from
// pane-5's wa-6la review — slug derivation is not the extractor's job).
func runPipeline(t *testing.T, essay, name, slug string) pipelineResult {
	t.Helper()
	parsed, err := Parse(essay, name)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	prose, notes := SplitNotes(parsed.RawBody)
	footnotes := ParseFootnotes(notes)
	cleaned, skipped := StripMarkdown(prose)
	woven, stats := WeaveFootnotes(cleaned, footnotes)
	doc := Finalize(woven, model.EssayMeta{
		Slug:       slug,
		Title:      parsed.Title,
		Author:     model.DefaultAuthor,
		SourcePath: name,
	}, skipped, FinalOpts{
		VoiceID:               "test-voice",
		ModelID:               model.DefaultModelID,
		FootnotePolicyVersion: model.FootnotePolicyVersion,
	})
	return pipelineResult{
		Parsed: parsed, Prose: prose, Notes: notes,
		Footnotes: footnotes, Cleaned: cleaned, Skipped: skipped,
		Woven: woven, Stats: stats, Doc: doc,
	}
}

// padBodyToMin appends filler text until prose clears
// model.MinBodyChars + a margin so an integration test isn't drowned
// by the Malformed gate. Filler does not contain any markdown noise
// so its presence in assertions is benign.
func padBodyToMin(prose string) string {
	const filler = "Filler word filler word filler word filler word filler word. "
	for utf8.RuneCountInString(prose) < model.MinBodyChars+50 {
		prose += filler
	}
	return prose
}

// buildReadwiseEssay assembles a synthetic essay in the canonical
// Readwise export shape that ParseFile expects.
func buildReadwiseEssay(prose, notes string) string {
	s := "# Test Essay\n\n## Metadata\n- Author: x\n- URL: https://example.com\n\n## Full Document\n" + prose
	if notes != "" {
		s += "\n\n## Notes\n\n" + notes
	}
	return s + "\n"
}

// --- Bead matrix row 1 — no_notes_section --------------------------------

func TestPipeline_NoNotesSection(t *testing.T) {
	essay := buildReadwiseEssay(padBodyToMin("Plain prose without footnotes. Just paragraphs."), "")
	got := runPipeline(t, essay, "x.md", "no-notes")
	if len(got.Footnotes) != 0 {
		t.Errorf("Footnotes should be empty; got %v", got.Footnotes)
	}
	if strings.Contains(got.Doc.Body, "Footnote ") {
		t.Errorf("body should have no 'Footnote ' lines; got %q", got.Doc.Body)
	}
	if got.Doc.Malformed {
		t.Errorf("body should not be malformed; reason: %q", got.Doc.MalformedReason)
	}
}

// --- Bead matrix row 2 — orphan_marker (in prose, no def) ----------------

func TestPipeline_OrphanMarker(t *testing.T) {
	prose := padBodyToMin("Prose mentioning [99] without a definition. ")
	notes := "[1] Some unrelated definition that is plenty long enough."
	essay := buildReadwiseEssay(prose, notes)
	got := runPipeline(t, essay, "x.md", "orphan-marker")
	if strings.Contains(got.Doc.Body, "[99]") {
		t.Errorf("orphan marker should be stripped; got body containing [99]: %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "Footnote 99:") {
		t.Errorf("orphan marker should NOT emit Footnote line")
	}
	if !contains(got.Stats.Unknown, 99) {
		t.Errorf("Stats.Unknown should contain 99; got %v", got.Stats.Unknown)
	}
}

// --- Bead matrix row 3 — orphan_definition (in notes, never referenced) --

func TestPipeline_OrphanDefinition(t *testing.T) {
	prose := padBodyToMin("Prose without any footnote references. ")
	notes := "[1] Defined but never referenced."
	essay := buildReadwiseEssay(prose, notes)
	got := runPipeline(t, essay, "x.md", "orphan-def")
	if strings.Contains(got.Doc.Body, "Defined but never referenced") {
		t.Errorf("orphan definition body should NOT appear in output; got %q", got.Doc.Body)
	}
	if !reflect.DeepEqual(got.Stats.Unreferenced, []int{1}) {
		t.Errorf("Stats.Unreferenced = %v; want [1]", got.Stats.Unreferenced)
	}
}

// --- Bead matrix row 4 — double_reference --------------------------------

func TestPipeline_DoubleReferenceEmitsOnce(t *testing.T) {
	prose := "First mention [1], later mention [1] again. " + padBodyToMin("")
	notes := "[1] The note body content."
	essay := buildReadwiseEssay(prose, notes)
	got := runPipeline(t, essay, "x.md", "double-ref")
	if strings.Count(got.Doc.Body, "Footnote 1:") != 1 {
		t.Errorf("Footnote 1 should emit exactly once; got %d in %q",
			strings.Count(got.Doc.Body, "Footnote 1:"), got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "[1]") {
		t.Errorf("all [1] markers should be stripped; got %q", got.Doc.Body)
	}
}

// --- Bead matrix row 5 — link_debris -------------------------------------

func TestPipeline_LinkDebrisLeftAlone(t *testing.T) {
	prose := padBodyToMin("Some [label][5] reference-style debris that should not be woven. ")
	notes := "[5] This body should not be emitted."
	essay := buildReadwiseEssay(prose, notes)
	got := runPipeline(t, essay, "x.md", "link-debris")
	if !strings.Contains(got.Doc.Body, "[5]") {
		t.Errorf("link-debris [5] should remain; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "Footnote 5:") {
		t.Errorf("link-debris should not emit Footnote line; got %q", got.Doc.Body)
	}
	if !contains(got.Stats.Unreferenced, 5) {
		t.Errorf("Stats.Unreferenced should contain 5; got %v", got.Stats.Unreferenced)
	}
}

// --- Bead matrix row 6 — notes_absent_markers_present --------------------

func TestPipeline_NotesAbsentMarkersPresent(t *testing.T) {
	prose := padBodyToMin("A paragraph with [1] and [2] markers but no Notes section. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "no-notes-with-markers")
	if strings.Contains(got.Doc.Body, "[1]") || strings.Contains(got.Doc.Body, "[2]") {
		t.Errorf("markers should be stripped silently when no Notes section; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "Footnote ") {
		t.Errorf("no Footnote line should appear when footnote_map is empty; got %q", got.Doc.Body)
	}
	want := []int{1, 2}
	if !reflect.DeepEqual(got.Stats.Unknown, want) {
		t.Errorf("Stats.Unknown = %v; want %v", got.Stats.Unknown, want)
	}
}

// --- Bead matrix row 7 — code_block_dropped ------------------------------

func TestPipeline_CodeBlockDropped(t *testing.T) {
	prose := padBodyToMin("Before the block.\n\n```python\nprint('drop me')\nprint('and me')\n```\n\nAfter the block. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "code-block")
	if strings.Contains(got.Doc.Body, "drop me") || strings.Contains(got.Doc.Body, "print(") {
		t.Errorf("code body should be dropped; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "Before the block.") || !strings.Contains(got.Doc.Body, "After the block.") {
		t.Errorf("surrounding prose should survive; got %q", got.Doc.Body)
	}
	if !reflect.DeepEqual(got.Doc.SkippedSegments, []string{"code_block:python"}) {
		t.Errorf("SkippedSegments = %v; want [code_block:python]", got.Doc.SkippedSegments)
	}
}

// --- Operator extra: code-fence-between-paragraphs in dense prose ---------
// The bead's row 7 covers the basic case; this exercises a code fence
// surrounded by additional content (multiple paragraphs each side).

func TestPipeline_CodeFenceWithDenseSurroundingParagraphs(t *testing.T) {
	prose := padBodyToMin(`Para one introduces the topic.

Para two leads into the example.

` + "```\nblock without a language\n```" + `

Para three reflects on the example.

Para four wraps it up. `)
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "fence-mid-prose")
	if strings.Contains(got.Doc.Body, "block without a language") {
		t.Errorf("fence body should be dropped; got %q", got.Doc.Body)
	}
	if !reflect.DeepEqual(got.Doc.SkippedSegments, []string{"code_block"}) {
		t.Errorf("SkippedSegments = %v; want [code_block]", got.Doc.SkippedSegments)
	}
	for _, expect := range []string{"Para one", "Para two", "Para three", "Para four"} {
		if !strings.Contains(got.Doc.Body, expect) {
			t.Errorf("expected %q to survive; body = %q", expect, got.Doc.Body)
		}
	}
}

// --- Bead matrix row 8 — image_ref_dropped -------------------------------

func TestPipeline_ImageRefDropped(t *testing.T) {
	prose := padBodyToMin("![cover](https://example.com/img.png)\n\nThe paragraph after the cover image. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "image-ref")
	if strings.Contains(got.Doc.Body, "https://example.com/img.png") {
		t.Errorf("image URL should be stripped; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "![") {
		t.Errorf("image marker '![' should be stripped; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "The paragraph after") {
		t.Errorf("subsequent prose should survive; got %q", got.Doc.Body)
	}
}

// --- Bead matrix row 9 — inline_link_text_kept ---------------------------

func TestPipeline_InlineLinkTextKept(t *testing.T) {
	prose := padBodyToMin("See [the original essay](https://paulgraham.com/x.html) for context. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "inline-link")
	if strings.Contains(got.Doc.Body, "https://paulgraham.com") {
		t.Errorf("URL should be stripped; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "the original essay") {
		t.Errorf("link text should survive; got %q", got.Doc.Body)
	}
}

// --- Bead matrix row 10 — wikilink_with_display --------------------------

func TestPipeline_WikilinkWithDisplay(t *testing.T) {
	prose := padBodyToMin("As [[deep-target|Paul]] argued. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "wikilink-display")
	if strings.Contains(got.Doc.Body, "deep-target") {
		t.Errorf("wikilink slug should be replaced; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "As Paul argued.") {
		t.Errorf("wikilink display should survive; got %q", got.Doc.Body)
	}
}

// --- Bead matrix row 11 — wikilink_no_display ----------------------------

func TestPipeline_WikilinkNoDisplay(t *testing.T) {
	prose := padBodyToMin("Refer to [[paul-graham]] for the original. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "wikilink-bare")
	if strings.Contains(got.Doc.Body, "[[paul-graham]]") {
		t.Errorf("wikilink should be flattened; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "Refer to paul graham for the original.") {
		t.Errorf("hyphens should become spaces; got %q", got.Doc.Body)
	}
}

// --- Bead matrix row 12 — metadata_section_dropped -----------------------

func TestPipeline_MetadataSectionDropped(t *testing.T) {
	essay := buildReadwiseEssay(padBodyToMin("Real body content paragraph. "), "")
	got := runPipeline(t, essay, "x.md", "metadata-drop")
	if strings.Contains(got.Doc.Body, "## Metadata") {
		t.Errorf("body should not contain '## Metadata'; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "## Full Document") {
		t.Errorf("body should not contain '## Full Document'; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "Author: x") {
		t.Errorf("metadata bullet should be dropped; got %q", got.Doc.Body)
	}
	if strings.Contains(got.Doc.Body, "URL: https://example.com") {
		t.Errorf("metadata URL bullet should be dropped; got %q", got.Doc.Body)
	}
}

// --- Operator extra: image + link + wikilink mix in one paragraph --------

func TestPipeline_ImageLinkWikilinkMix(t *testing.T) {
	prose := padBodyToMin("Mixed paragraph: ![cover](u) and [click](https://x.com) and [[wiki-target|wiki]]. ")
	essay := buildReadwiseEssay(prose, "")
	got := runPipeline(t, essay, "x.md", "mixed-noise")
	if strings.Contains(got.Doc.Body, "![") || strings.Contains(got.Doc.Body, "https://x.com") || strings.Contains(got.Doc.Body, "wiki-target") {
		t.Errorf("all three noise types should be stripped; got %q", got.Doc.Body)
	}
	if !strings.Contains(got.Doc.Body, "Mixed paragraph:") || !strings.Contains(got.Doc.Body, "click") || !strings.Contains(got.Doc.Body, "wiki") {
		t.Errorf("the human-readable text from each should survive; got %q", got.Doc.Body)
	}
}

// --- Operator extra: BOM + CRLF round-trip via ParseFile ------------------

func TestPipeline_BOMAndCRLFFromFile(t *testing.T) {
	dir := t.TempDir()
	body := buildReadwiseEssay(padBodyToMin("BOM and CRLF body content. "), "[1] Note body that is plenty long for testing.")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	bom := "\uFEFF"
	if err := os.WriteFile(dir+"/essay.md", []byte(bom+body), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseFile(dir + "/essay.md")
	if err != nil {
		t.Fatalf("ParseFile under BOM+CRLF: %v", err)
	}
	if parsed.Title != "Test Essay" {
		t.Errorf("Title under BOM+CRLF: got %q, want %q", parsed.Title, "Test Essay")
	}
	if !strings.Contains(parsed.RawBody, "BOM and CRLF body content") {
		t.Errorf("body content should survive BOM+CRLF read")
	}
}

// --- Operator extra: em-dash CharCount semantics through full pipeline ---

func TestPipeline_EmDashCharCountIsRunes(t *testing.T) {
	// Em-dash is 3 bytes in UTF-8 but 1 rune. Build a body where
	// rune count and byte count diverge meaningfully.
	prose := strings.Repeat("foo—bar baz. ", 50) // 50 em-dashes
	essay := buildReadwiseEssay(padBodyToMin(prose), "")
	got := runPipeline(t, essay, "x.md", "em-dash")
	bodyBytes := len(got.Doc.Body)
	if got.Doc.CharCount >= bodyBytes {
		t.Errorf("CharCount (%d) should be less than byte length (%d) when body has em-dashes",
			got.Doc.CharCount, bodyBytes)
	}
	if got.Doc.CharCount != utf8.RuneCountInString(got.Doc.Body) {
		t.Errorf("CharCount should equal utf8.RuneCount; got %d vs %d",
			got.Doc.CharCount, utf8.RuneCountInString(got.Doc.Body))
	}
}

// --- Operator extra: empty-body essay marks Malformed ---------------------

func TestPipeline_EmptyBodyMarksMalformed(t *testing.T) {
	// An essay whose Full Document section is short (after strip)
	// should produce Malformed=true. Use a single short paragraph
	// well below MinBodyChars (200).
	essay := buildReadwiseEssay("Short.", "")
	got := runPipeline(t, essay, "x.md", "empty")
	if !got.Doc.Malformed {
		t.Errorf("short body should be Malformed; CharCount=%d", got.Doc.CharCount)
	}
	if got.Doc.MalformedReason == "" {
		t.Errorf("MalformedReason should be set when Malformed=true")
	}
	if !strings.Contains(got.Doc.MalformedReason, "200") {
		t.Errorf("MalformedReason should reference the threshold; got %q", got.Doc.MalformedReason)
	}
}

// --- Comprehensive smoke against real example essays ----------------------

func TestPipeline_RealExampleEssays(t *testing.T) {
	cases := []struct {
		path           string
		title          string
		expectFootnote bool
	}{
		{"/data/projects/wiki-audio/examples/How to Do Great Work.md", "How to Do Great Work", true},
		{"/data/projects/wiki-audio/examples/High Agency — In 30 Minutes.md", "High Agency", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.title, func(t *testing.T) {
			if _, err := os.Stat(c.path); err != nil {
				t.Skipf("example not present at %s: %v", c.path, err)
			}
			parsed, err := ParseFile(c.path)
			if err != nil {
				t.Fatal(err)
			}
			prose, notes := SplitNotes(parsed.RawBody)
			footnotes := ParseFootnotes(notes)
			cleaned, skipped := StripMarkdown(prose)
			woven, stats := WeaveFootnotes(cleaned, footnotes)
			doc := Finalize(woven, model.EssayMeta{
				Slug:       slugify(c.title),
				Title:      parsed.Title,
				Author:     model.DefaultAuthor,
				SourcePath: c.path,
			}, skipped, FinalOpts{
				VoiceID:               "test-voice",
				ModelID:               model.DefaultModelID,
				FootnotePolicyVersion: model.FootnotePolicyVersion,
			})

			if doc.Malformed {
				t.Errorf("real essay should not be malformed; reason: %q", doc.MalformedReason)
			}
			if parsed.Title != c.title {
				t.Errorf("Title = %q; want %q", parsed.Title, c.title)
			}
			if doc.CharCount < 5000 {
				t.Errorf("CharCount suspiciously low: %d", doc.CharCount)
			}
			if len(doc.BodyHash) != 64 {
				t.Errorf("BodyHash length = %d; want 64", len(doc.BodyHash))
			}
			// Real essay bodies should never contain leftover
			// Readwise headers or inline link URLs.
			for _, leak := range []string{"## Metadata", "## Full Document", "https://paulgraham.com/", "![rw-book-cover"} {
				if strings.Contains(doc.Body, leak) {
					t.Errorf("body leaked %q", leak)
				}
			}
			if c.expectFootnote {
				if len(footnotes) == 0 {
					t.Errorf("expected at least one footnote in %s", c.title)
				}
				if !strings.Contains(doc.Body, "Footnote 1:") {
					t.Errorf("expected Footnote 1 emission in %s", c.title)
				}
			}
			t.Logf("%s: char=%d word=%d footnotes=%d skipped=%v unknown=%v unref=%v",
				c.title, doc.CharCount, doc.WordCount, len(footnotes), skipped, stats.Unknown, stats.Unreferenced)
		})
	}
}

// --- helpers --------------------------------------------------------------

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// slugify is a minimal best-effort title→slug used only for the smoke
// tests — F4 (real slug derivation) lives outside the extractor.
func slugify(title string) string {
	s := strings.ToLower(title)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "—", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
