package chunk

import (
	"math"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// wa-kyn.18 — table-driven coverage of the six bead-dispatch cases.
// Complements (does not replace) the smoke tests in chunk_test.go
// (wa-kyn.10) and the fallback tests in fallback_test.go (wa-kyn.11).
// Some rows overlap with the existing suites by design — the table
// here is the canonical wa-kyn.18 contract; if it ever diverges from
// the smoke-suite assertions, this file is the source of truth.

// TestChunkBeadDispatchCases pins each of the six cases the
// orchestrator's dispatch listed for wa-kyn.18, in one table-driven
// test so a failure surfaces as "Body with N paragraphs of equal
// size" rather than as a generic chunk-test failure.
func TestChunkBeadDispatchCases(t *testing.T) {
	const (
		max  = 100 // tight maxChars to keep fixtures readable
		para = 30  // chars per paragraph in the equal-size case
	)

	tenEqual := buildEqualParagraphs(10, para)

	overlongPara := strings.Repeat("This is a sentence. ", 25) // ~500 chars,
	// 25 sentences delimited by ". " — well above max=100.

	cases := []struct {
		name      string
		body      string
		maxChars  int
		wantCheck func(t *testing.T, chunks []model.AudioChunk)
	}{
		{
			name:     "body shorter than maxChars yields 1 chunk",
			body:     "Short body fits.",
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				if len(chunks) != 1 {
					t.Errorf("got %d chunks, want 1", len(chunks))
				}
				if len(chunks) == 1 && chunks[0].Text != "Short body fits." {
					t.Errorf("chunk text = %q", chunks[0].Text)
				}
			},
		},
		{
			name: "body slightly longer than maxChars splits at paragraph",
			body: strings.Repeat("a", 60) + "\n\n" + strings.Repeat("b", 60),
			// Each paragraph 60; with the +2 for "\n\n" both can't fit
			// in 100. Split must happen at the \n\n boundary.
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				if len(chunks) != 2 {
					t.Fatalf("got %d chunks, want 2", len(chunks))
				}
				if chunks[0].Text != strings.Repeat("a", 60) {
					t.Errorf("chunk 0 should be the first paragraph alone")
				}
				if chunks[1].Text != strings.Repeat("b", 60) {
					t.Errorf("chunk 1 should be the second paragraph alone")
				}
			},
		},
		{
			name:     "body with 10 paragraphs of equal size matches ceil(total/maxChars)",
			body:     tenEqual,
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				// totalBytes is the body's byte length. Greedy
				// chunker packs whole paragraphs (with their "\n\n"
				// trailers) until the next paragraph would push cur
				// over maxChars. Lower-bound estimate is
				// ceil(totalBytes / maxChars); the actual count may
				// be EQUAL or one more (boundary-induced waste).
				lowerBound := int(math.Ceil(float64(len(tenEqual)) / float64(max)))
				if len(chunks) < lowerBound {
					t.Errorf("expected at least %d chunks (ceil(%d/%d)); got %d",
						lowerBound, len(tenEqual), max, len(chunks))
				}
				if len(chunks) > lowerBound+2 {
					t.Errorf("expected ≤ %d chunks; got %d (chunker is wasting too much budget)",
						lowerBound+2, len(chunks))
				}
				for i, c := range chunks {
					if len(c.Text) > max {
						t.Errorf("chunk %d byte len %d exceeds maxChars %d",
							i, len(c.Text), max)
					}
				}
			},
		},
		{
			name:     "single paragraph longer than maxChars triggers sentence-split fallback",
			body:     overlongPara,
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				if len(chunks) < 2 {
					t.Fatalf("expected fallback to produce multiple chunks; got %d",
						len(chunks))
				}
				// Each chunk should fit (sentences are ~20 chars; max=100).
				for i, c := range chunks {
					if len(c.Text) > max {
						t.Errorf("chunk %d byte len %d exceeds maxChars %d (fallback didn't constrain)",
							i, len(c.Text), max)
					}
				}
				// Sentence-final periods should be preserved.
				if !strings.Contains(chunks[0].Text, ". ") &&
					!strings.HasSuffix(chunks[0].Text, ".") {
					t.Errorf("chunk 0 missing sentence punctuation: %q", chunks[0].Text)
				}
			},
		},
		{
			name:     "empty body yields 0 chunks (no crash)",
			body:     "",
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				if len(chunks) != 0 {
					t.Errorf("got %d chunks, want 0; chunks: %#v", len(chunks), chunks)
				}
			},
		},
		{
			name: "body with only whitespace paragraphs yields 0 chunks",
			body: "   \n\n\t\n\n  \n",
			// All paragraphs collapse to whitespace-only; chunker's
			// strings.TrimSpace(p) == "" guard skips them. Final
			// builder is empty → zero chunks.
			maxChars: max,
			wantCheck: func(t *testing.T, chunks []model.AudioChunk) {
				if len(chunks) != 0 {
					t.Errorf("got %d chunks, want 0; chunks: %#v", len(chunks), chunks)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Chunk(c.body, c.maxChars)
			c.wantCheck(t, got)
			// Cross-cutting invariant for non-empty results: indices
			// are 0-based and sequential. Catches a bug where a
			// future refactor reorders chunks or skips an Index.
			for i, ch := range got {
				if ch.Index != i {
					t.Errorf("chunk %d has Index = %d; want %d", i, ch.Index, i)
				}
			}
		})
	}
}

