package extract

import (
	"reflect"
	"strings"
	"testing"
)

// TestWeaveFootnotes_BeadWorkedExample reproduces the worked example
// from wa-kyn.8 verbatim. It is the spec's gold input/output pair.
func TestWeaveFootnotes_BeadWorkedExample(t *testing.T) {
	prose := "Working hard isn't enough[1]. You have to work on the right things[2]."
	footnotes := map[int]string{
		1: "By 'hard' I mean ... long hours.",
		2: "Choosing the topic matters more than effort.",
	}
	want := "Working hard isn't enough. You have to work on the right things.\n\n" +
		"Footnote 1: By 'hard' I mean ... long hours.\n" +
		"Footnote 2: Choosing the topic matters more than effort."
	got, stats := WeaveFootnotes(prose, footnotes)
	if got != want {
		t.Errorf("woven output mismatch:\n got: %q\nwant: %q", got, want)
	}
	if len(stats.Unknown) != 0 {
		t.Errorf("Unknown should be empty; got %v", stats.Unknown)
	}
	if len(stats.Unreferenced) != 0 {
		t.Errorf("Unreferenced should be empty; got %v", stats.Unreferenced)
	}
}

func TestWeaveFootnotes_NoNotesEmptyMap(t *testing.T) {
	prose := "Plain paragraph one.\n\nPlain paragraph two."
	got, stats := WeaveFootnotes(prose, map[int]string{})
	if got != prose {
		t.Errorf("empty footnote map should pass prose through unchanged;\n got: %q\nwant: %q", got, prose)
	}
	if len(stats.Unknown) != 0 || len(stats.Unreferenced) != 0 {
		t.Errorf("stats should be empty; got %+v", stats)
	}
}

// Edge case: marker in prose with no matching note → strip silently,
// record in Unknown for caller's debug log.
func TestWeaveFootnotes_MarkerWithoutMatchingNote(t *testing.T) {
	prose := "A paragraph mentioning [42] without a definition."
	got, stats := WeaveFootnotes(prose, map[int]string{1: "irrelevant note."})
	// Key 42 is referenced but not in map → stripped + Unknown.
	// Key 1 is in map but not referenced → Unreferenced.
	if !strings.Contains(got, "without a definition") {
		t.Errorf("paragraph text should survive; got %q", got)
	}
	if strings.Contains(got, "[42]") {
		t.Errorf("unmatched marker should be stripped; got %q", got)
	}
	if strings.Contains(got, "Footnote 42") {
		t.Errorf("unmatched marker should NOT emit footnote; got %q", got)
	}
	if !reflect.DeepEqual(stats.Unknown, []int{42}) {
		t.Errorf("Unknown = %v; want [42]", stats.Unknown)
	}
	if !reflect.DeepEqual(stats.Unreferenced, []int{1}) {
		t.Errorf("Unreferenced = %v; want [1]", stats.Unreferenced)
	}
}

func TestWeaveFootnotes_DefinedButNeverReferenced(t *testing.T) {
	prose := "Plain paragraph with no markers."
	footnotes := map[int]string{1: "first.", 2: "second.", 3: "third."}
	got, stats := WeaveFootnotes(prose, footnotes)
	if got != prose {
		t.Errorf("prose should pass through unchanged; got %q", got)
	}
	if !reflect.DeepEqual(stats.Unreferenced, []int{1, 2, 3}) {
		t.Errorf("Unreferenced should be ascending; got %v", stats.Unreferenced)
	}
}

// Edge case: same [N] in one paragraph twice → emit ONCE.
func TestWeaveFootnotes_DuplicateMarkerInOneParagraphEmitsOnce(t *testing.T) {
	prose := "First mention[1], then later [1] again, finally one more [1] time."
	got, _ := WeaveFootnotes(prose, map[int]string{1: "the body."})
	if strings.Count(got, "Footnote 1:") != 1 {
		t.Errorf("Footnote 1: should appear exactly once; got %d in %q",
			strings.Count(got, "Footnote 1:"), got)
	}
	if strings.Contains(got, "[1]") {
		t.Errorf("all [1] markers should be stripped; got %q", got)
	}
}

