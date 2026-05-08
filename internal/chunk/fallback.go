package chunk

import (
	"regexp"
	"strings"
)

// sentenceBoundary matches a sentence-ending punctuation followed by
// run of ASCII whitespace. PLAN §5.2 step 5 specifies the regex
// `[.!?]\s+(?=[A-Z])` — Go's RE2 has no lookahead, so the
// "next character is uppercase" constraint is enforced separately by
// regroupOverlongParagraph after FindAllStringIndex returns.
//
// The regex is deliberately imperfect (won't handle "Mr. Smith",
// abbreviations, mid-sentence quoted speech) — the bead accepts the
// imperfection because (a) this code path is hit rarely (PG essays
// rarely produce paragraphs > 4000 chars) and (b) building a real
// sentence segmenter for one edge case is wildly disproportionate.
// If we ever hit a tricky overlong paragraph in the corpus, the fix
// is to manually break the paragraph in the markdown source.
var sentenceBoundary = regexp.MustCompile(`[.!?]\s+`)

// regroupOverlongParagraph splits a paragraph longer than maxChars
// into sub-paragraphs at sentence boundaries, greedy-packing
// sentences into groups of ≤ maxChars where possible. A single
// sentence longer than maxChars is emitted as its own (still
// overlong) sub-paragraph; the caller surfaces it as an overlong
// chunk and the bead accepts the imperfection.
//
// Within a group, sentences are joined with a single space — the
// natural separator in prose. Between groups (i.e. across chunk
// boundaries), the chunker uses "\n\n" as it would for any
// paragraph break, producing a slightly longer pause where the
// listener would have heard a sentence boundary in the source. Per
// the bead, that's acceptable: "the listener's experience already
// has a mid-paragraph break either way."
func regroupOverlongParagraph(p string, maxChars int) []string {
	matches := sentenceBoundary.FindAllStringIndex(p, -1)

	// Filter for the lookahead constraint that Go regexp can't
	// express directly: the next character after the matched
	// punctuation+whitespace must be ASCII uppercase. Each match m
	// is [start_of_punct, end_of_whitespace]. The punctuation
	// itself is exactly one byte (`.`, `!`, or `?` — all ASCII), so
	// puncEnd = m[0] + 1 marks where the trailing whitespace
	// begins.
	type boundary struct{ puncEnd, whitespaceEnd int }
	var bounds []boundary
	for _, m := range matches {
		if m[1] >= len(p) {
			continue
		}
		next := p[m[1]]
		if next < 'A' || next > 'Z' {
			continue
		}
		bounds = append(bounds, boundary{puncEnd: m[0] + 1, whitespaceEnd: m[1]})
	}
	if len(bounds) == 0 {
		// No usable sentence boundaries — caller will emit p as a
		// single overlong unit. The warning was already logged at
		// Chunk()'s call site.
		return []string{p}
	}

	var sentences []string
	start := 0
	for _, b := range bounds {
		sentences = append(sentences, p[start:b.puncEnd])
		start = b.whitespaceEnd
	}
	sentences = append(sentences, p[start:])

	var groups []string
	var cur strings.Builder
	for _, s := range sentences {
		if cur.Len() == 0 {
			cur.WriteString(s)
			continue
		}
		// Adding " " + s would push us over budget — flush.
		if cur.Len()+1+len(s) > maxChars {
			groups = append(groups, cur.String())
			cur.Reset()
			cur.WriteString(s)
			continue
		}
		cur.WriteString(" ")
		cur.WriteString(s)
	}
	if cur.Len() > 0 {
		groups = append(groups, cur.String())
	}
	return groups
}
