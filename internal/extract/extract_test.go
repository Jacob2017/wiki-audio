package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const canonicalEssay = `# How to Do Great Work

![rw-book-cover](https://news.ycombinator.com/favicon.ico)

## Metadata
- Author: [[Paul Graham]]
- Full Title: How to Do Great Work
- Category: #articles
- Summary: A guide.
- URL: http://paulgraham.com/greatwork.html

## Full Document
July 2023

If you collected lists of techniques for doing great work, what would the intersection look like?

The following recipe assumes you're very ambitious.
`

func TestParse_CanonicalEssay(t *testing.T) {
	got, err := Parse(canonicalEssay, "How to Do Great Work.md")
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if got.Title != "How to Do Great Work" {
		t.Errorf("Title = %q, want %q", got.Title, "How to Do Great Work")
	}
	if strings.Contains(got.RawBody, "## Metadata") {
		t.Errorf("RawBody should not contain '## Metadata': %q", got.RawBody)
	}
	if strings.Contains(got.RawBody, "## Full Document") {
		t.Errorf("RawBody should not contain '## Full Document': %q", got.RawBody)
	}
	if strings.Contains(got.RawBody, "Author: [[Paul Graham]]") {
		t.Errorf("RawBody should not contain metadata bullets: %q", got.RawBody)
	}
	if !strings.Contains(got.RawBody, "July 2023") {
		t.Errorf("RawBody should contain body content: %q", got.RawBody)
	}
	if !strings.Contains(got.RawBody, "# How to Do Great Work") {
		t.Errorf("RawBody should preserve the # title heading line: %q", got.RawBody)
	}
	if !strings.Contains(got.RawBody, "![rw-book-cover]") {
		t.Errorf("RawBody should preserve content above '## Metadata': %q", got.RawBody)
	}
}

func TestParse_TitleFallbackToFilename(t *testing.T) {
	const noTitle = `## Metadata
- Author: [[Paul Graham]]
- URL: x

## Full Document
Body text here.
`
	got, err := Parse(noTitle, "how-to-do-great-work.md")
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if got.Title != "How To Do Great Work" {
		t.Errorf("Title fallback = %q, want %q", got.Title, "How To Do Great Work")
	}
	if !strings.Contains(got.RawBody, "Body text here.") {
		t.Errorf("RawBody should contain body content")
	}
}

func TestParse_MissingMetadataHeader(t *testing.T) {
	const noMetadata = `# Some Essay

Body without the Readwise headers.
`
	_, err := Parse(noMetadata, "x.md")
	if err == nil {
		t.Fatal("expected error for missing '## Metadata' header, got nil")
	}
	if !strings.Contains(err.Error(), "Metadata") {
		t.Errorf("error should mention 'Metadata': %q", err)
	}
}

func TestParse_MissingFullDocumentHeader(t *testing.T) {
	const noFullDoc = `# Some Essay

## Metadata
- Author: x

Body that never reaches '## Full Document'.
`
	_, err := Parse(noFullDoc, "x.md")
	if err == nil {
		t.Fatal("expected error for missing '## Full Document' header, got nil")
	}
	if !strings.Contains(err.Error(), "Full Document") {
		t.Errorf("error should mention 'Full Document': %q", err)
	}
}

func TestParse_TrailingWhitespaceOnHeaders(t *testing.T) {
	withSpaces := "# T\n\n## Metadata   \n- a: b\n## Full Document  \nbody\n"
	got, err := Parse(withSpaces, "x.md")
	if err != nil {
		t.Fatalf("Parse: unexpected error with trailing whitespace: %v", err)
	}
	if !strings.Contains(got.RawBody, "body") {
		t.Errorf("RawBody should contain body content despite trailing whitespace")
	}
	if strings.Contains(got.RawBody, "Metadata") {
		t.Errorf("RawBody should not contain 'Metadata' line: %q", got.RawBody)
	}
}

func TestParse_CRLFLineEndings(t *testing.T) {
	crlf := strings.ReplaceAll(canonicalEssay, "\n", "\r\n")
	got, err := Parse(crlf, "x.md")
	if err != nil {
		t.Fatalf("Parse: unexpected error with CRLF endings: %v", err)
	}
	if got.Title != "How to Do Great Work" {
		t.Errorf("Title = %q under CRLF, want %q", got.Title, "How to Do Great Work")
	}
	if !strings.Contains(got.RawBody, "July 2023") {
		t.Errorf("RawBody should contain body content under CRLF")
	}
}

