package chunk

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// regroupOverlongParagraph tests — the helper is package-private but
// the unit tests live in the same package, so direct calls are fine.

func TestRegroupSplitsAtSentenceBoundaries(t *testing.T) {
	// Three sentences of ~30 chars each → maxChars=70 packs 2 per
	// group.
	p := "First sentence here. Second sentence here. Third sentence end."
	got := regroupOverlongParagraph(p, 50)
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2: %#v", len(got), got)
	}
	if got[0] != "First sentence here. Second sentence here." {
		t.Errorf("group 0: %q", got[0])
	}
	if got[1] != "Third sentence end." {
		t.Errorf("group 1: %q", got[1])
	}
}

func TestRegroupKeepsPunctuationWithSentence(t *testing.T) {
	// Each sentence's terminal punctuation (.,!,?) must stay
	// attached. Otherwise TTS rendering loses prosody.
	p := "Wait! Really? Yes."
	got := regroupOverlongParagraph(p, 8) // forces every sentence into its own group
	if len(got) != 3 {
		t.Fatalf("got %d groups, want 3: %#v", len(got), got)
	}
	wantSuffixes := []string{"!", "?", "."}
	for i, g := range got {
		if !strings.HasSuffix(g, wantSuffixes[i]) {
			t.Errorf("group %d %q lost terminal punctuation", i, g)
		}
	}
}

// Sentence boundaries require an uppercase letter to follow — that
// constraint emulates the regex's `(?=[A-Z])` lookahead in Go's RE2.
// "i.e." style abbreviations should NOT trip the splitter because
// they're followed by lowercase.
func TestRegroupRespectsUppercaseLookahead(t *testing.T) {
	p := "The point is e.g. an abbreviation. Another sentence here."
	got := regroupOverlongParagraph(p, 10) // tiny to force splits if any boundary exists
	// Only one true boundary — between "abbreviation." and "Another".
	// "e.g." is followed by lowercase 'a' so it must NOT split.
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2 (e.g. shouldn't split): %#v", len(got), got)
	}
	if !strings.Contains(got[0], "e.g. an abbreviation.") {
		t.Errorf("e.g. boundary was incorrectly split: group 0 = %q", got[0])
	}
}

// A paragraph with no sentence boundaries (one giant run-on) returns
// itself as a single overlong group.
func TestRegroupNoBoundaryReturnsWhole(t *testing.T) {
	p := strings.Repeat("a", 5000) + " trailing"
	got := regroupOverlongParagraph(p, 4000)
	if len(got) != 1 {
		t.Fatalf("got %d groups, want 1: lengths %v", len(got), groupLens(got))
	}
	if got[0] != p {
		t.Errorf("group not equal to input")
	}
}

// A single sentence longer than maxChars is emitted as its own group.
// Bead acceptance: "the fix is to manually break the paragraph in
// the markdown source."
func TestRegroupSingleOversizedSentence(t *testing.T) {
	huge := strings.Repeat("a", 5000)
	p := huge + ". Short tail."
	got := regroupOverlongParagraph(p, 1000)
	if len(got) != 2 {
		t.Fatalf("got %d groups, want 2: lengths %v", len(got), groupLens(got))
	}
	if len(got[0]) <= 1000 {
		t.Errorf("expected first group > maxChars (oversized sentence), got len %d", len(got[0]))
	}
	if got[1] != "Short tail." {
		t.Errorf("group 1: %q", got[1])
	}
}

// Chunk() integration: a paragraph longer than maxChars triggers the
// fallback and produces multiple chunks each ≤ maxChars (assuming
// individual sentences fit).
func TestChunkOverlongParagraphTriggersFallback(t *testing.T) {
	// Construct a 1200-char paragraph from many short sentences.
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("Sentence ")
		sb.WriteString(strings.Repeat("x", 30))
		sb.WriteString(". ")
	}
	body := sb.String()
	if len(body) <= 500 {
		t.Fatalf("test fixture too short")
	}
	got := Chunk(body, 500)
	if len(got) < 2 {
		t.Fatalf("expected fallback to produce multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if len(c.Text) > 500 {
			t.Errorf("chunk %d byte len %d exceeds maxChars 500 — fallback didn't constrain it", i, len(c.Text))
		}
	}
}

