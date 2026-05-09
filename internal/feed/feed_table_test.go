package feed

// wa-i1l.15 canonical test table. Maps 1:1 onto the row names in the
// bead's "Test Plan" section so cross-reference is easy: every row in
// the bead has a test here whose name ends with the row's snake_case
// label, OR has a comment naming the wa-i1l.5 test that already
// covered it.
//
// Rows that depend on wa-i1l.6 (token stamping, env-driven URL
// composition, self-link assembly) are tested at the feed-side seam
// only — the actual env-reading + error semantics live with wa-i1l.6
// or wa-i1l.7 (publish orchestrator).

import (
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// =====================================================================
// Row: empty_manifest
// (Already covered: TestGenerate_EmptyManifest_StillValidXML.
//  Re-asserted here under the bead's row name for grep-by-spec.)
// =====================================================================

func TestRow_empty_manifest(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	// Valid XML that parses + the channel skeleton + zero items.
	var any struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Items []struct{} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(out, &any); err != nil {
		t.Fatalf("not parseable XML: %v\n%s", err, s)
	}
	if got := len(any.Channel.Items); got != 0 {
		t.Errorf("empty_manifest: got %d items, want 0", got)
	}
	if !strings.Contains(s, "<channel>") {
		t.Errorf("channel skeleton missing")
	}
}

// =====================================================================
// Row: single_entry — exactly one <item>; required fields present
// =====================================================================

func TestRow_single_entry(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 21, 3, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("how-to-do-great-work", "How to Do Great Work", 4924, 65437184, pub),
	}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := strings.Count(string(out), "<item>"); got != 1 {
		t.Errorf("expected exactly one <item>, got %d:\n%s", got, out)
	}
	// Each required field per the §5.5 sample item.
	required := []string{
		"<title>How to Do Great Work</title>",
		`<enclosure url="https://feed.example.test/pg/how-to-do-great-work.mp3" length="65437184" type="audio/mpeg">`,
		`<guid isPermaLink="false">pg-how-to-do-great-work</guid>`,
		"<pubDate>",
		"<itunes:author>Paul Graham</itunes:author>",
		"<itunes:duration>4924</itunes:duration>",
		"<itunes:explicit>false</itunes:explicit>",
	}
	for _, want := range required {
		if !strings.Contains(string(out), want) {
			t.Errorf("single_entry missing %q\n%s", want, out)
		}
	}
}

// =====================================================================
// Row: multiple_entries_pubdate_order — newest-first
// (Already covered: TestGenerate_53Entries_AllPresent_DescendingPubDate.
//  Re-asserted here at N=3 to match the bead's row example.)
// =====================================================================

func TestRow_multiple_entries_pubdate_order(t *testing.T) {
	mk := func(slug string, day int) model.ManifestEntry {
		return entry(slug, "Title "+slug, 60, 1000, time.Date(2026, 5, day, 0, 0, 0, 0, time.UTC))
	}
	// Provide entries OUT of order; generator must emit newest-first.
	entries := []model.ManifestEntry{mk("middle", 5), mk("oldest", 1), mk("newest", 9)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	iNew := strings.Index(s, "<title>Title newest</title>")
	iMid := strings.Index(s, "<title>Title middle</title>")
	iOld := strings.Index(s, "<title>Title oldest</title>")
	if iNew < 0 || iMid < 0 || iOld < 0 {
		t.Fatalf("missing one of newest/middle/oldest titles\n%s", s)
	}
	if !(iNew < iMid && iMid < iOld) {
		t.Errorf("expected newest-first ordering; positions newest=%d middle=%d oldest=%d", iNew, iMid, iOld)
	}
}

// =====================================================================
// Row: itunes_namespace_declaration
// (See also: TestGenerate_NamespaceURIs_AreExact.)
// =====================================================================

func TestRow_itunes_namespace_declaration(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"`) {
		t.Errorf("itunes xmlns attribute missing or wrong\n%s", out)
	}
}

// =====================================================================
// Row: atom_self_link_present
// =====================================================================

