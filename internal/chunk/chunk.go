package chunk

import (
	"log/slog"
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
// never start or end mid-paragraph in the common case. The +2 in the
// size budget accounts for the "\n\n" separator we append after every
// paragraph inside cur; the final "\n\n" gets trimmed by
// strings.TrimSpace, so chunks land slightly under the budget in
// practice.
//
// AudioChunk.CharCount is a rune count (utf8.RuneCountInString) to
// match model.CleanedDocument.CharCount semantics. ElevenLabs bills
// per rune; reporting runes here keeps wa-kyn.22's
// total_chunk_chars_match_body assertion correct on essays with
// em-dashes or smart quotes. Internal budgeting uses bytes (cheaper
// and more conservative — UTF-8 bytes ≥ runes, so byte-budgeted
// chunks always fit ElevenLabs' rune-counted limit).
//
// Overlong paragraphs (len(p) > maxChars) trigger the sentence-style
// fallback per wa-kyn.11: the paragraph is sentence-split via
// regroupOverlongParagraph and each group is treated as a paragraph
// for chunking. A single sentence longer than maxChars is still
// emitted as an overlong chunk — the bead accepts the imperfection.
// A warning is logged once per overlong paragraph so corpus auditing
// can find the source markdown that needs a manual paragraph break.
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
		units := []string{p}
		if len(p) > maxChars {
			slog.Warn("chunk: paragraph exceeds max chars; falling back to sentence-split (wa-kyn.11)",
				"paragraph_chars", len(p),
				"max_chars", maxChars,
				"paragraph_prefix", paragraphPrefix(p))
			units = regroupOverlongParagraph(p, maxChars)
		}
		for _, u := range units {
			if cur.Len()+len(u)+2 > maxChars && cur.Len() > 0 {
				chunks = append(chunks, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			cur.WriteString(u)
			cur.WriteString("\n\n")
		}
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

// paragraphPrefix returns up to the first 60 bytes of p, ASCII-safe,
// for inclusion in the overlong-paragraph warning. Helps a human
// auditing logs find the offending markdown in the source corpus
// without dumping the whole 4KB+ paragraph into structured logs.
func paragraphPrefix(p string) string {
	const limit = 60
	if len(p) <= limit {
		return p
	}
	// Trim at a UTF-8 boundary by walking back if we landed mid-rune.
	cut := limit
	for cut > 0 && (p[cut]&0xC0) == 0x80 {
		cut--
	}
	return p[:cut] + "…"
}
