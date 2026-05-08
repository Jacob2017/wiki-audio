package extract

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Parsed is the output of §5.1 steps 1-4 (wa-kyn.4): a UTF-8 read of
// the source file with the Readwise canonical "## Metadata" block and
// "## Full Document" sentinel stripped. RawBody is the buffer ready
// for step 5 (prose vs Notes split — call SplitNotes on RawBody, see
// wa-kyn.5).
//
// The "# Title" header line itself is NOT removed from RawBody — only
// the metadata block and the Full Document sentinel are. Downstream
// markdown stripping (wa-kyn.7) handles heading markers in the body.
type Parsed struct {
	Title   string
	RawBody string
}

var titleRe = regexp.MustCompile(`(?m)^# (.+)$`)

const (
	metadataHeader   = "## Metadata"
	fullDocumentMark = "## Full Document"
)

// ParseFile reads path as UTF-8 and runs §5.1 steps 1-4.
//
// The file MUST be in the Readwise canonical format — a "## Metadata"
// block followed (somewhere later) by a "## Full Document" sentinel.
// Files that lack either header return a non-nil error so the build
// fails loudly rather than silently shipping metadata text into TTS.
func ParseFile(path string) (Parsed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Parsed{}, fmt.Errorf("extract: read %q: %w", path, err)
	}
	return Parse(string(data), filepath.Base(path))
}

// Parse runs §5.1 steps 1-4 on an already-loaded essay. name is the
// file basename and is used only as a fallback when the buffer has no
// "# Title" line.
func Parse(content, name string) (Parsed, error) {
	content = strings.TrimPrefix(content, "\uFEFF")
	title, ok := firstHeading(content)
	if !ok {
		title = titleCaseFromFilename(name)
	}
	body, err := stripReadwiseHeaders(content)
	if err != nil {
		return Parsed{Title: title}, err
	}
	return Parsed{Title: title, RawBody: body}, nil
}

func firstHeading(content string) (string, bool) {
	m := titleRe.FindStringSubmatch(content)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func stripReadwiseHeaders(content string) (string, error) {
	lines := strings.Split(content, "\n")
	metaIdx, fullDocIdx := -1, -1
	for i, ln := range lines {
		trim := strings.TrimRight(ln, " \t\r")
		if metaIdx == -1 && trim == metadataHeader {
			metaIdx = i
			continue
		}
		if metaIdx != -1 && trim == fullDocumentMark {
			fullDocIdx = i
			break
		}
	}
	if metaIdx == -1 {
		return "", errors.New("extract: missing '## Metadata' header (file is not a Readwise canonical export)")
	}
	if fullDocIdx == -1 {
		return "", errors.New("extract: missing '## Full Document' header after '## Metadata'")
	}
	out := make([]string, 0, len(lines)-(fullDocIdx-metaIdx+1))
	out = append(out, lines[:metaIdx]...)
	out = append(out, lines[fullDocIdx+1:]...)
	return strings.Join(out, "\n"), nil
}

// SplitNotes scans rawBody line-by-line for the FIRST line that —
// after strings.TrimSpace — equals (case-insensitively) either
// "**Notes**" or "## Notes". These are the two Readwise canonical
// Notes-section markers (§5.1 step 5, wa-kyn.5).
//
// Returns (prose, notes). prose is everything before the marker
// line; notes is everything after it (may be empty if the marker is
// the last non-blank line, i.e. the essay declares a Notes section
// but has no footnotes in the export). If no marker appears at all,
// prose == rawBody and notes is "".
//
// The marker line itself is dropped — it appears in neither output.
//
// Variants like "## Notes:" or "## Notes section" are intentionally
// NOT matched. The two recognised forms are the only canonical ones
// in the corpus; anything else is treated as prose to avoid false-
// positive splits in essays whose body discusses notes-on-something.
func SplitNotes(rawBody string) (prose, notes string) {
	lines := strings.Split(rawBody, "\n")
	for i, ln := range lines {
		if isNotesMarker(ln) {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	return rawBody, ""
}

func isNotesMarker(line string) bool {
	trim := strings.TrimSpace(line)
	return strings.EqualFold(trim, "**Notes**") || strings.EqualFold(trim, "## Notes")
}

// titleCaseFromFilename derives a fallback title from a file basename
// when the buffer has no "# Title" line. It strips the .md/.txt
// extension, replaces hyphens and underscores with spaces, and upper-
// cases the first letter of each whitespace-delimited word. It is
// deliberately naive (does NOT lowercase function words like "of" or
// "the") because in practice this fallback fires only on malformed
// inputs and the result is replaced once a "# Title" line is added.
func titleCaseFromFilename(name string) string {
	stem := name
	for _, ext := range []string{".md", ".markdown", ".txt"} {
		stem = strings.TrimSuffix(stem, ext)
	}
	stem = strings.NewReplacer("-", " ", "_", " ").Replace(stem)
	fields := strings.Fields(stem)
	for i, f := range fields {
		rs := []rune(f)
		if len(rs) == 0 {
			continue
		}
		rs[0] = unicode.ToUpper(rs[0])
		for j := 1; j < len(rs); j++ {
			rs[j] = unicode.ToLower(rs[j])
		}
		fields[i] = string(rs)
	}
	return strings.Join(fields, " ")
}