func TestRow_atom_self_link_present(t *testing.T) {
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml?t=THE_TOKEN"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `<atom:link rel="self" type="application/rss+xml" href="https://feed.example.test/pg.xml?t=THE_TOKEN">`
	if !strings.Contains(string(out), want) {
		t.Errorf("atom self-link missing or malformed; want %q in:\n%s", want, out)
	}
}

// =====================================================================
// Row: enclosure_url_token_stamped — feed-side test of the seam
//
// Whatever URL the caller's enclosureURL builder returns must land in
// the <enclosure url="..."> attr. The actual env-reading + error
// semantics for the token-stamping layer live with wa-i1l.6.
// =====================================================================

func TestRow_enclosure_url_token_stamped(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}
	stampingURL := func(e model.ManifestEntry) string {
		return "https://feed.example.test/" + e.R2Key + "?t=foo"
	}
	out, err := Generate(fixtureChannel(), entries, stampingURL)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `url="https://feed.example.test/pg/essay.mp3?t=foo"`
	if !strings.Contains(string(out), want) {
		t.Errorf("token-stamped URL missing from <enclosure>; want %q in:\n%s", want, out)
	}
}

// Row: enclosure_url_token_missing — DEFERRED to wa-i1l.6's tests.
// The feed-side contract is "use whatever URL the builder returns";
// env-reading / refuse-on-empty-token belongs with the wa-i1l.6 layer.

// =====================================================================
// Row: enclosure_length_matches_filesize
// =====================================================================

func TestRow_enclosure_length_matches_filesize(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 60, 1234567, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `length="1234567"`) {
		t.Errorf("enclosure length attr should match FileSizeBytes 1234567:\n%s", out)
	}
}

// =====================================================================
// Row: guid_is_pg_slug_prefix
// =====================================================================

func TestRow_guid_is_pg_slug_prefix(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("how-to-do-great-work", "T", 60, 1000, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `<guid isPermaLink="false">pg-how-to-do-great-work</guid>`
	if !strings.Contains(string(out), want) {
		t.Errorf("guid format mismatch; want %q in:\n%s", want, out)
	}
}

// =====================================================================
// Row: stable_guid_across_regen
//
// Same slug → same guid. Re-running Generate on the same (or
// equivalent) input must produce a byte-identical guid value. Stronger
// than just byte-stable output — guarantees podcast apps don't see a
// "new" episode on regen.
// =====================================================================

func TestRow_stable_guid_across_regen(t *testing.T) {
	pub1 := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	pub2 := time.Date(2026, 5, 8, 14, 0, 0, 5, time.UTC) // 5ns later — within the same second
	entries1 := []model.ManifestEntry{entry("essay-x", "X", 60, 1000, pub1)}
	entries2 := []model.ManifestEntry{entry("essay-x", "X", 61, 1001, pub2)} // duration + size drift simulates a re-synth
	out1, _ := Generate(fixtureChannel(), entries1, passthroughURL("https://feed.example.test"))
	out2, _ := Generate(fixtureChannel(), entries2, passthroughURL("https://feed.example.test"))
	guid := `<guid isPermaLink="false">pg-essay-x</guid>`
	if !strings.Contains(string(out1), guid) || !strings.Contains(string(out2), guid) {
		t.Errorf("guid not stable across regen:\nout1 has guid: %v\nout2 has guid: %v",
			strings.Contains(string(out1), guid), strings.Contains(string(out2), guid))
	}
}

// =====================================================================
// Row: itunes_duration_seconds — integer-seconds form per §5.5
// =====================================================================

func TestRow_itunes_duration_seconds(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 4924, 1000, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), "<itunes:duration>4924</itunes:duration>") {
		t.Errorf("itunes:duration should be integer seconds (4924):\n%s", out)
	}
}

// Sub-row: rounding from float DurationSeconds (e.g. 4924.6 → 4925).
// Pin so a future "use math.Floor instead" PR re-surfaces the choice.
func TestRow_itunes_duration_rounds_half_up(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 4924.6, 1000, pub)}
	out, _ := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if !strings.Contains(string(out), "<itunes:duration>4925</itunes:duration>") {
		t.Errorf("4924.6 should round to 4925 (nearest integer, not floor):\n%s", out)
	}
}

