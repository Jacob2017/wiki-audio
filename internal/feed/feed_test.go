package feed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// fixtureChannel returns a Channel populated with values that satisfy
// validate(); tests can shadow individual fields as needed.
func fixtureChannel() Channel {
	return Channel{
		Title:       "PG Essays",
		Description: "Paul Graham essays read aloud.",
		Author:      "Paul Graham",
		OwnerEmail:  "owner@example.test",
		Language:    "en-us",
		Link:        "https://feed.example.test",
		SelfLinkURL: "https://feed.example.test/pg.xml?t=ABC",
	}
}

// entry returns a fully-published ManifestEntry with the given slug,
// title, duration, file size, and publication time. Use this in tests
// instead of constructing the struct inline so any future field
// addition lands in one place.
func entry(slug, title string, dur float64, size int64, pub time.Time) model.ManifestEntry {
	p := pub
	return model.ManifestEntry{
		Slug:            slug,
		Title:           title,
		DurationSeconds: dur,
		FileSizeBytes:   size,
		R2Key:           "pg/" + slug + ".mp3",
		PublishedAt:     &p,
	}
}

// passthroughURL is the simplest enclosureURL builder — concatenate the
// channel link with the R2Key. wa-i1l.6 will replace this with a token-
// stamping variant; we keep tests independent of that layer.
func passthroughURL(linkPrefix string) func(model.ManifestEntry) string {
	return func(e model.ManifestEntry) string {
		return linkPrefix + "/" + e.R2Key
	}
}

// =====================================================================
// Smoke + happy-path
// =====================================================================

func TestGenerate_HappyPath_TwoEntries(t *testing.T) {
	pub1 := time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC)
	pub2 := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("how-to-do-great-work", "How to Do Great Work", 4924, 65437184, pub1),
		entry("high-agency", "High Agency", 1800, 24000000, pub2),
	}

	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)

	wantAll := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<rss version="2.0"`,
		` xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"`,
		` xmlns:atom="http://www.w3.org/2005/Atom"`,
		`<channel>`,
		`<title>PG Essays</title>`,
		`<atom:link rel="self" type="application/rss+xml" href="https://feed.example.test/pg.xml?t=ABC">`,
		`<itunes:author>Paul Graham</itunes:author>`,
		`<itunes:owner>`,
		`<itunes:name>Paul Graham</itunes:name>`,
		`<itunes:email>owner@example.test</itunes:email>`,
		`<itunes:explicit>false</itunes:explicit>`,
		`<itunes:category text="Technology">`,
		// Both items present
		`<title>How to Do Great Work</title>`,
		`<title>High Agency</title>`,
		`<enclosure url="https://feed.example.test/pg/how-to-do-great-work.mp3" length="65437184" type="audio/mpeg">`,
		`<enclosure url="https://feed.example.test/pg/high-agency.mp3" length="24000000" type="audio/mpeg">`,
		// Both guids in the canonical pg-<slug> shape with isPermaLink=false
		`<guid isPermaLink="false">pg-how-to-do-great-work</guid>`,
		`<guid isPermaLink="false">pg-high-agency</guid>`,
		`<itunes:duration>4924</itunes:duration>`,
		`<itunes:duration>1800</itunes:duration>`,
		`<pubDate>Fri, 08 May 2026 14:21:03 +0000</pubDate>`,
		`<pubDate>Sat, 09 May 2026 10:00:00 +0000</pubDate>`,
	}
	for _, w := range wantAll {
		if !strings.Contains(s, w) {
			t.Errorf("output missing fragment %q\nfull output:\n%s", w, s)
		}
	}
}

// =====================================================================
// Namespace URIs are load-bearing — pin exact bytes
// =====================================================================