func TestWeaveFootnotes_DuplicateAcrossParagraphsEmitsPerParagraph(t *testing.T) {
	prose := "First paragraph mentions [1].\n\nSecond paragraph also mentions [1]."
	got, _ := WeaveFootnotes(prose, map[int]string{1: "body."})
	if strings.Count(got, "Footnote 1:") != 2 {
		t.Errorf("Footnote 1: should appear once per paragraph (= 2 total); got %d in %q",
			strings.Count(got, "Footnote 1:"), got)
	}
}

func TestWeaveFootnotes_MultipleMarkersInOrderOfFirstAppearance(t *testing.T) {
	prose := "Refs [3], [1], [2], and [1] again."
	got, _ := WeaveFootnotes(prose, map[int]string{
		1: "one.", 2: "two.", 3: "three.",
	})
	wantOrder := []string{"Footnote 3:", "Footnote 1:", "Footnote 2:"}
	prev := 0
	for _, fragment := range wantOrder {
		idx := strings.Index(got, fragment)
		if idx < prev {
			t.Errorf("expected order %v in output; got %q", wantOrder, got)
		}
		prev = idx
	}
	if strings.Contains(got, "[1]") || strings.Contains(got, "[2]") || strings.Contains(got, "[3]") {
		t.Errorf("all markers should be stripped; got %q", got)
	}
}

// Edge case: link debris ([N] preceded by "]") — leave in place.
// Pattern arises from reference-style markdown like "[label][5]" that
// survives step 7b (which only flattens "[text](url)" inline links).
func TestWeaveFootnotes_LinkDebrisPrecededByBracket(t *testing.T) {
	prose := "Some [label][5] reference-style debris."
	got, stats := WeaveFootnotes(prose, map[int]string{5: "should not be emitted."})
	if !strings.Contains(got, "[5]") {
		t.Errorf("link-debris [5] (preceded by ']') should remain in prose; got %q", got)
	}
	if strings.Contains(got, "Footnote 5:") {
		t.Errorf("link-debris should not emit a footnote; got %q", got)
	}
	if !reflect.DeepEqual(stats.Unreferenced, []int{5}) {
		t.Errorf("link-debris should leave 5 unreferenced; got %v", stats.Unreferenced)
	}
}

// Edge case: link debris ([N] followed by "(") — leave in place.
func TestWeaveFootnotes_LinkDebrisFollowedByParen(t *testing.T) {
	prose := "Some [5](https://example.com) malformed link debris."
	got, stats := WeaveFootnotes(prose, map[int]string{5: "should not be emitted."})
	if !strings.Contains(got, "[5]") {
		t.Errorf("link-debris [5] (followed by '(') should remain in prose; got %q", got)
	}
	if strings.Contains(got, "Footnote 5:") {
		t.Errorf("link-debris should not emit a footnote; got %q", got)
	}
	if !reflect.DeepEqual(stats.Unreferenced, []int{5}) {
		t.Errorf("link-debris should leave 5 unreferenced; got %v", stats.Unreferenced)
	}
}

func TestWeaveFootnotes_MultipleParagraphsEachWithOwnFootnotes(t *testing.T) {
	prose := "Para one references [1].\n\nPara two references [2] and [3].\n\nPara three has none."
	footnotes := map[int]string{1: "one.", 2: "two.", 3: "three."}
	got, stats := WeaveFootnotes(prose, footnotes)

	expected := []string{
		"Para one references .",
		"Footnote 1: one.",
		"Para two references  and .",
		"Footnote 2: two.",
		"Footnote 3: three.",
		"Para three has none.",
	}
	for _, frag := range expected {
		if !strings.Contains(got, frag) {
			t.Errorf("missing fragment %q in output; got %q", frag, got)
		}
	}
	if len(stats.Unknown) != 0 || len(stats.Unreferenced) != 0 {
		t.Errorf("stats should be empty; got %+v", stats)
	}
}

