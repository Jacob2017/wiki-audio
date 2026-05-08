package extract

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseFootnotes_EmptyInput(t *testing.T) {
	got := ParseFootnotes("")
	if got == nil {
		t.Fatal("ParseFootnotes('') returned nil; want non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("ParseFootnotes('') = %v; want empty", got)
	}
}

func TestParseFootnotes_SingleNote(t *testing.T) {
	got := ParseFootnotes("[1] First footnote body.\n")
	want := map[int]string{1: "First footnote body."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_MultipleNotes(t *testing.T) {
	notes := `[1] First note body.

[2] Second note body.

[3] Third note body.
`
	got := ParseFootnotes(notes)
	want := map[int]string{
		1: "First note body.",
		2: "Second note body.",
		3: "Third note body.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_MultiLineBody(t *testing.T) {
	notes := "[1] First line of body.\nSecond line.\nThird line.\n"
	got := ParseFootnotes(notes)
	want := map[int]string{1: "First line of body.\nSecond line.\nThird line."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_BodyContainsBracketReference(t *testing.T) {
	// "see [1] above" inside a body should NOT start a new note —
	// the regex anchors at line start. Note 2's body must include
	// the inline [1] reference verbatim.
	notes := "[1] First note.\n\n[2] Refer to see [1] above for more.\n"
	got := ParseFootnotes(notes)
	want := map[int]string{
		1: "First note.",
		2: "Refer to see [1] above for more.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_LeadingTrailingWhitespaceTrimmed(t *testing.T) {
	// "Strip leading/trailing whitespace from each body" applies to
	// the joined body, not each line. The leading run after "[N]" is
	// consumed by the regex's \s+; the trailing whitespace on the
	// final line is stripped by TrimSpace; whitespace mid-body is
	// preserved verbatim.
	notes := "[1]    leading spaces.   \n   trailing line.   \n"
	got := ParseFootnotes(notes)
	want := "leading spaces.   \n   trailing line."
	if got[1] != want {
		t.Errorf("got %q; want %q", got[1], want)
	}
}

func TestParseFootnotes_OrphanContinuationDropped(t *testing.T) {
	// Continuation lines before any "[N] " header should vanish.
	notes := "Some orphan text.\nMore orphan text.\n[1] Real note.\n"
	got := ParseFootnotes(notes)
	want := map[int]string{1: "Real note."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_EmptyBodyDropped(t *testing.T) {
	// "[1] " with only trailing whitespace and no continuation
	// produces an empty body and must be dropped as malformed.
	notes := "[1]   \n[2] Real body.\n"
	got := ParseFootnotes(notes)
	if _, ok := got[1]; ok {
		t.Errorf("entry 1 should be dropped (empty body); got map %v", got)
	}
	if got[2] != "Real body." {
		t.Errorf("entry 2 should survive; got %q", got[2])
	}
}

func TestParseFootnotes_DuplicateKeyLastWins(t *testing.T) {
	notes := "[1] First definition.\n\n[1] Second definition wins.\n"
	got := ParseFootnotes(notes)
	if got[1] != "Second definition wins." {
		t.Errorf("duplicate key should resolve last-wins; got %q", got[1])
	}
}

func TestParseFootnotes_CRLFLineEndings(t *testing.T) {
	notes := "[1] First.\r\nContinuation line.\r\n\r\n[2] Second.\r\n"
	got := ParseFootnotes(notes)
	want := map[int]string{
		1: "First.\nContinuation line.",
		2: "Second.",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestParseFootnotes_TabAfterBracket(t *testing.T) {
	// `\s+` matches tab too; "[1]\tbody" should parse as note 1.
	got := ParseFootnotes("[1]\tbody after tab.\n")
	if got[1] != "body after tab." {
		t.Errorf("tab separator should be accepted; got %v", got)
	}
}

func TestParseFootnotes_NumericInBracketsOnly(t *testing.T) {
	// "[abc]" must not be treated as a header; "[10]" must.
	notes := "[abc] not a footnote.\n[10] Tenth note.\n"
	got := ParseFootnotes(notes)
	if _, ok := got[10]; !ok {
		t.Errorf("expected entry 10; got %v", got)
	}
	for k, v := range got {
		if strings.Contains(v, "[abc]") {
			t.Errorf("entry %d body should not contain '[abc]'; got %q", k, v)
		}
	}
}

func TestParseFootnotes_LargeIndices(t *testing.T) {
	notes := "[42] Forty-two.\n[100] One hundred.\n"
	got := ParseFootnotes(notes)
	want := map[int]string{42: "Forty-two.", 100: "One hundred."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
}

// Integration: full pipeline ParseFile → SplitNotes → ParseFootnotes
// on a synthetic essay validates the contract between the three.
func TestParseFootnotes_FullPipeline(t *testing.T) {
	const essay = `# Synthetic Essay

![rw-book-cover](https://example.com/cover.png)

## Metadata
- Author: [[Test]]
- URL: https://example.com

## Full Document
Body paragraph one referencing [1] something.

Body paragraph two referencing [2] another thing.

## Notes

[1] First note body.

[2] Second note body, multi-line.
With a continuation.
`
	parsed, err := Parse(essay, "synthetic.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	prose, notes := SplitNotes(parsed.RawBody)
	if !strings.Contains(prose, "paragraph one") || !strings.Contains(prose, "paragraph two") {
		t.Errorf("prose should contain both paragraphs")
	}
	footnotes := ParseFootnotes(notes)
	if footnotes[1] != "First note body." {
		t.Errorf("[1] = %q", footnotes[1])
	}
	if footnotes[2] != "Second note body, multi-line.\nWith a continuation." {
		t.Errorf("[2] = %q", footnotes[2])
	}
	if len(footnotes) != 2 {
		t.Errorf("expected exactly 2 footnotes; got %d (%v)", len(footnotes), footnotes)
	}
}