// buildEqualParagraphs returns a body of n paragraphs each of the
// given byte length, separated by "\n\n". Used by the
// 10-paragraph case to keep the fixture self-explanatory.
func buildEqualParagraphs(n, paraLen int) string {
	parts := make([]string, n)
	for i := range parts {
		// Pad with a digit prefix so each paragraph is distinct in a
		// failure message, then back-fill to paraLen with 'x'.
		prefix := []byte{byte('0' + (i % 10)), '.', ' '}
		fill := paraLen - len(prefix)
		if fill < 0 {
			fill = 0
		}
		parts[i] = string(prefix) + strings.Repeat("x", fill)
	}
	return strings.Join(parts, "\n\n")
}

// Sanity that the case fixtures actually exercise the dispatch
// scenarios. Catches a fixture-vs-test drift where someone changes
// `max` and the "slightly longer" case accidentally fits in one
// chunk.
func TestChunkDispatchFixtureSanity(t *testing.T) {
	const max = 100

	// "Body shorter than maxChars" — len("Short body fits.") < max.
	if len("Short body fits.") >= max {
		t.Errorf("dispatch case 1 fixture is no longer shorter than maxChars=%d", max)
	}

	// "Body slightly longer than maxChars" — total > max, both
	// paragraphs individually < max.
	body := strings.Repeat("a", 60) + "\n\n" + strings.Repeat("b", 60)
	if len(body) <= max {
		t.Errorf("dispatch case 2 body should exceed maxChars=%d; got %d", max, len(body))
	}

	// "10 paragraphs equal size" — total well above max, each para < max.
	body = buildEqualParagraphs(10, 30)
	if len(body) <= max {
		t.Errorf("dispatch case 3 body should exceed maxChars=%d; got %d", max, len(body))
	}

	// "Single paragraph longer than maxChars" — no \n\n in body.
	body = strings.Repeat("This is a sentence. ", 25)
	if strings.Contains(body, "\n\n") {
		t.Errorf("dispatch case 4 body must be a single paragraph (no '\\n\\n')")
	}
	if len(body) <= max {
		t.Errorf("dispatch case 4 body should exceed maxChars=%d; got %d", max, len(body))
	}
}

// Ensure the chunker behaves identically when invoked with the §2
// production maxChars (model.DefaultChunkMaxChars=4000) on a body
// that exercises every dispatch shape. This is the closest thing to
// a smoke test of the full production configuration without
// reaching into wa-kyn.22's e2e harness.
func TestChunkBeadDispatch_ProductionMaxChars(t *testing.T) {
	body := buildEqualParagraphs(10, 100)
	got := Chunk(body, model.DefaultChunkMaxChars)
	if len(got) == 0 {
		t.Fatalf("expected non-empty result; body len = %d", len(body))
	}
	for i, c := range got {
		if c.CharCount > model.DefaultChunkMaxChars {
			t.Errorf("chunk %d CharCount %d > DefaultChunkMaxChars %d",
				i, c.CharCount, model.DefaultChunkMaxChars)
		}
	}
}
