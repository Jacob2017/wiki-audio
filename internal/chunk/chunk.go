package chunk

import (
	"strings"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Chunk splits a cleaned essay body into paragraph-bounded
// AudioChunks per PLAN §5.2.
//
// The algorithm is greedy: walk paragraphs, accumulate until adding
// the next paragraph (plus its trailing "\n\n") would exceed maxChars
// in bytes, then flush. Paragraph boundaries are honored — chunks
// never start or end mid-paragraph. The +2 in the size budget
// accounts for the "\n\n" separator we append after every paragraph
// inside cur; the final "\n\n" gets trimmed by strings.TrimSpace, so
// chunks land slightly under the budget in practice.
//
// AudioChunk.CharCount is a rune count (utf8.RuneCountInString) to
// match model.CleanedDocument.CharCount semantics. ElevenLabs bills
// per rune; reporting runes here keeps wa-kyn.22's
// total_chunk_chars_match_body assertion correct on essays with
// em-dashes or smart quotes. Internal budgeting uses bytes (cheaper
// and more conservative — UTF-8 bytes ≥ runes, so byte-budgeted
// chunks always fit ElevenLabs' rune-counted limit).
//
// Edge case: a single paragraph longer than maxChars produces a
// chunk that exceeds the budget. wa-kyn.11 implements the
// sentence-style fallback; wa-kyn.10 deliberately does not handle
// it. The chunker emits the over-long chunk; the fallback can be
// applied as a post-pass once it lands.
//
// Empty body returns nil. Whitespace-only paragraphs (which shouldn't
// occur after the extractor's blank-line collapse, but might in
// pathological input) are skipped rather than emitted as empty
// chunks — strings.Split would otherwise create one for a single
// trailing "\n\n".
func Chunk(body string, maxChars int) []model.AudioChunk {
	if body == "" || maxChars <= 0 {
		return nil
	}

	paragraphs := strings.Split(body, "\n\n")
	var chunks []string
	var cur strings.Builder

	for _, p := range paragraphs {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if cur.Len()+len(p)+2 > maxChars && cur.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		cur.WriteString(p)
		cur.WriteString("\n\n")
	}
	if cur.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(cur.String()))
	}

	out := make([]model.AudioChunk, len(chunks))
	for i, c := range chunks {
		out[i] = model.AudioChunk{
			Index:     i,
			Text:      c,
			CharCount: utf8.RuneCountInString(c),
		}
	}
	return out
}
