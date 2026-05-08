package extract

import (
	"regexp"
	"strings"
)

var (
	// imageRe matches "![alt](url)" non-greedily on a single line.
	// alt cannot contain "]" and url cannot contain ")"; the simple
	// form is sufficient for the Readwise/PG corpus.
	imageRe = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)

	// linkRe matches "[text](url)". Note: a PG-style footnote ref
	// "[[3](url)]" is matched by linkRe at the inner "[3](url)"
	// extended out to the outer "["; the replacement leaves "[3]"
	// in place for wa-kyn.8's footnote weaving.
	linkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)

	// wikilinkRe matches "[[content]]" with at least one character.
	wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

	// codeFenceRe matches a fenced code block. (?ms): "." spans
	// newlines; "^" / "$" anchor at line boundaries. Group 1 is the
	// optional language tag from the opening fence.
	codeFenceRe = regexp.MustCompile("(?ms)^```([^\n]*)\n(.*?)\n```[^\n]*$")
)

// StripMarkdown applies §5.1 step 7 to prose, in order (wa-kyn.7):
//
//	a. Remove ![alt](url) image refs entirely.
//	b. Replace [text](url) → text. As a useful side effect,
//	   PG-style footnote refs "[[N](url)]" collapse to "[N]" —
//	   exactly the marker shape wa-kyn.8 will weave.
//	c. Replace [[slug|display]] → display, otherwise [[slug]] →
//	   slug with "-" replaced by " ".
//	d. Strip fenced ``` code blocks; append "code_block" (or
//	   "code_block:lang" if the opening fence has a language tag)
//	   to the returned skipped slice in match order.
//
// Inline single-backtick `code` is NOT touched — the spec only
// mentions fenced code. The four steps run in declared order so
// images do not collide with the link rule and so wikilinks do
// not double-process inside a previously-flattened link.
//
// Returns (cleaned, skipped). skipped is nil when no code fences
// were dropped.
func StripMarkdown(prose string) (string, []string) {
	out := imageRe.ReplaceAllString(prose, "")
	out = linkRe.ReplaceAllString(out, "$1")
	out = wikilinkRe.ReplaceAllStringFunc(out, flattenWikilink)

	var skipped []string
	out = codeFenceRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := codeFenceRe.FindStringSubmatch(m)
		lang := strings.TrimSpace(sub[1])
		if lang == "" {
			skipped = append(skipped, "code_block")
		} else {
			skipped = append(skipped, "code_block:"+lang)
		}
		return ""
	})
	return out, skipped
}

func flattenWikilink(match string) string {
	inner := match[2 : len(match)-2]
	if i := strings.Index(inner, "|"); i >= 0 {
		return inner[i+1:]
	}
	return strings.ReplaceAll(inner, "-", " ")
}
