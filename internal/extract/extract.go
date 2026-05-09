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
// the source file with its metadata header stripped. RawBody is the
// buffer ready for step 5 (prose vs Notes split — call SplitNotes on
// RawBody, see wa-kyn.5).
//
// Two header shapes are accepted (wa-k8a, F1):
//
//   - Readwise canonical: "## Metadata" block + "## Full Document"
//     sentinel. The block (including both headers) is removed from
//     RawBody.
//   - YAML frontmatter: "---\n…\n---\n" at the very top of the file.
//     The fenced block is removed from RawBody and the title is read
//     from the `title:` key when present.
//
// The "# Title" heading line — when one exists — is dropped from
// RawBody as well (wa-k8a F2). Speaking the title twice (from both
// the ID3 tag and the body) was an audibly bad regression.
type Parsed struct {
	Title   string
	RawBody string

	// Meta carries Readwise-only metadata fields the feed generator
	// surfaces (per-item link + description, wa-bo5). Empty for YAML-
	// frontmatter files; the feed item then omits the corresponding
	// element rather than emitting an empty one.
	Meta ReadwiseMeta
}

var (
	titleRe        = regexp.MustCompile(`(?m)^# (.+)$`)
	yamlTitleRe    = regexp.MustCompile(`(?m)^title:\s*"?(.+?)"?\s*$`)
	yamlFrontStart = "---\n"
	yamlFrontEnd   = "\n---\n"
)

const (
	metadataHeader   = "## Metadata"
	fullDocumentMark = "## Full Document"
)

// ParseFile reads path as UTF-8 and runs §5.1 steps 1-4.
//
// The file must declare its body via either the Readwise headers
// ("## Metadata" + "## Full Document") OR a YAML frontmatter fence
// at the top. Files that satisfy neither return a non-nil error so
// the build fails loudly rather than silently shipping the
// metadata block into TTS.
func ParseFile(path string) (Parsed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Parsed{}, fmt.Errorf("extract: read %q: %w", path, err)
	}
	return Parse(string(data), filepath.Base(path))
}

// Parse runs §5.1 steps 1-4 on already-loaded content. name is the
// file basename, used as the title fallback when neither a "# Title"
// heading nor a YAML `title:` key is present.
func Parse(content, name string) (Parsed, error) {
	content = strings.TrimPrefix(content, "\uFEFF")

	body, err := stripHeaders(content)
	if err != nil {
		return Parsed{Title: titleFallback(content, name)}, err
	}

	title, body := extractTitle(content, body)
	if title == "" {
		title = titleCaseFromFilename(name)
	}
	return Parsed{
		Title:   title,
		RawBody: body,
		Meta:    ParseReadwiseMeta(content),
	}, nil
}

// stripHeaders dispatches between Readwise and YAML-frontmatter
// header shapes (wa-k8a F1). Order matters: a file with both
// (defensively) is treated as Readwise because the Readwise headers
// are more explicit about where the body starts.
func stripHeaders(content string) (string, error) {
	if hasReadwiseHeaders(content) {
		return stripReadwiseHeaders(content)
	}
	if hasYAMLFrontmatter(content) {
		return stripYAMLFrontmatter(content)
	}
	return "", errors.New(
		"extract: file has neither Readwise headers ('## Metadata' + '## Full Document') " +
			"nor a YAML frontmatter ('---' fence) at the top; cannot determine where the body starts")
}

func hasReadwiseHeaders(content string) bool {
	mIdx := strings.Index(content, metadataHeader)
	if mIdx < 0 {
		return false
	}
	return strings.Contains(content[mIdx:], fullDocumentMark)
}