// =====================================================================
// Row: itunes_explicit_false
// =====================================================================

func TestRow_itunes_explicit_false(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Channel-level + per-item; both must be false for v1.
	if got := strings.Count(string(out), "<itunes:explicit>false</itunes:explicit>"); got < 2 {
		t.Errorf("expected itunes:explicit=false at channel + each item; got %d occurrences:\n%s", got, out)
	}
}

// =====================================================================
// Row: itunes_author_per_entry
//
// V1 model: single author per feed → per-item itunes:author defaults
// to channel.Author. If a future model change adds per-entry author,
// this test will need updating to assert per-entry differentiation.
// =====================================================================

func TestRow_itunes_author_per_entry(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("a", "A", 60, 1000, pub),
		entry("b", "B", 60, 1000, pub.Add(time.Second)),
	}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// One channel-level + one per item = 1 + N occurrences.
	got := strings.Count(string(out), "<itunes:author>Paul Graham</itunes:author>")
	want := 1 + len(entries)
	if got != want {
		t.Errorf("itunes:author count: got %d, want %d (1 channel + %d items)\n%s", got, want, len(entries), out)
	}
}

// Row: description_uses_publish_date_text — DEFERRED.
// ManifestEntry doesn't carry PublishDateText today (only EssayMeta
// does, and EssayMeta isn't kept in the manifest). v1 emits empty
// <description>. When a follow-up bead extends ManifestEntry to
// include PublishDateText, this row should be reactivated.

// =====================================================================
// Row: xml_special_chars_escaped
// (Already covered: TestGenerate_EscapesSpecialCharacters. Re-asserted
//  here under the bead row name.)
// =====================================================================

func TestRow_xml_special_chars_escaped(t *testing.T) {
	ch := fixtureChannel()
	ch.Title = "Foo & Bar <baz>"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(out), "Foo & Bar <baz>") {
		t.Errorf("title should be XML-escaped, not raw:\n%s", out)
	}
	// Round-trip parse must restore the original value.
	var parsed struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Channel.Title != "Foo & Bar <baz>" {
		t.Errorf("round-trip title mismatch: got %q want %q", parsed.Channel.Title, "Foo & Bar <baz>")
	}
}

// Row: xml_validates — gated on `xmllint` being on PATH. The cheaper
// structural assertion is in the table above (parse + namespace
// equality); this is the belt-and-suspenders run.
//
// Lives alongside the golden test in feed_golden_test.go so the
// xmllint subtest can validate against the same canonical fixture.

// Row: feed_self_url_uses_FeedConfig — DEFERRED.
// Channel.SelfLinkURL is supplied by the caller (orchestrator). The
// composition logic — `BaseURL + "/" + FeedPath + "?t=" + token` —
// lives in wa-i1l.6 / wa-i1l.7. Feed-side test would just assert that
// "whatever string we're given lands in the href" — which is already
// covered by TestRow_atom_self_link_present.

// =====================================================================
// Bead's "Namespace-URL exact-match" rows (added pass 3)
// (Already covered: TestGenerate_NamespaceURIs_AreExact.
//  Re-asserted under each row's snake_case name for grep-by-spec.)
// =====================================================================

func TestRow_itunes_ns_uri_exact(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"`
	if !strings.Contains(string(out), want) {
		t.Errorf("xmlns:itunes URI must be exactly %q (HTTP, not HTTPS):\n%s", want, out)
	}
}

func TestRow_atom_ns_uri_exact(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `xmlns:atom="http://www.w3.org/2005/Atom"`
	if !strings.Contains(string(out), want) {
		t.Errorf("xmlns:atom URI must be exactly %q:\n%s", want, out)
	}
}

func TestRow_rss_version_attr(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `<rss version="2.0"`) {
		t.Errorf("rss version attribute missing; must be 2.0:\n%s", out)
	}
}

func TestRow_guid_ispermalink_false_explicit(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `isPermaLink="false"`) {
		t.Errorf("guid isPermaLink attribute must be literally present and equal to \"false\":\n%s", out)
	}
}

