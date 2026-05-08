package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Smoke tests for wa-kyn.10. The full edge-case matrix (overlong
// fallback, etc.) is the province of wa-kyn.18. Tests here pin only
// what wa-kyn.10 itself owns: paragraph-bounded splits, indices, and
// rune-count CharCount.

func TestChunkEmptyBody(t *testing.T) {
	if got := Chunk("", model.DefaultChunkMaxChars); got != nil {
		t.Fatalf("empty body: got %v want nil", got)
	}
}

func TestChunkSingleShortParagraph(t *testing.T) {
	got := Chunk("Hello world.", model.DefaultChunkMaxChars)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Text != "Hello world." {
		t.Errorf("text mismatch: %q", got[0].Text)
	}
	if got[0].Index != 0 {
		t.Errorf("Index = %d, want 0", got[0].Index)
	}
	if got[0].CharCount != 12 {
		t.Errorf("CharCount = %d, want 12", got[0].CharCount)
	}
}

func TestChunkMultipleParagraphsFitInOneChunk(t *testing.T) {
	body := "Para one.\n\nPara two.\n\nPara three."
	got := Chunk(body, model.DefaultChunkMaxChars)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Text != body {
		t.Errorf("text not preserved: %q vs %q", got[0].Text, body)
	}
}

// Splitting at paragraph boundaries: each paragraph is well below the
// limit individually, but together they exceed it.
func TestChunkSplitsAtParagraphBoundaries(t *testing.T) {
	p1 := strings.Repeat("a", 100)
	p2 := strings.Repeat("b", 100)
	p3 := strings.Repeat("c", 100)
	body := p1 + "\n\n" + p2 + "\n\n" + p3
	got := Chunk(body, 220) // p1+p2 fits (200+2=202); p1+p2+p3 doesn't
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Text != p1+"\n\n"+p2 {
		t.Errorf("chunk 0 text wrong: %q", got[0].Text)
	}
	if got[1].Text != p3 {
		t.Errorf("chunk 1 text wrong: %q", got[1].Text)
	}
	if got[0].Index != 0 || got[1].Index != 1 {
		t.Errorf("indices not 0,1: %d,%d", got[0].Index, got[1].Index)
	}
}

// Each chunk must independently fit under maxChars (in bytes). This
// is the API contract — ElevenLabs will 4xx on requests over the
// limit. Excludes the wa-kyn.11 single-paragraph-too-long edge case.
func TestChunkAllChunksUnderMaxChars(t *testing.T) {
	var paras []string
	for i := 0; i < 50; i++ {
		paras = append(paras, strings.Repeat("x", 200))
	}
	body := strings.Join(paras, "\n\n")
	const maxChars = 1000
	got := Chunk(body, maxChars)
	if len(got) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(got))
	}
	for i, c := range got {
		if len(c.Text) > maxChars {
			t.Errorf("chunk %d byte len %d exceeds maxChars %d", i, len(c.Text), maxChars)
		}
	}
}

// CharCount must be a rune count, not a byte length, so wa-kyn.22's
// sum-of-chunk-chars == body-chars assertion holds on essays with
// non-ASCII characters. PG essays are full of em-dashes ("—" is 3
// bytes, 1 rune) and smart quotes.
func TestChunkCharCountIsRuneCount(t *testing.T) {
	body := "café — résumé"
	got := Chunk(body, model.DefaultChunkMaxChars)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	wantRunes := utf8.RuneCountInString(body)
	if got[0].CharCount != wantRunes {
		t.Errorf("CharCount = %d, want %d (rune count). Byte length is %d.",
			got[0].CharCount, wantRunes, len(body))
	}
	if len(body) == wantRunes {
		t.Fatalf("test fixture lost its non-ASCII characters; this test is no longer meaningful")
	}
}

// Sum of chunk rune counts equals body rune count modulo whitespace
// trimming between chunks. This is the wa-kyn.22
// total_chunk_chars_match_body invariant. Use a body where every
// chunk ends on a paragraph boundary to keep accounting simple.
func TestChunkTotalRunesMatchBody(t *testing.T) {
	// 3 paragraphs of 100 chars each, separated by "\n\n".
	p := strings.Repeat("a", 100)
	body := p + "\n\n" + p + "\n\n" + p
	const maxChars = 220 // splits after 2 paragraphs
	got := Chunk(body, maxChars)
	var total int
	for _, c := range got {
		total += c.CharCount
	}
	// Each chunk's TrimSpace removes the trailing "\n\n" between
	// chunks. The first chunk has 100+2+100 = 202 runes (p\n\np).
	// The second chunk has 100 runes. Total = 302.
	// Body rune count = 100+2+100+2+100 = 304, minus the 2 runes
	// trimmed between chunks = 302.
	want := utf8.RuneCountInString(body) - 2*(len(got)-1)
	if total != want {
		t.Errorf("sum of chunk CharCount = %d, want %d (body runes %d, %d boundaries trimmed)",
			total, want, utf8.RuneCountInString(body), len(got)-1)
	}
}

// Trim semantics: chunk text never has leading or trailing
// whitespace. wa-kyn.22 uses a heuristic that chunks "start with
// capital or quote"; whitespace-stripped chunks are a precondition.
func TestChunkTextHasNoLeadingOrTrailingWhitespace(t *testing.T) {
	body := "Para one.\n\nPara two.\n\nPara three."
	got := Chunk(body, 25) // forces splits
	for i, c := range got {
		if c.Text != strings.TrimSpace(c.Text) {
			t.Errorf("chunk %d has untrimmed whitespace: %q", i, c.Text)
		}
	}
}

// Whitespace-only paragraphs (degenerate input — shouldn't happen
// after the extractor's blank-line collapse, but defensive) must not
// produce empty chunks.
func TestChunkSkipsEmptyParagraphs(t *testing.T) {
	body := "real para.\n\n\n\nanother real para."
	got := Chunk(body, model.DefaultChunkMaxChars)
	for i, c := range got {
		if strings.TrimSpace(c.Text) == "" {
			t.Errorf("chunk %d is empty/whitespace: %q", i, c.Text)
		}
	}
}

func TestChunkInvalidMaxCharsReturnsNil(t *testing.T) {
	for _, m := range []int{0, -1, -100} {
		if got := Chunk("body", m); got != nil {
			t.Errorf("maxChars=%d: got %v, want nil", m, got)
		}
	}
}