func hasYAMLFrontmatter(content string) bool {
	return strings.HasPrefix(content, yamlFrontStart)
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

// stripYAMLFrontmatter removes a "---\n…\n---\n" fence at the very
// top of content and returns the remainder. The closing fence may
// be the literal "---" at the end of file (no trailing newline) or
// "\n---\n" mid-file.
func stripYAMLFrontmatter(content string) (string, error) {
	if !strings.HasPrefix(content, yamlFrontStart) {
		return "", errors.New("extract: missing YAML frontmatter opening '---'")
	}
	rest := content[len(yamlFrontStart):]
	if idx := strings.Index(rest, yamlFrontEnd); idx >= 0 {
		return rest[idx+len(yamlFrontEnd):], nil
	}
	// Tolerate a frontmatter that runs to the end of file (no body).
	if strings.HasSuffix(rest, "\n---") {
		return "", nil
	}
	return "", errors.New("extract: YAML frontmatter is not closed by a '---' fence")
}

// extractTitle returns the essay title and a body with the "# Title"
// line removed (if one was present).
//
// Lookup order (wa-k8a F1, F2):
//
//  1. The first "^# (.+)$" line in body. If found, the line is
//     dropped so it isn't read aloud after the ID3 tag already
//     spoke it.
//  2. The "title:" key inside the YAML frontmatter (when present in
//     the original file). The frontmatter has already been stripped
//     from body by stripHeaders, so we look at the original content.
//  3. Empty (caller falls back to the filename).
func extractTitle(content, body string) (string, string) {
	if loc := titleRe.FindStringIndex(body); loc != nil {
		title := strings.TrimSpace(titleRe.FindStringSubmatch(body)[1])
		end := loc[1]
		if end < len(body) && body[end] == '\n' {
			end++
		}
		return title, body[:loc[0]] + body[end:]
	}
	if hasYAMLFrontmatter(content) {
		if t := readYAMLTitle(content); t != "" {
			return t, body
		}
	}
	return "", body
}

// titleFallback is used only when stripHeaders fails — the caller
// still wants a sensible Title field on the partial Parsed result.
func titleFallback(content, name string) string {
	if loc := titleRe.FindStringIndex(content); loc != nil {
		return strings.TrimSpace(titleRe.FindStringSubmatch(content)[1])
	}
	if hasYAMLFrontmatter(content) {
		if t := readYAMLTitle(content); t != "" {
			return t
		}
	}
	return titleCaseFromFilename(name)
}

func readYAMLTitle(content string) string {
	if !hasYAMLFrontmatter(content) {
		return ""
	}
	rest := content[len(yamlFrontStart):]
	end := strings.Index(rest, yamlFrontEnd)
	if end < 0 {
		return ""
	}
	block := rest[:end]
	m := yamlTitleRe.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ReadwiseMeta is the subset of Readwise metadata fields the feed
// generator cares about (wa-bo5). Both fields are best-effort: a
// missing or malformed line yields the zero value rather than an
// error. The full body parse is always done elsewhere — this is a
// supplementary metadata reader.
type ReadwiseMeta struct {
	SourceURL string // "URL: ..." line
	Summary   string // "Summary: ..." line
}

// readwiseFieldRe matches a Readwise metadata bullet line and captures
// the field name + value. Tolerant of optional leading whitespace and
// of either `- ` or `* ` bullet markers.
var readwiseFieldRe = regexp.MustCompile(`(?m)^[ \t]*[-*][ \t]+([A-Za-z][A-Za-z ]*?):[ \t]*(.+?)[ \t]*$`)

// ParseReadwiseMeta extracts URL and Summary fields from the
// "## Metadata" block of a Readwise canonical export. Returns the
// zero ReadwiseMeta if the file is YAML-frontmatter-only or has no
// "## Metadata" block — callers treat absence as "no per-essay
// metadata to surface in the feed".
func ParseReadwiseMeta(content string) ReadwiseMeta {
	if !hasReadwiseHeaders(content) {
		return ReadwiseMeta{}
	}
	mIdx := strings.Index(content, metadataHeader)
	fdIdx := strings.Index(content[mIdx:], fullDocumentMark)
	if mIdx < 0 || fdIdx < 0 {
		return ReadwiseMeta{}
	}
	block := content[mIdx : mIdx+fdIdx]

	var meta ReadwiseMeta
	for _, m := range readwiseFieldRe.FindAllStringSubmatch(block, -1) {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		val := strings.TrimSpace(m[2])
		switch key {
		case "url":
			meta.SourceURL = val
		case "summary":
			meta.Summary = val
		}
	}
	return meta
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

// SplitNotes etc. are unchanged; everything below this line was
// preserved verbatim from the pre-wa-k8a extract.go.

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