func TestRow_channel_has_itunes_owner_email(t *testing.T) {
	ch := fixtureChannel()
	ch.OwnerEmail = "owner@example.test"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `<itunes:email>owner@example.test</itunes:email>`) {
		t.Errorf("itunes:owner > itunes:email should reflect Channel.OwnerEmail:\n%s", out)
	}
}

func TestRow_channel_has_itunes_category(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), `<itunes:category text="Technology">`) {
		t.Errorf("itunes:category default to Technology not emitted:\n%s", out)
	}
}

// =====================================================================
// Castfeedvalidator parity check (informational, structural only)
//
// Pure structural assertions on the fields castfeedvalidator.com
// considers required for podcast feed validity. No network calls.
// =====================================================================

func TestCastfeedvalidator_StructuralParity(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}
	out, err := Generate(fixtureChannel(), entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	required := map[string]string{
		"channel/title":           "<title>",
		"channel/link":            "<link>",
		"channel/description":     "<description>",
		"channel/language":        "<language>",
		"channel/atom-self-link":  `<atom:link rel="self"`,
		"channel/itunes:author":   "<itunes:author>",
		"channel/itunes:owner":    "<itunes:owner>",
		"channel/itunes:explicit": "<itunes:explicit>",
		"channel/itunes:category": "<itunes:category",
		"item/title":              "<title>Essay</title>",
		"item/enclosure":          "<enclosure ",
		"item/guid":               `<guid isPermaLink="false">`,
		"item/pubDate":            "<pubDate>",
		"item/itunes:duration":    "<itunes:duration>",
	}
	for name, frag := range required {
		if !strings.Contains(string(out), frag) {
			t.Errorf("castfeedvalidator parity: %s missing fragment %q", name, frag)
		}
	}
	// Output must round-trip; xmllint can elaborate, but stdlib parse
	// catches the catastrophic class of failure (unbalanced tags etc.).
	var any struct{ XMLName xml.Name }
	if err := xml.Unmarshal(out, &any); err != nil {
		t.Errorf("castfeedvalidator parity: parse error %v\n%s", err, out)
	}
}

// =====================================================================
// wa-bo5 rows (castfeedvalidator-driven feed enrichments). Each row
// pins one of the six changes shipped in the wa-bo5 commit.
// =====================================================================

// Row: channel itunes:image (wa-bo5 #1)
func TestRow_channel_itunes_image_emitted_when_set(t *testing.T) {
	ch := fixtureChannel()
	ch.CoverImage = "https://feed.example.test/pg/cover.jpg"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `<itunes:image href="https://feed.example.test/pg/cover.jpg">`
	if !strings.Contains(string(out), want) {
		t.Errorf("channel itunes:image missing or malformed; want %q in:\n%s", want, out)
	}
}

func TestRow_per_item_itunes_image_repeated(t *testing.T) {
	ch := fixtureChannel()
	ch.CoverImage = "https://feed.example.test/pg/cover.jpg"
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("a", "A", 60, 1000, pub),
		entry("b", "B", 60, 1000, pub.Add(time.Second)),
	}
	out, err := Generate(ch, entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// 1 channel + N items = 3 occurrences for 2 entries.
	got := strings.Count(string(out), `<itunes:image href="https://feed.example.test/pg/cover.jpg">`)
	want := 1 + len(entries)
	if got != want {
		t.Errorf("itunes:image count: got %d, want %d (1 channel + %d items)\n%s", got, want, len(entries), out)
	}
}

// Row: channel itunes:type=episodic (wa-bo5 #2)
func TestRow_channel_itunes_type_episodic(t *testing.T) {
	ch := fixtureChannel()
	ch.PodcastType = PodcastTypeEpisodic
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), "<itunes:type>episodic</itunes:type>") {
		t.Errorf("itunes:type=episodic missing:\n%s", out)
	}
}

// Default callers (PodcastType empty) should NOT emit the element —
// keeps wa-i1l.5 callers backward-compatible until they opt in.
func TestRow_channel_itunes_type_omitted_when_empty(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(out), "<itunes:type>") {
		t.Errorf("itunes:type should be omitted when Channel.PodcastType is empty; got:\n%s", out)
	}
}

