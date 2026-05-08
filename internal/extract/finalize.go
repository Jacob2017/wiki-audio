package extract

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// blankLineRunRe matches a "blank-line run" of 2 or more consecutive
// blank lines (where a "blank line" tolerates intra-line whitespace).
// The replacement collapses every match to exactly "\n\n" — one blank
// line, matching §5.1 step 9. Single blank-line paragraph breaks
// ("\n\n" with nothing else) are NOT matched and pass through.
var blankLineRunRe = regexp.MustCompile(`\n[ \t]*\n([ \t]*\n)+`)

// CollapseBlankLines applies §5.1 step 9: any run of 2+ consecutive
// blank lines is reduced to exactly one blank line. Paragraph
// boundaries (single blank line) are preserved unchanged.
//
// Worked examples:
//
//	"para1\n\n\n\npara2"   → "para1\n\npara2"
//	"para1\n   \n   \np2"  → "para1\n\np2"
//	"para1\n\npara2"       → "para1\n\npara2"   (no change)
func CollapseBlankLines(s string) string {
	return blankLineRunRe.ReplaceAllString(s, "\n\n")
}

// FinalOpts carries the per-essay configuration that Finalize feeds
// into model.BodyHash. All fields are required; callers that have no
// principled value should use the spec-pinned defaults from package
// model (DefaultModelID, FootnotePolicyVersion).
type FinalOpts struct {
	VoiceID               string
	ModelID               string
	FootnotePolicyVersion string
}

// Finalize composes the woven prose into a populated CleanedDocument
// (§5.1 steps 9-12, wa-kyn.9). Steps:
//
//  9. CollapseBlankLines on the input.
//     Then TrimSpace so leading/trailing whitespace doesn't pollute
//     CharCount or BodyHash; the canonical document body has clean
//     boundaries.
//
//  10. Paragraph boundaries are already correct (\n\n separators);
//     this step is a documented no-op.
//
//  11. body_hash = sha256(body || voiceID || modelID ||
//     footnotePolicyVersion). Delegated to model.BodyHash so the
//     ingredient list lives in exactly one place.
//
//  12. Return the populated CleanedDocument with Meta, Body, BodyHash,
//     CharCount (utf8.RuneCountInString — matches ElevenLabs billing,
//     not byte length), WordCount (strings.Fields — whitespace-split
//     fields, Unicode-aware), and SkippedSegments. If CharCount is
//     below model.MinBodyChars (200), the document is marked
//     Malformed with a reason suitable for the §6 skipped.txt log.
//
// Finalize takes Meta and SkippedSegments from the caller because:
//   - Meta is populated by the parser (Title from Parse) plus caller-
//     supplied fields (Slug, Author — F4/F5 in wa-6la are unresolved).
//   - SkippedSegments aggregates entries from StripMarkdown (and
//     potentially other steps); the caller is the natural site for
//     that aggregation.
func Finalize(woven string, meta model.EssayMeta, skippedSegments []string, opts FinalOpts) model.CleanedDocument {
	body := strings.TrimSpace(CollapseBlankLines(woven))
	charCount := utf8.RuneCountInString(body)

	doc := model.CleanedDocument{
		Meta:            meta,
		Body:            body,
		BodyHash:        model.BodyHash(body, opts.VoiceID, opts.ModelID, opts.FootnotePolicyVersion),
		CharCount:       charCount,
		WordCount:       len(strings.Fields(body)),
		SkippedSegments: skippedSegments,
	}

	if charCount < model.MinBodyChars {
		doc.Malformed = true
		doc.MalformedReason = fmt.Sprintf(
			"body too short after extraction: %d chars (minimum %d)",
			charCount, model.MinBodyChars,
		)
	}
	return doc
}
