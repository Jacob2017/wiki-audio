package extract

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// paragraphSepRe splits on a blank line (optionally containing
	// horizontal whitespace) — §5.1 step 8a's `\n\s*\n` boundary.
	paragraphSepRe = regexp.MustCompile(`\n\s*\n`)

	// markerRe matches "[N]" with N a non-negative integer. Link-
	// debris (a "[N]" preceded by "]" or followed by "(") is filtered
	// at the call site, not in the regex.
	markerRe = regexp.MustCompile(`\[(\d+)\]`)
)

// WeaveStats surfaces the two edge-case categories from the wa-kyn.8
// matrix that the bead asks the caller to log:
//
//   - Unknown: marker Ns referenced by prose with no matching key in
//     footnotes. Bead says "log debug".
//   - Unreferenced: keys present in footnotes that no paragraph ever
//     matched. Bead says "log info".
//
// Order: Unknown is in marker-encounter order across paragraphs.
// Unreferenced is in ascending key order so the field is stable
// across runs (Go map iteration is otherwise non-deterministic).
type WeaveStats struct {
	Unknown      []int
	Unreferenced []int
}

// WeaveFootnotes implements §5.1 step 8 — paragraph-attributed
// footnote weaving (wa-kyn.8, the load-bearing step).
//
// Algorithm:
//
//  1. Split prose into paragraphs on /\n\s*\n/.
//  2. For each paragraph: find every "[N]" with N a digit run.
//     Skip a marker if it is preceded by "]" or immediately followed
//     by "(" — that pattern is link debris, not a footnote ref. Real
//     footnote refs survive D7 step b's link flattening; the guard
//     is defensive.
//  3. Strip the surviving markers from the paragraph text.
//  4. If any markers survived, append a blank line and one
//     "Footnote N: <body>" line per UNIQUE marker, in order of FIRST
//     appearance. Markers whose N is missing from footnotes are
//     stripped silently and recorded in stats.Unknown — the prose
//     keeps a clean reading despite the dangling reference.
//
// Paragraphs without markers pass through unchanged. Link-debris
// markers are left in prose verbatim (neither stripped nor woven).
//
// The output joins paragraphs with "\n\n" — exactly one blank line
// between them. Step 9 (collapse blank-line runs) is the canonical
// cleanup pass and runs after this step.
func WeaveFootnotes(prose string, footnotes map[int]string) (string, WeaveStats) {
	var stats WeaveStats
	referenced := make(map[int]bool)

	paragraphs := paragraphSepRe.Split(prose, -1)
	for i, para := range paragraphs {
		woven, matched, unknown := weaveParagraph(para, footnotes)
		paragraphs[i] = woven
		for _, n := range matched {
			referenced[n] = true
		}
		stats.Unknown = append(stats.Unknown, unknown...)
	}

	// Stable, ascending order for Unreferenced — Go map iteration is
	// non-deterministic and the field is observable to caller logs.
	for n := range footnotes {
		if !referenced[n] {
			stats.Unreferenced = append(stats.Unreferenced, n)
		}
	}
	sort.Ints(stats.Unreferenced)

	return strings.Join(paragraphs, "\n\n"), stats
}

// weaveParagraph processes a single paragraph and returns:
//   - woven: the paragraph with kept markers stripped and footnote
//     lines appended (separated by a blank line).
//   - matched: footnote keys that were referenced by the paragraph
//     AND found in the map (deduplicated, first-appearance order).
//   - unknown: marker Ns referenced by the paragraph but missing
//     from the map (deduplicated within the paragraph).
func weaveParagraph(para string, footnotes map[int]string) (string, []int, []int) {
	locs := markerRe.FindAllStringSubmatchIndex(para, -1)
	if len(locs) == 0 {
		return para, nil, nil
	}

	type marker struct {
		start, end int
		n          int
	}
	var keep []marker
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		if start > 0 && para[start-1] == ']' {
			continue
		}
		if end < len(para) && para[end] == '(' {
			continue
		}
		n, err := strconv.Atoi(para[loc[2]:loc[3]])
		if err != nil {
			continue
		}
		keep = append(keep, marker{start, end, n})
	}
	if len(keep) == 0 {
		return para, nil, nil
	}

	var b strings.Builder
	last := 0
	for _, m := range keep {
		b.WriteString(para[last:m.start])
		last = m.end
	}
	b.WriteString(para[last:])
	stripped := b.String()

	seen := make(map[int]bool)
	var order []int
	for _, m := range keep {
		if !seen[m.n] {
			seen[m.n] = true
			order = append(order, m.n)
		}
	}

	var lines []string
	var matched, unknown []int
	for _, n := range order {
		body, ok := footnotes[n]
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		lines = append(lines, fmt.Sprintf("Footnote %d: %s", n, body))
		matched = append(matched, n)
	}

	if len(lines) == 0 {
		return stripped, matched, unknown
	}
	return stripped + "\n\n" + strings.Join(lines, "\n"), matched, unknown
}