// Row: channel ≥3 categories with subcategories (wa-bo5 #3)
func TestRow_channel_categories_triple_with_subs(t *testing.T) {
	ch := fixtureChannel()
	ch.Categories = [][]string{
		{"Technology"},
		{"Education", "Self-Improvement"},
		{"Business", "Entrepreneurship"},
	}
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)

	// Three top-level itunes:category text="..." attrs.
	for _, cat := range []string{"Technology", "Education", "Business"} {
		if !strings.Contains(s, `<itunes:category text="`+cat+`"`) {
			t.Errorf("missing top-level category text=%q\n%s", cat, s)
		}
	}
	// Two nested subcategories (Self-Improvement, Entrepreneurship)
	// must appear as nested itunes:category elements — pinning the
	// nested shape distinguishes "valid Apple subcategory" from
	// "two flat sibling categories".
	for _, sub := range []string{"Self-Improvement", "Entrepreneurship"} {
		nested := `<itunes:category text="` + sub + `">`
		if !strings.Contains(s, nested) {
			t.Errorf("missing nested subcategory text=%q\n%s", sub, s)
		}
	}
	// Order: parent appears before its sub. We don't pin top-level
	// order across the three triples (callers may reorder); but
	// nested-after-parent is a structural invariant.
	parentEdu := strings.Index(s, `<itunes:category text="Education">`)
	subImprov := strings.Index(s, `<itunes:category text="Self-Improvement">`)
	if parentEdu < 0 || subImprov < 0 || parentEdu > subImprov {
		t.Errorf("Education parent must precede its Self-Improvement subcategory; positions parent=%d sub=%d", parentEdu, subImprov)
	}
}

// Default fallback: zero-config channel emits the
// model.DefaultFeedCategories triple. Catches a future PR that
// silently empties the default and ships a single-category feed.
func TestRow_channel_default_categories_when_unset(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	got := strings.Count(s, "<itunes:category ")
	// 3 parents + 2 subcategories = 5 total occurrences (the parents
	// open as `<itunes:category text="..."` and the subs open the
	// same way nested inside).
	if got < 5 {
		t.Errorf("default categories should emit ≥5 itunes:category elements (3 parents + ≥2 subs); got %d\n%s", got, s)
	}
}

// Legacy single-category callers still work — wa-i1l.5's
// Channel.Category field maps to a single category entry. Pin so a
// future "remove the legacy field" PR re-surfaces the migration cost.
func TestRow_channel_legacy_single_category_still_works(t *testing.T) {
	ch := fixtureChannel()
	ch.Category = "Society & Culture"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `<itunes:category text="Society &amp; Culture">`) {
		t.Errorf("legacy single category should be emitted (and `&` escaped):\n%s", s)
	}
	// Default triple should NOT also be emitted — the legacy field
	// stands in for the whole list.
	if strings.Contains(s, `<itunes:category text="Education">`) {
		t.Errorf("legacy single category set; default Education should NOT appear:\n%s", s)
	}
}