func TestParse_BOMStripped(t *testing.T) {
	got, err := Parse("\uFEFF"+canonicalEssay, "x.md")
	if err != nil {
		t.Fatalf("Parse: unexpected error with BOM: %v", err)
	}
	if got.Title != "How to Do Great Work" {
		t.Errorf("Title = %q with BOM, want %q", got.Title, "How to Do Great Work")
	}
}

func TestParse_PreservesContentAboveMetadata(t *testing.T) {
	got, err := Parse(canonicalEssay, "x.md")
	if err != nil {
		t.Fatal(err)
	}
	idxTitle := strings.Index(got.RawBody, "# How to Do Great Work")
	idxBody := strings.Index(got.RawBody, "July 2023")
	if idxTitle == -1 || idxBody == -1 {
		t.Fatalf("expected both title and body in RawBody: %q", got.RawBody)
	}
	if idxTitle >= idxBody {
		t.Errorf("title heading should appear before body content")
	}
}

func TestParseFile_RoundTripsCanonicalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "How to Do Great Work.md")
	if err := os.WriteFile(path, []byte(canonicalEssay), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got.Title != "How to Do Great Work" {
		t.Errorf("Title = %q, want %q", got.Title, "How to Do Great Work")
	}
	if !strings.Contains(got.RawBody, "July 2023") {
		t.Errorf("RawBody should contain body content")
	}
}

