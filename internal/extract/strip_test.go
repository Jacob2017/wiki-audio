package extract

import (
	"reflect"
	"strings"
	"testing"
)

func TestStripMarkdown_ImagesRemovedEntirely(t *testing.T) {
	cases := []struct{ in, want string }{
		{"before ![alt](https://x.png) after", "before  after"},
		{"![](url-only)", ""},
		{"![rw-book-cover](https://news.ycombinator.com/favicon.ico)", ""},
		{"text\n![cover](url)\nmore text", "text\n\nmore text"},
		{"![one](u1) and ![two](u2)", " and "},
	}
	for _, c := range cases {
		got, skipped := StripMarkdown(c.in)
		if got != c.want {
			t.Errorf("StripMarkdown(%q) cleaned = %q, want %q", c.in, got, c.want)
		}
		if skipped != nil {
			t.Errorf("StripMarkdown(%q) skipped = %v, want nil", c.in, skipped)
		}
	}
}

func TestStripMarkdown_LinkKeepsTextDropsURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[click here](https://example.com)", "click here"},
		{"see [Y Combinator](http://ycombinator.com/apply.html) for details", "see Y Combinator for details"},
		{"[](empty-text)", ""},
		{"two [first](u1) and [second](u2) links", "two first and second links"},
	}
	for _, c := range cases {
		got, _ := StripMarkdown(c.in)
		if got != c.want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripMarkdown_PGFootnoteRefCollapsesToBracketN(t *testing.T) {
	in := "If you're excited, even if other people aren't [[3](https://paulgraham.com/greatwork.html#f3n)] in fact."
	got, _ := StripMarkdown(in)
	want := "If you're excited, even if other people aren't [3] in fact."
	if got != want {
		t.Errorf("PG footnote ref collapse:\n got: %q\nwant: %q", got, want)
	}
}

func TestStripMarkdown_WikilinkDisplayPipe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"see [[some-slug|display text]] here", "see display text here"},
		{"[[a|b]]", "b"},
		{"prefix [[x|y]] middle [[z|w]] suffix", "prefix y middle w suffix"},
	}
	for _, c := range cases {
		got, _ := StripMarkdown(c.in)
		if got != c.want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripMarkdown_WikilinkSlugReplacesHyphensWithSpaces(t *testing.T) {
	cases := []struct{ in, want string }{
		{"[[wiki-link]]", "wiki link"},
		{"[[paul-graham]]", "paul graham"},
		{"[[single]]", "single"},
		{"[[multi-word-slug]] in text", "multi word slug in text"},
	}
	for _, c := range cases {
		got, _ := StripMarkdown(c.in)
		if got != c.want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripMarkdown_CodeFenceDroppedAndRecorded(t *testing.T) {
	in := "before paragraph.\n\n```\ncode line one\ncode line two\n```\n\nafter paragraph.\n"
	got, skipped := StripMarkdown(in)
	if strings.Contains(got, "code line") || strings.Contains(got, "```") {
		t.Errorf("fenced block should be dropped; got %q", got)
	}
	if !strings.Contains(got, "before paragraph.") || !strings.Contains(got, "after paragraph.") {
		t.Errorf("surrounding text should survive; got %q", got)
	}
	want := []string{"code_block"}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}

func TestStripMarkdown_CodeFenceLanguageTagRecorded(t *testing.T) {
	in := "intro.\n\n```python\nprint(\"hi\")\n```\n\noutro.\n"
	got, skipped := StripMarkdown(in)
	if strings.Contains(got, "print") {
		t.Errorf("code body should be dropped; got %q", got)
	}
	want := []string{"code_block:python"}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}

func TestStripMarkdown_MultipleCodeFencesRecordedInOrder(t *testing.T) {
	in := "```js\na\n```\n\nbetween.\n\n```py\nb\n```\n\nend.\n```\nc\n```\n"
	_, skipped := StripMarkdown(in)
	want := []string{"code_block:js", "code_block:py", "code_block"}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}

func TestStripMarkdown_InlineBackticksPreserved(t *testing.T) {
	// Single-backtick `inline code` is NOT a fenced block; the spec
	// only covers triple-backtick fences. Inline backticks survive.
	cases := []string{
		"use `os.ReadFile` for that",
		"the `[[1]]` syntax is for wikilinks",
		"`a` and `b` are inline",
	}
	for _, in := range cases {
		got, skipped := StripMarkdown(in)
		if !strings.Contains(got, "`") {
			t.Errorf("inline backticks should survive in %q; got %q", in, got)
		}
		if skipped != nil {
			t.Errorf("inline backticks should not be recorded as skipped; got %v", skipped)
		}
	}
}

func TestStripMarkdown_NoMarkdownNoiseUnchanged(t *testing.T) {
	in := "Plain prose with no markdown noise at all.\n\nMultiple paragraphs.\n"
	got, skipped := StripMarkdown(in)
	if got != in {
		t.Errorf("plain prose should round-trip; got %q, want %q", got, in)
	}
	if skipped != nil {
		t.Errorf("skipped should be nil for plain prose; got %v", skipped)
	}
}

func TestStripMarkdown_EmptyInput(t *testing.T) {
	got, skipped := StripMarkdown("")
	if got != "" {
		t.Errorf("empty input should return empty; got %q", got)
	}
	if skipped != nil {
		t.Errorf("empty input should return nil skipped; got %v", skipped)
	}
}

func TestStripMarkdown_OrderHandlesImageLinkInteraction(t *testing.T) {
	// "![alt](url)" must not be processed as a link AFTER step a
	// drops the entire image. Verify there is no leftover [alt]
	// debris after stripping.
	in := "![cover](u) and [text](u2)"
	got, _ := StripMarkdown(in)
	want := " and text"
	if got != want {
		t.Errorf("image+link interaction: got %q, want %q", got, want)
	}
}

func TestStripMarkdown_FullPipeline(t *testing.T) {
	const essay = `# Synthetic Essay

![rw-book-cover](https://example.com/cover.png)

## Metadata
- Author: [[Paul Graham]]
- URL: https://example.com

## Full Document
First paragraph with [a link](https://example.com) and [[wiki-target|wikitext]].

A paragraph with footnote ref [[1](https://example.com#f1n)] inline.

` + "```python\nprint('drop me')\n```" + `

Last paragraph after the code block.

## Notes

[1] First note with [[paul-graham]] reference.
`
	parsed, err := Parse(essay, "synthetic.md")
	if err != nil {
		t.Fatal(err)
	}
	prose, notes := SplitNotes(parsed.RawBody)
	cleaned, skipped := StripMarkdown(prose)

	if strings.Contains(cleaned, "rw-book-cover") {
		t.Error("cover image should be stripped")
	}
	if strings.Contains(cleaned, "https://example.com") {
		t.Error("link URLs should be stripped")
	}
	if !strings.Contains(cleaned, "a link") {
		t.Error("link display text should survive")
	}
	if !strings.Contains(cleaned, "wikitext") {
		t.Error("wikilink display half should survive")
	}
	if strings.Contains(cleaned, "wiki-target") {
		t.Error("wikilink slug half should be replaced")
	}
	if !strings.Contains(cleaned, "[1]") {
		t.Error("PG footnote ref should collapse to [1] for wa-kyn.8")
	}
	if strings.Contains(cleaned, "drop me") {
		t.Error("fenced code block should be dropped")
	}
	if !reflect.DeepEqual(skipped, []string{"code_block:python"}) {
		t.Errorf("skipped = %v, want [code_block:python]", skipped)
	}

	// Notes side preserves "[[paul-graham]]" until after wa-kyn.6
	// has parsed it; wa-kyn.7 only operates on prose. Verify the
	// caller's separation: we did NOT touch notes here.
	if !strings.Contains(notes, "[[paul-graham]]") {
		t.Error("Notes section should not be touched by StripMarkdown (caller's contract)")
	}
}