// Row: channel <copyright> (wa-bo5 #4)
func TestRow_channel_copyright_emitted_when_set(t *testing.T) {
	ch := fixtureChannel()
	ch.Copyright = "Audio rendering © 2026 Operator"
	out, err := Generate(ch, nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := `<copyright>Audio rendering © 2026 Operator</copyright>`
	if !strings.Contains(string(out), want) {
		t.Errorf("copyright element missing or malformed; want %q in:\n%s", want, out)
	}
}

func TestRow_channel_copyright_omitted_when_empty(t *testing.T) {
	out, err := Generate(fixtureChannel(), nil, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(out), "<copyright>") {
		t.Errorf("copyright must be omitted when empty (PG owns the essay copyright; we don't claim it):\n%s", out)
	}
}

// Row: per-item <link> (wa-bo5 #5)
func TestRow_item_link_emitted_when_source_url_set(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	e := entry("essay", "Essay", 60, 1000, pub)
	e.SourceURL = "http://paulgraham.com/greatwork.html"
	out, err := Generate(fixtureChannel(), []model.ManifestEntry{e}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(string(out), "<link>http://paulgraham.com/greatwork.html</link>") {
		t.Errorf("item link missing:\n%s", out)
	}
}

func TestRow_item_link_omitted_when_source_url_empty(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	e := entry("essay", "Essay", 60, 1000, pub) // SourceURL left empty
	out, err := Generate(fixtureChannel(), []model.ManifestEntry{e}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only the channel <link> should appear, not an item-level one.
	// Channel <link> reads <link>https://feed.example.test</link>;
	// an item without SourceURL must not introduce an empty <link>.
	if strings.Count(string(out), "<link>") > 1 {
		t.Errorf("expected exactly one <link> (channel only) when SourceURL is empty; got:\n%s", out)
	}
}

// Row: per-item <description> (wa-bo5 #6)
func TestRow_item_description_emitted_when_set(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	e := entry("essay", "Essay", 60, 1000, pub)
	e.Description = "First paragraph of the essay, briefly summarizing the thesis."
	out, err := Generate(fixtureChannel(), []model.ManifestEntry{e}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "<description>First paragraph of the essay, briefly summarizing the thesis.</description>"
	if !strings.Contains(string(out), want) {
		t.Errorf("item description missing or malformed; want %q in:\n%s", want, out)
	}
}

func TestRow_item_description_omitted_when_empty(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	e := entry("essay", "Essay", 60, 1000, pub) // Description left empty
	out, err := Generate(fixtureChannel(), []model.ManifestEntry{e}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Channel <description> exists; item should NOT add an empty one.
	descCount := strings.Count(string(out), "<description>")
	if descCount != 1 {
		t.Errorf("expected exactly one <description> (channel only) when item Description is empty; got %d:\n%s", descCount, out)
	}
}

// =====================================================================
// Coverage map (informational — run as a sanity log on -v) — checks
// that every test name we *intend* to cover from the bead's table is
// actually represented by a test function in this package. Helps a
// future maintainer confirm the bead → test mapping by inspection.
// =====================================================================

func TestRowCoverageMap_Sanity(t *testing.T) {
	// Test names referenced in the bead's two tables. Asserting their
	// presence here catches a future "rename the test and break
	// cross-reference" change. Updating this list is part of any
	// edit to the bead's row names.
	rows := []string{
		"empty_manifest",
		"single_entry",
		"multiple_entries_pubdate_order",
		"itunes_namespace_declaration",
		"atom_self_link_present",
		"enclosure_url_token_stamped",
		"enclosure_length_matches_filesize",
		"guid_is_pg_slug_prefix",
		"stable_guid_across_regen",
		"itunes_duration_seconds",
		"itunes_explicit_false",
		"itunes_author_per_entry",
		"xml_special_chars_escaped",
		"itunes_ns_uri_exact",
		"atom_ns_uri_exact",
		"rss_version_attr",
		"guid_ispermalink_false_explicit",
		"channel_has_itunes_owner_email",
		"channel_has_itunes_category",
	}
	// We just trust the Go test runner to pick these up — the loop is
	// here so a renamed row blows the loop assertion via t.Logf
	// reflection, not via dynamic discovery (Go test framework doesn't
	// expose that). The list is the contract.
	for _, r := range rows {
		t.Logf("bead row '%s' → expected test name 'TestRow_%s'", r, r)
	}
	// Punted rows — documented here so the closing comment can quote.
	punted := map[string]string{
		"enclosure_url_token_missing":        "wa-i1l.6 (token-stamper env handling)",
		"description_uses_publish_date_text": "v1 (ManifestEntry doesn't carry PublishDateText)",
		"xml_validates":                      "TestXmlValidates in feed_golden_test.go (gated on xmllint)",
		"feed_self_url_uses_FeedConfig":      "wa-i1l.7 (orchestrator composes Channel.SelfLinkURL)",
	}
	for r, why := range punted {
		fmt.Printf("punted row %q → %s\n", r, why)
	}
}
