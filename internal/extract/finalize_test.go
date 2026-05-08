package extract

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// --- CollapseBlankLines (§5.1 step 9) ------------------------------------

func TestCollapseBlankLines_DoubleBlank(t *testing.T) {
	got := CollapseBlankLines("para1\n\n\n\npara2")
	want := "para1\n\npara2"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCollapseBlankLines_TripleBlank(t *testing.T) {
	got := CollapseBlankLines("p1\n\n\n\n\np2")
	want := "p1\n\np2"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCollapseBlankLines_SingleBlankPreserved(t *testing.T) {
	in := "para1\n\npara2"
	got := CollapseBlankLines(in)
	if got != in {
		t.Errorf("single blank line should not change: got %q want %q", got, in)
	}
}

func TestCollapseBlankLines_WhitespaceOnlyBlanks(t *testing.T) {
	got := CollapseBlankLines("para1\n   \n\t\npara2")
	want := "para1\n\npara2"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCollapseBlankLines_MultipleRuns(t *testing.T) {
	in := "p1\n\n\n\np2\n\n\n\n\np3\n\np4"
	got := CollapseBlankLines(in)
	want := "p1\n\np2\n\np3\n\np4"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestCollapseBlankLines_NoBlankLines(t *testing.T) {
	in := "single line content"
	got := CollapseBlankLines(in)
	if got != in {
		t.Errorf("got %q; want %q", got, in)
	}
}

func TestCollapseBlankLines_Empty(t *testing.T) {
	if got := CollapseBlankLines(""); got != "" {
		t.Errorf("got %q; want empty", got)
	}
}

// --- Finalize hash properties (§5.1 step 11) -----------------------------

func defaultFinalOpts() FinalOpts {
	return FinalOpts{
		VoiceID:               "test-voice",
		ModelID:               model.DefaultModelID,
		FootnotePolicyVersion: model.FootnotePolicyVersion,
	}
}

func longBody(n int) string {
	// Generate a deterministic long body for hash/length tests.
	var b strings.Builder
	for b.Len() < n {
		b.WriteString("lorem ipsum dolor sit amet consectetur adipiscing elit. ")
	}
	return strings.TrimSpace(b.String()[:n])
}

func TestFinalize_BodyHashStable(t *testing.T) {
	body := longBody(500)
	d1 := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	d2 := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if d1.BodyHash != d2.BodyHash {
		t.Errorf("BodyHash should be stable across runs; got %q vs %q", d1.BodyHash, d2.BodyHash)
	}
}

func TestFinalize_BodyHashIncludesVoiceID(t *testing.T) {
	body := longBody(500)
	a := defaultFinalOpts()
	b := a
	b.VoiceID = "different-voice"
	if Finalize(body, model.EssayMeta{}, nil, a).BodyHash == Finalize(body, model.EssayMeta{}, nil, b).BodyHash {
		t.Error("changing VoiceID should change BodyHash")
	}
}

func TestFinalize_BodyHashIncludesModelID(t *testing.T) {
	body := longBody(500)
	a := defaultFinalOpts()
	b := a
	b.ModelID = "different-model"
	if Finalize(body, model.EssayMeta{}, nil, a).BodyHash == Finalize(body, model.EssayMeta{}, nil, b).BodyHash {
		t.Error("changing ModelID should change BodyHash")
	}
}

func TestFinalize_BodyHashIncludesFootnotePolicyVersion(t *testing.T) {
	body := longBody(500)
	a := defaultFinalOpts()
	b := a
	b.FootnotePolicyVersion = "v9999"
	if Finalize(body, model.EssayMeta{}, nil, a).BodyHash == Finalize(body, model.EssayMeta{}, nil, b).BodyHash {
		t.Error("changing FootnotePolicyVersion should change BodyHash")
	}
}

func TestFinalize_BodyHashFormatIsHex64(t *testing.T) {
	hashRe := regexp.MustCompile(`^[a-f0-9]{64}$`)
	for _, in := range []string{"a", longBody(50), longBody(500), longBody(5000)} {
		got := Finalize(in, model.EssayMeta{}, nil, defaultFinalOpts())
		if !hashRe.MatchString(got.BodyHash) {
			t.Errorf("BodyHash for input len=%d not 64-char lowercase hex: %q", len(in), got.BodyHash)
		}
	}
}

// --- Finalize malformed boundary (§5.1 step 12 + §6 sanity) --------------

func TestFinalize_BodyUnder200CharsMarksMalformed(t *testing.T) {
	body := longBody(150)
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if !doc.Malformed {
		t.Errorf("body of %d chars should be Malformed (threshold %d)", utf8.RuneCountInString(body), model.MinBodyChars)
	}
	if doc.MalformedReason == "" {
		t.Error("MalformedReason should be set for short body")
	}
	if !strings.Contains(doc.MalformedReason, "150") {
		t.Errorf("MalformedReason should report actual char count; got %q", doc.MalformedReason)
	}
}

func TestFinalize_BodyAt200CharsNotMalformed(t *testing.T) {
	body := longBody(200)
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if doc.Malformed {
		t.Errorf("body of exactly %d chars should NOT be Malformed", model.MinBodyChars)
	}
	if doc.MalformedReason != "" {
		t.Errorf("MalformedReason should be empty; got %q", doc.MalformedReason)
	}
}

func TestFinalize_BodyOver200CharsNotMalformed(t *testing.T) {
	body := longBody(201)
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if doc.Malformed {
		t.Error("body of 201 chars should NOT be Malformed")
	}
}

// --- Finalize counts -----------------------------------------------------

func TestFinalize_CharCountIsRunesNotBytes(t *testing.T) {
	// Em-dash "—" is 3 bytes in UTF-8 but one rune. Pad to >= 200
	// chars so we don't trip the malformed gate.
	body := strings.Repeat("a—b ", 60) // 60 * 4 runes = 240 runes, 60 * 6 = 360 bytes
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	wantRunes := utf8.RuneCountInString(strings.TrimSpace(body))
	if doc.CharCount != wantRunes {
		t.Errorf("CharCount = %d; want utf8.RuneCount = %d", doc.CharCount, wantRunes)
	}
	if doc.CharCount == len(strings.TrimSpace(body)) {
		t.Errorf("CharCount must be RUNES not bytes — they should differ for this input (%d bytes)", len(body))
	}
}

func TestFinalize_WordCountSimpleSplit(t *testing.T) {
	// strings.Fields is whitespace-split, Unicode-aware. "hello world foo"
	// is 3 fields. Pad with filler so we clear MinBodyChars.
	filler := longBody(200)
	body := "hello world foo " + filler
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	wantWords := len(strings.Fields(strings.TrimSpace(body)))
	if doc.WordCount != wantWords {
		t.Errorf("WordCount = %d; want %d", doc.WordCount, wantWords)
	}
}

// --- Finalize aggregates SkippedSegments verbatim ------------------------

func TestFinalize_SkippedSegmentsAggregated(t *testing.T) {
	body := longBody(300)
	skipped := []string{"code_block:python", "image"}
	doc := Finalize(body, model.EssayMeta{}, skipped, defaultFinalOpts())
	if !reflect.DeepEqual(doc.SkippedSegments, skipped) {
		t.Errorf("SkippedSegments = %v; want %v", doc.SkippedSegments, skipped)
	}
}

func TestFinalize_NilSkippedAcceptedAsEmpty(t *testing.T) {
	body := longBody(300)
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if doc.SkippedSegments != nil && len(doc.SkippedSegments) != 0 {
		t.Errorf("nil skipped should pass through; got %v", doc.SkippedSegments)
	}
}

// --- Finalize meta passthrough -------------------------------------------

func TestFinalize_MetaPopulated(t *testing.T) {
	body := longBody(300)
	meta := model.EssayMeta{
		Slug:       "how-to-do-great-work",
		Title:      "How to Do Great Work",
		Author:     "Paul Graham",
		SourcePath: "/path/to/essay.md",
	}
	doc := Finalize(body, meta, nil, defaultFinalOpts())
	if doc.Meta != meta {
		t.Errorf("Meta should pass through unchanged: got %+v; want %+v", doc.Meta, meta)
	}
}

// --- Finalize trim semantics ---------------------------------------------

func TestFinalize_BodyTrimsLeadingTrailingWhitespace(t *testing.T) {
	body := "\n\n   " + longBody(300) + "   \n\n"
	doc := Finalize(body, model.EssayMeta{}, nil, defaultFinalOpts())
	if strings.HasPrefix(doc.Body, "\n") || strings.HasPrefix(doc.Body, " ") {
		t.Errorf("body should be left-trimmed; got prefix %q", firstChars(doc.Body, 10))
	}
	if strings.HasSuffix(doc.Body, "\n") || strings.HasSuffix(doc.Body, " ") {
		t.Errorf("body should be right-trimmed; got suffix %q", lastChars(doc.Body, 10))
	}
}

func TestFinalize_CollapsesBeforeHashing(t *testing.T) {
	// Two inputs that differ only in blank-line runs should hash
	// identically once Finalize collapses them.
	a := Finalize("para1\n\n\n\npara2 "+longBody(200), model.EssayMeta{}, nil, defaultFinalOpts())
	b := Finalize("para1\n\npara2 "+longBody(200), model.EssayMeta{}, nil, defaultFinalOpts())
	if a.BodyHash != b.BodyHash {
		t.Errorf("blank-line collapse should canonicalize before hashing; got %q vs %q", a.BodyHash, b.BodyHash)
	}
}

// --- Full pipeline integration -------------------------------------------

func TestFinalize_FullPipeline(t *testing.T) {
	const essay = `# Synthetic Essay

## Metadata
- Author: x

## Full Document
First paragraph references [1].



(Triple blank lines should collapse to one.)

## Notes

[1] Note body that is long enough.
` + ""
	parsed, err := Parse(essay+"\n"+strings.Repeat("filler word ", 50), "synthetic.md")
	if err != nil {
		t.Fatal(err)
	}
	prose, notesPart := SplitNotes(parsed.RawBody)
	footnotes := ParseFootnotes(notesPart)
	cleaned, skipped := StripMarkdown(prose)
	woven, _ := WeaveFootnotes(cleaned, footnotes)
	doc := Finalize(woven, model.EssayMeta{
		Slug:       "synthetic",
		Title:      parsed.Title,
		Author:     "x",
		SourcePath: "/synthetic.md",
	}, skipped, defaultFinalOpts())

	if doc.Malformed {
		t.Errorf("synthetic essay should not be malformed; reason: %q", doc.MalformedReason)
	}
	if doc.Meta.Title != "Synthetic Essay" {
		t.Errorf("Meta.Title = %q; want %q", doc.Meta.Title, "Synthetic Essay")
	}
	if doc.CharCount == 0 {
		t.Error("CharCount should be > 0")
	}
	if doc.WordCount == 0 {
		t.Error("WordCount should be > 0")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(doc.BodyHash) {
		t.Errorf("BodyHash format wrong: %q", doc.BodyHash)
	}
	if strings.Contains(doc.Body, "\n\n\n") {
		t.Errorf("Body should have no triple-newline runs; got %q", doc.Body)
	}
	if !strings.Contains(doc.Body, "Footnote 1: Note body that is long enough.") {
		t.Errorf("Footnote line should be present in Body; got %q", doc.Body)
	}
}

func firstChars(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func lastChars(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[len(s)-n:]
}
