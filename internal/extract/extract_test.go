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