func TestWeaveFootnotes_ParagraphSeparatorVariants(t *testing.T) {
	// `\n\s*\n` matches blank lines with optional whitespace.
	cases := []string{
		"para one.\n\npara two.",
		"para one.\n   \npara two.",
		"para one.\n\t\npara two.",
		"para one.\n\n\npara two.",
	}
	for _, prose := range cases {
		got, _ := WeaveFootnotes(prose, map[int]string{})
		if !strings.Contains(got, "para one.") || !strings.Contains(got, "para two.") {
			t.Errorf("both paragraphs should survive in %q; got %q", prose, got)
		}
		// After Join with "\n\n", we expect exactly two paragraphs.
		parts := strings.Split(got, "\n\n")
		if len(parts) != 2 {
			t.Errorf("expected 2 paragraphs after weave for input %q; got %d (%v)", prose, len(parts), parts)
		}
	}
}

func TestWeaveFootnotes_ParagraphIsJustAMarker(t *testing.T) {
	prose := "Real paragraph.\n\n[1]"
	got, _ := WeaveFootnotes(prose, map[int]string{1: "the note."})
	if !strings.Contains(got, "Footnote 1: the note.") {
		t.Errorf("footnote should be emitted; got %q", got)
	}
	if strings.Contains(got, "[1]") {
		t.Errorf("[1] should be stripped; got %q", got)
	}
}

func TestWeaveFootnotes_EmptyInput(t *testing.T) {
	got, stats := WeaveFootnotes("", map[int]string{})
	if got != "" {
		t.Errorf("empty input should return empty; got %q", got)
	}
	if len(stats.Unknown) != 0 || len(stats.Unreferenced) != 0 {
		t.Errorf("stats should be empty; got %+v", stats)
	}
}

// Integration test: Parse → SplitNotes → ParseFootnotes → StripMarkdown
// → WeaveFootnotes pipeline. Verifies the contract between all five
// functions.
func TestWeaveFootnotes_FullPipeline(t *testing.T) {
	const essay = `# Essay

## Metadata
- Author: x

## Full Document
Working hard isn't enough[[1](https://example.com#f1n)]. You have to work on the right things[[2](https://example.com#f2n)].

A second paragraph references [[1](https://example.com#f1n)] again.

## Notes

[1] By 'hard' I mean ... long hours.

[2] Choosing the topic matters more than effort.
`
	parsed, err := Parse(essay, "essay.md")
	if err != nil {
		t.Fatal(err)
	}
	prose, notesPart := SplitNotes(parsed.RawBody)
	footnotes := ParseFootnotes(notesPart)
	cleaned, _ := StripMarkdown(prose)
	woven, stats := WeaveFootnotes(cleaned, footnotes)

	wantContains := []string{
		"Working hard isn't enough.",
		"You have to work on the right things.",
		"Footnote 1: By 'hard' I mean ... long hours.",
		"Footnote 2: Choosing the topic matters more than effort.",
		"A second paragraph references  again.",
	}
	for _, frag := range wantContains {
		if !strings.Contains(woven, frag) {
			t.Errorf("woven output missing fragment %q; got %q", frag, woven)
		}
	}
	if strings.Contains(woven, "[1]") || strings.Contains(woven, "[2]") {
		t.Errorf("all markers should be stripped after weave; got %q", woven)
	}
	if strings.Contains(woven, "https://example.com") {
		t.Errorf("link URLs should have been stripped earlier in pipeline; got %q", woven)
	}
	// Footnote 1 referenced in two paragraphs → emitted twice.
	if strings.Count(woven, "Footnote 1:") != 2 {
		t.Errorf("Footnote 1 should emit once per referencing paragraph (= 2); got %d", strings.Count(woven, "Footnote 1:"))
	}
	// Footnote 2 referenced once → emitted once.
	if strings.Count(woven, "Footnote 2:") != 1 {
		t.Errorf("Footnote 2 should emit once; got %d", strings.Count(woven, "Footnote 2:"))
	}
	if len(stats.Unknown) != 0 || len(stats.Unreferenced) != 0 {
		t.Errorf("stats should be empty for well-formed input; got %+v", stats)
	}
}

func TestWeaveFootnotes_UnreferencedSorted(t *testing.T) {
	prose := "No markers here."
	footnotes := map[int]string{5: "e.", 1: "a.", 3: "c.", 2: "b."}
	_, stats := WeaveFootnotes(prose, footnotes)
	want := []int{1, 2, 3, 5}
	if !reflect.DeepEqual(stats.Unreferenced, want) {
		t.Errorf("Unreferenced should be ascending; got %v want %v", stats.Unreferenced, want)
	}
}
