package extract

import (
	"regexp"
	"strconv"
	"strings"
)

// footnoteHeadRe anchors at line start so a body that mentions
// "see [1] above" is NOT interpreted as a new footnote header.
var footnoteHeadRe = regexp.MustCompile(`^\[(\d+)\]\s+(.*)$`)

// ParseFootnotes converts notesPart (the second return of SplitNotes)
// into a map from footnote number to footnote body (§5.1 step 6,
// wa-kyn.6).
//
// Algorithm — a one-pass state machine over the lines of notesPart:
//
//   - A line matching `^\[(\d+)\]\s+(.*)` opens a new entry: any
//     in-progress entry is flushed, then N becomes the current key
//     and the trailing capture group is the first body line.
//   - Any other line is appended verbatim to the current entry's
//     body. Lines that arrive before any header line are dropped
//     silently (they're orphan continuation text and treating them
//     as malformed prose is safer than guessing a number).
//
// On flush the body is joined with "\n" and TrimSpace'd; entries
// whose body is empty after trimming are dropped (malformed). On
// duplicate keys, the LAST occurrence wins — natural map-assignment
// semantics, matching the bead's "drop malformed silently" guidance.
//
// The returned map is always non-nil; an essay with no footnotes
// produces an empty map.
func ParseFootnotes(notesPart string) map[int]string {
	out := make(map[int]string)
	currentNum := -1
	var currentBody []string

	flush := func() {
		if currentNum < 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(currentBody, "\n"))
		if body != "" {
			out[currentNum] = body
		}
		currentNum = -1
		currentBody = currentBody[:0]
	}

	for _, raw := range strings.Split(notesPart, "\n") {
		line := strings.TrimRight(raw, "\r")
		if m := footnoteHeadRe.FindStringSubmatch(line); m != nil {
			flush()
			n, err := strconv.Atoi(m[1])
			if err != nil {
				continue // unreachable given \d+ — defensive
			}
			currentNum = n
			currentBody = append(currentBody, m[2])
			continue
		}
		if currentNum >= 0 {
			currentBody = append(currentBody, line)
		}
	}
	flush()
	return out
}