// The fallback must not be triggered for short paragraphs. This
// guards against a regression where the helper accidentally runs on
// every paragraph and reformats prose that didn't need it.
func TestChunkShortParagraphsBypassFallback(t *testing.T) {
	body := "Short. Another. Done."
	got := Chunk(body, 4000)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	// Original prose preserved (single space between sentences,
	// terminal periods kept). If fallback ran on this, internal
	// joins might shift to `\n\n` between sentences within a group
	// — they shouldn't.
	if got[0].Text != body {
		t.Errorf("short paragraph got reformatted: %q", got[0].Text)
	}
}

// Total content preserved: the fallback shouldn't drop any content.
// Walk every rune of the input through the chunks (modulo whitespace
// boundaries) and verify nothing was eaten by the regex split.
func TestChunkOverlongPreservesContentRunes(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("Another sentence with content. ")
	}
	body := strings.TrimSpace(sb.String())

	got := Chunk(body, 200)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}

	// Strip ALL whitespace from input and from concatenated chunks;
	// they must match. This is a content-preservation invariant
	// that ignores paragraph/sentence boundary differences.
	wantNoWs := stripAllWhitespace(body)
	var sbGot strings.Builder
	for _, c := range got {
		sbGot.WriteString(c.Text)
	}
	gotNoWs := stripAllWhitespace(sbGot.String())
	if wantNoWs != gotNoWs {
		t.Errorf("content lost: input ws-stripped len %d vs chunks ws-stripped len %d",
			utf8.RuneCountInString(wantNoWs), utf8.RuneCountInString(gotNoWs))
	}
}

// The bead requires a warning log when fallback trips. Capture
// slog output and assert the expected key is present.
func TestChunkOverlongLogsWarning(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))

	body := strings.Repeat("a", 5000) + ". short tail."
	_ = Chunk(body, 1000)

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("expected WARN level log; got: %s", out)
	}
	if !strings.Contains(out, "paragraph_chars=") {
		t.Errorf("expected paragraph_chars attr; got: %s", out)
	}
	if !strings.Contains(out, "max_chars=1000") {
		t.Errorf("expected max_chars=1000 attr; got: %s", out)
	}
}

// Short paragraphs MUST NOT log a warning, otherwise every build
// would spam the log.
func TestChunkShortParagraphsDoNotLog(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))

	_ = Chunk("Short paragraph.\n\nAnother short paragraph.", 4000)

	if buf.Len() != 0 {
		t.Errorf("expected zero log output for short paragraphs; got: %s", buf.String())
	}
}

func TestParagraphPrefixTruncates(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := paragraphPrefix(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected truncation marker; got %q", got)
	}
	if len(got) > 70 {
		t.Errorf("prefix too long: %d bytes", len(got))
	}
}

func TestParagraphPrefixShortReturnsAsIs(t *testing.T) {
	short := "hello"
	if got := paragraphPrefix(short); got != short {
		t.Errorf("got %q, want %q", got, short)
	}
}

// paragraphPrefix must not slice mid-rune when the limit lands inside
// a multi-byte UTF-8 sequence. Verify the result is valid UTF-8.
func TestParagraphPrefixUTF8Safe(t *testing.T) {
	// 30 em-dashes (3 bytes each in UTF-8) = 90 bytes. The 60-byte
	// limit would land mid-rune at byte 60 of em-dash #21.
	long := strings.Repeat("—", 30)
	got := paragraphPrefix(long)
	// Strip the trailing "…" before validating.
	body := strings.TrimSuffix(got, "…")
	if !utf8.ValidString(body) {
		t.Errorf("prefix sliced mid-rune: %q (%d bytes)", body, len(body))
	}
}

// helpers

func groupLens(gs []string) []int {
	out := make([]int, len(gs))
	for i, g := range gs {
		out[i] = len(g)
	}
	return out
}

func stripAllWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