func TestGenerate_NamespaceURIs_AreExact(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	out, err := Generate(
		fixtureChannel(),
		[]model.ManifestEntry{entry("a", "A", 60, 1000, pub)},
		passthroughURL("https://feed.example.test"),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)

	// Apple, Pocket Casts, and Overcast match by exact URI; a typo
	// here silently breaks subscribers. The bead's "Load-bearing
	// constants" section.
	if !strings.Contains(s, `xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"`) {
		t.Errorf("xmlns:itunes URI not exactly the expected literal")
	}
	if !strings.Contains(s, `xmlns:atom="http://www.w3.org/2005/Atom"`) {
		t.Errorf("xmlns:atom URI not exactly the expected literal")
	}
	if strings.Contains(s, `https://www.itunes.com/`) {
		t.Errorf("itunes namespace must use http, not https — Apple has not migrated this URI")
	}
	if !strings.Contains(s, `<rss version="2.0"`) {
		t.Errorf("rss must be version 2.0 (RSS 2.0, not Atom-as-primary)")
	}
}

// =====================================================================
// Round-trip: marshal then unmarshal preserves the load-bearing fields
// =====================================================================

func TestGenerate_RoundTrip_PreservesFields(t *testing.T) {
	pub1 := time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC)
	pub2 := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("how-to-do-great-work", "How to Do Great Work", 4924, 65437184, pub1),
		entry("high-agency", "High Agency", 1800, 24000000, pub2),
	}

	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Decode into a permissive shape that tracks the fields under test.
	var parsed struct {
		XMLName xml.Name `xml:"rss"`
		Version string   `xml:"version,attr"`
		Channel struct {
			Title    string `xml:"title"`
			AtomLink struct {
				Rel  string `xml:"rel,attr"`
				Href string `xml:"href,attr"`
			} `xml:"link"` // we don't care about namespace prefix on parse — Go strips it
			Items []struct {
				Title     string `xml:"title"`
				PubDate   string `xml:"pubDate"`
				Enclosure struct {
					URL    string `xml:"url,attr"`
					Length int64  `xml:"length,attr"`
					Type   string `xml:"type,attr"`
				} `xml:"enclosure"`
				Guid struct {
					IsPermaLink string `xml:"isPermaLink,attr"`
					Value       string `xml:",chardata"`
				} `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\noutput:\n%s", err, out)
	}

	if parsed.Version != "2.0" {
		t.Errorf("version: got %q want 2.0", parsed.Version)
	}
	if parsed.Channel.Title != "PG Essays" {
		t.Errorf("channel title: got %q", parsed.Channel.Title)
	}
	if got := len(parsed.Channel.Items); got != 2 {
		t.Fatalf("item count: got %d want 2", got)
	}

	// Newest first.
	first := parsed.Channel.Items[0]
	if first.Title != "High Agency" {
		t.Errorf("first item should be the newest (High Agency, pub 2026-05-09); got %q", first.Title)
	}
	if first.Guid.Value != "pg-high-agency" {
		t.Errorf("guid value: got %q want pg-high-agency", first.Guid.Value)
	}
	if first.Guid.IsPermaLink != "false" {
		t.Errorf("guid isPermaLink: got %q want \"false\" (RSS 2.0 default is true; clients GET the guid as a URL otherwise)", first.Guid.IsPermaLink)
	}
	if first.Enclosure.Length != 24000000 {
		t.Errorf("enclosure length: got %d want 24000000", first.Enclosure.Length)
	}
	if first.Enclosure.Type != "audio/mpeg" {
		t.Errorf("enclosure type: got %q want audio/mpeg", first.Enclosure.Type)
	}
	if want := "Sat, 09 May 2026 10:00:00 +0000"; first.PubDate != want {
		t.Errorf("pubDate: got %q want %q (RFC1123Z)", first.PubDate, want)
	}
}

// =====================================================================
// Edge: empty manifest → valid feed with no items
// =====================================================================

func TestGenerate_EmptyManifest_StillValidXML(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Must parse as XML.
	var anyDoc struct {
		XMLName xml.Name `xml:"rss"`
	}
	if err := xml.Unmarshal(out, &anyDoc); err != nil {
		t.Fatalf("empty-manifest output is not valid XML: %v\n%s", err, out)
	}
	// No items.
	if strings.Contains(string(out), "<item>") {
		t.Errorf("empty manifest produced an item element:\n%s", out)
	}
	// Channel skeleton still present.
	for _, frag := range []string{"<channel>", "<title>PG Essays</title>", "<itunes:author>Paul Graham</itunes:author>"} {
		if !strings.Contains(string(out), frag) {
			t.Errorf("empty-manifest output missing %q\n%s", frag, out)
		}
	}
}

// =====================================================================
// Edge: ineligible entries (nil PublishedAt or empty R2Key) skipped
// =====================================================================

func TestGenerate_SkipsIneligibleEntries(t *testing.T) {
	pubGood := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	good := entry("good", "Good", 60, 1000, pubGood)

	// nil PublishedAt
	pendingPub := model.ManifestEntry{Slug: "pending-pub", Title: "Pending Pub", R2Key: "pg/pending-pub.mp3"} // PublishedAt nil

	// empty R2Key
	pPub := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	pendingR2 := model.ManifestEntry{Slug: "pending-r2", Title: "Pending R2", PublishedAt: &pPub} // R2Key empty

	entries := []model.ManifestEntry{pendingPub, good, pendingR2}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)

	if !strings.Contains(s, "<title>Good</title>") {
		t.Errorf("eligible entry missing from output")
	}
	if strings.Contains(s, "Pending Pub") {
		t.Errorf("entry with nil PublishedAt should be skipped (wa-hv6 [HIGH]):\n%s", s)
	}
	if strings.Contains(s, "Pending R2") {
		t.Errorf("entry with empty R2Key should be skipped (wa-hv6 [HIGH]):\n%s", s)
	}
}

// =====================================================================
// Edge: 53 entries — all present, ordered newest first
// =====================================================================

func TestGenerate_53Entries_AllPresent_DescendingPubDate(t *testing.T) {
	const N = 53
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := make([]model.ManifestEntry, 0, N)
	for i := 0; i < N; i++ {
		// Stagger each entry one day later than the previous.
		entries = append(entries, entry(
			fmt.Sprintf("essay-%02d", i),
			fmt.Sprintf("Essay %d", i),
			float64(60+i),
			int64(1000+i),
			base.AddDate(0, 0, i),
		))
	}

	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var parsed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct {
				Title   string `xml:"title"`
				PubDate string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := len(parsed.Channel.Items); got != N {
		t.Fatalf("item count: got %d want %d", got, N)
	}

	// First item should be the latest (i=N-1 = essay-52); last item should be i=0 (essay-00).
	if parsed.Channel.Items[0].Title != fmt.Sprintf("Essay %d", N-1) {
		t.Errorf("first item: got %q want %q (newest first)", parsed.Channel.Items[0].Title, fmt.Sprintf("Essay %d", N-1))
	}
	if parsed.Channel.Items[N-1].Title != "Essay 0" {
		t.Errorf("last item: got %q want \"Essay 0\" (oldest last)", parsed.Channel.Items[N-1].Title)
	}

	// Strictly descending pubDates.
	prev := time.Time{}
	for i, item := range parsed.Channel.Items {
		t0, err := time.Parse(time.RFC1123Z, item.PubDate)
		if err != nil {
			t.Fatalf("item %d: parse pubDate %q: %v", i, item.PubDate, err)
		}
		if i > 0 && !prev.After(t0) {
			t.Errorf("item %d pubDate %v should be strictly older than prev %v (newest first)", i, t0, prev)
		}
		prev = t0
	}
}

// =====================================================================
// Tie-break: equal PublishedAt → slug-ascending; ensures byte-stable
// regeneration across runs (wa-hv6 [LOW] item-ordering)
// =====================================================================

func TestGenerate_EqualPublishedAt_TieBreaksOnSlug(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("zzz", "Zzz", 60, 1000, pub),
		entry("aaa", "Aaa", 60, 1000, pub),
		entry("mmm", "Mmm", 60, 1000, pub),
	}

	out1, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate 1: %v", err)
	}
	// Re-run on the SAME slice (sortByPubDateDesc sorts in place; the
	// second Generate sees an already-sorted input). Output must be
	// byte-identical so feed regeneration is deterministic.
	out2, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate 2: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Errorf("two consecutive Generate calls on same input differ:\n--- run1 ---\n%s\n--- run2 ---\n%s", out1, out2)
	}

	// Order should be aaa, mmm, zzz on equal pubDate.
	s := string(out1)
	iAaa := strings.Index(s, "<title>Aaa</title>")
	iMmm := strings.Index(s, "<title>Mmm</title>")
	iZzz := strings.Index(s, "<title>Zzz</title>")
	if !(iAaa < iMmm && iMmm < iZzz) {
		t.Errorf("equal-pubDate tie-break should be slug-ascending; got positions Aaa=%d Mmm=%d Zzz=%d", iAaa, iMmm, iZzz)
	}
}

// =====================================================================
// Validate
// =====================================================================

func TestGenerate_RejectsIncompleteChannel(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("a", "A", 60, 1000, pub)}

	cases := []struct {
		name  string
		mut   func(*Channel)
		field string
	}{
		{"missing Title", func(c *Channel) { c.Title = "" }, "Title"},
		{"missing Description", func(c *Channel) { c.Description = "" }, "Description"},
		{"missing Author", func(c *Channel) { c.Author = "" }, "Author"},
		{"missing OwnerEmail", func(c *Channel) { c.OwnerEmail = "" }, "OwnerEmail"},
		{"missing Language", func(c *Channel) { c.Language = "" }, "Language"},
		{"missing Link", func(c *Channel) { c.Link = "" }, "Link"},
		{"missing SelfLinkURL", func(c *Channel) { c.SelfLinkURL = "" }, "SelfLinkURL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := fixtureChannel()
			c.mut(&ch)
			_, err := Generate(ch, entries, passthroughURL("https://feed.example.test"))
			if err == nil {
				t.Fatalf("expected error for %s, got nil", c.field)
			}
			if !strings.Contains(err.Error(), c.field) {
				t.Errorf("error should name the missing field %q; got: %v", c.field, err)
			}
		})
	}
}

func TestGenerate_RejectsNilURLBuilder(t *testing.T) {
	_, err := Generate(fixtureChannel(), nil, nil)
	if err == nil {
		t.Fatal("expected error for nil enclosureURL builder, got nil")
	}
	if !strings.Contains(err.Error(), "enclosureURL") {
		t.Errorf("error should mention enclosureURL; got: %v", err)
	}
}

// =====================================================================
// Cover image — optional element, present iff CoverImage is set
// =====================================================================

func TestGenerate_CoverImage_OmittedWhenEmpty(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(out), "<itunes:image") {
		t.Errorf("itunes:image should be omitted when CoverImage is empty; got:\n%s", out)
	}
}

func TestGenerate_CoverImage_EmittedWhenSet(t *testing.T) {
	ch := fixtureChannel()
	ch.CoverImage = "https://art.example.test/cover.png"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `<itunes:image href="https://art.example.test/cover.png">`
	if !strings.Contains(string(out), want) {
		t.Errorf("itunes:image element missing or malformed; want %q in:\n%s", want, out)
	}
}

// =====================================================================
// Category default
// =====================================================================

func TestGenerate_Category_DefaultIsTechnology(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `<itunes:category text="Technology">`) {
		t.Errorf("default category should be Technology; got:\n%s", out)
	}
}

func TestGenerate_Category_OverrideHonored(t *testing.T) {
	ch := fixtureChannel()
	ch.Category = "Education"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `<itunes:category text="Education">`) {
		t.Errorf("Channel.Category override not honored; got:\n%s", out)
	}
}

// =====================================================================
// XML special chars in fields are escaped (defense against an
// operator setting Title to "Less < & More" and breaking the feed)
// =====================================================================

func TestGenerate_EscapesSpecialCharacters(t *testing.T) {
	ch := fixtureChannel()
	ch.Title = `Less < & "More"`
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	if strings.Contains(s, `Less < & "More"`) {
		t.Errorf("title should be XML-escaped, not raw:\n%s", s)
	}
	// Re-parse to confirm escapes round-trip back to the original value.
	var parsed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Channel.Title != `Less < & "More"` {
		t.Errorf("round-trip title mismatch: got %q want %q", parsed.Channel.Title, `Less < & "More"`)
	}
}