func TestParseFile_NonexistentReturnsError(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "does-not-exist.md"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestTitleCaseFromFilename(t *testing.T) {
	cases := []struct{ in, want string }{
		{"how-to-do-great-work.md", "How To Do Great Work"},
		{"high_agency.markdown", "High Agency"},
		{"hello.txt", "Hello"},
		{"already Title Case.md", "Already Title Case"},
		{"", ""},
		{"single.md", "Single"},
	}
	for _, c := range cases {
		got := titleCaseFromFilename(c.in)
		if got != c.want {
			t.Errorf("titleCaseFromFilename(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- SplitNotes (§5.1 step 5, wa-kyn.5) ----------------------------------

func TestSplitNotes_NoNotesSection(t *testing.T) {
	const body = `# Title

July 2023

The essay body without any notes section.

Just paragraphs of prose.
`
	prose, notes := SplitNotes(body)
	if prose != body {
		t.Errorf("prose should equal full body when no Notes marker present;\n got: %q\nwant: %q", prose, body)
	}
	if notes != "" {
		t.Errorf("notes should be empty when no Notes marker present; got: %q", notes)
	}
}

func TestSplitNotes_TypicalEssayWithH2Notes(t *testing.T) {
	const body = `# Some Essay

July 2023

The body has multiple paragraphs.

A second paragraph with [[1]] reference.

## Notes

[[1]] First footnote text.

[[2]] Second footnote text.
`
	prose, notes := SplitNotes(body)
	if !strings.Contains(prose, "July 2023") || !strings.Contains(prose, "second paragraph") {
		t.Errorf("prose should contain body content; got: %q", prose)
	}
	if strings.Contains(prose, "## Notes") {
		t.Errorf("prose should NOT contain the '## Notes' marker line; got: %q", prose)
	}
	if strings.Contains(prose, "First footnote") {
		t.Errorf("prose should NOT contain footnote bodies; got: %q", prose)
	}
	if !strings.Contains(notes, "First footnote") || !strings.Contains(notes, "Second footnote") {
		t.Errorf("notes should contain footnote bodies; got: %q", notes)
	}
	if strings.Contains(notes, "## Notes") {
		t.Errorf("notes should NOT contain the marker line; got: %q", notes)
	}
}

func TestSplitNotes_TypicalEssayWithBoldNotes(t *testing.T) {
	const body = `# Some Essay

Body paragraph one.

Body paragraph two.

**Notes**

[[1]] note text.
`
	prose, notes := SplitNotes(body)
	if !strings.Contains(prose, "Body paragraph one") {
		t.Errorf("prose should contain body content; got: %q", prose)
	}
	if strings.Contains(prose, "**Notes**") {
		t.Errorf("prose should NOT contain the '**Notes**' marker; got: %q", prose)
	}
	if !strings.Contains(notes, "[[1]] note text.") {
		t.Errorf("notes should contain the note body; got: %q", notes)
	}
}

func TestSplitNotes_MarkerAtVeryEndNoContent(t *testing.T) {
	const body = `# Some Essay

Body paragraph one.

Body paragraph two.

## Notes
`
	prose, notes := SplitNotes(body)
	if !strings.Contains(prose, "Body paragraph one") || !strings.Contains(prose, "Body paragraph two") {
		t.Errorf("prose should contain body content; got: %q", prose)
	}
	if strings.Contains(prose, "## Notes") {
		t.Errorf("prose should NOT contain the marker line; got: %q", prose)
	}
	if strings.TrimSpace(notes) != "" {
		t.Errorf("notes should be empty (or whitespace-only) when marker has no content after; got: %q", notes)
	}
}

func TestSplitNotes_CaseInsensitive(t *testing.T) {
	cases := []string{
		"## NOTES",
		"## notes",
		"## NoTeS",
		"**NOTES**",
		"**notes**",
		"**nOtEs**",
	}
	for _, marker := range cases {
		body := "Prose here.\n\n" + marker + "\n\nNote body.\n"
		prose, notes := SplitNotes(body)
		if !strings.Contains(prose, "Prose here.") {
			t.Errorf("marker %q: prose should contain 'Prose here.'; got: %q", marker, prose)
		}
		if !strings.Contains(notes, "Note body.") {
			t.Errorf("marker %q: notes should contain 'Note body.'; got: %q", marker, notes)
		}
	}
}

func TestSplitNotes_TrailingWhitespace(t *testing.T) {
	const body = "Prose.\n\n## Notes   \n\nNote body.\n"
	prose, notes := SplitNotes(body)
	if !strings.Contains(prose, "Prose.") {
		t.Errorf("prose should contain 'Prose.'; got: %q", prose)
	}
	if !strings.Contains(notes, "Note body.") {
		t.Errorf("notes should contain 'Note body.'; got: %q", notes)
	}
}

func TestSplitNotes_VariantsDoNotMatch(t *testing.T) {
	cases := []string{
		"## Notes:",                 // colon variant
		"## Notes section",          // extra word
		"### Notes",                 // h3 not h2
		"# Notes",                   // h1 not h2
		"**Notes:**",                // colon inside bold
		"** Notes **",               // spaces inside bold
		"Notes",                     // bare text
		"some inline ## Notes here", // not at start of line
	}
	for _, marker := range cases {
		body := "Prose.\n\n" + marker + "\n\nMore text.\n"
		prose, notes := SplitNotes(body)
		if notes != "" {
			t.Errorf("marker variant %q should NOT split (false positive); got notes=%q", marker, notes)
		}
		if prose != body {
			t.Errorf("marker variant %q should leave prose intact", marker)
		}
	}
}

func TestSplitNotes_FirstMarkerWins(t *testing.T) {
	const body = `Para one.

**Notes**

First match block.

## Notes

Second match (should never reach here as a split point).
`
	prose, notes := SplitNotes(body)
	if !strings.Contains(prose, "Para one.") {
		t.Errorf("prose should contain content before first marker")
	}
	if strings.Contains(prose, "First match block") {
		t.Errorf("prose should not contain text after the first matching marker")
	}
	if !strings.Contains(notes, "First match block") {
		t.Errorf("notes should contain text immediately after the first matching marker; got: %q", notes)
	}
	if !strings.Contains(notes, "## Notes") {
		t.Errorf("a later '## Notes' literal should appear inside notes (not consumed as a second split); got: %q", notes)
	}
}

func TestSplitNotes_EmptyInput(t *testing.T) {
	prose, notes := SplitNotes("")
	if prose != "" || notes != "" {
		t.Errorf("empty rawBody → ('','') ; got prose=%q notes=%q", prose, notes)
	}
}

func TestSplitNotes_MarkerAtVeryStart(t *testing.T) {
	const body = "## Notes\n\nOnly note content.\n"
	prose, notes := SplitNotes(body)
	if prose != "" {
		t.Errorf("when marker is the first line, prose must be empty; got: %q", prose)
	}
	if !strings.Contains(notes, "Only note content.") {
		t.Errorf("notes should contain the body following the marker; got: %q", notes)
	}
}
