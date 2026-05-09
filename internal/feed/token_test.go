package feed

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

func TestStampTokens_StampsEveryEnclosure(t *testing.T) {
	const n = 53
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := make([]model.ManifestEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, entry(
			fmt.Sprintf("essay-%02d", i),
			fmt.Sprintf("Essay %d", i),
			float64(60+i),
			int64(1000+i),
			base.AddDate(0, 0, i),
		))
	}

	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml"

	feedXML, err := Generate(ch, entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	stamped := StampTokens(feedXML, "tok-123")

	var parsed struct {
		Channel struct {
			Items []struct {
				Enclosure struct {
					URL string `xml:"url,attr"`
				} `xml:"enclosure"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(stamped, &parsed); err != nil {
		t.Fatalf("unmarshal stamped feed: %v", err)
	}
	if got := len(parsed.Channel.Items); got != n {
		t.Fatalf("item count: got %d want %d", got, n)
	}
	for i, item := range parsed.Channel.Items {
		u, err := url.Parse(item.Enclosure.URL)
		if err != nil {
			t.Fatalf("item %d enclosure parse: %v", i, err)
		}
		if got := u.Query().Get("t"); got != "tok-123" {
			t.Fatalf("item %d token: got %q want %q", i, got, "tok-123")
		}
	}
}

func TestStampTokens_StampsSelfLink(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml"

	feedXML, err := Generate(ch, []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	stamped := StampTokens(feedXML, "self-token")
	want := `href="https://feed.example.test/pg.xml?t=self-token"`
	if !strings.Contains(string(stamped), want) {
		t.Fatalf("self-link missing token stamp; want fragment %q in:\n%s", want, stamped)
	}
}

func TestStampTokens_PreservesExistingQueryParams(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml?other=foo&x=1"

	feedXML, err := Generate(ch, []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}, func(e model.ManifestEntry) string {
		return "https://feed.example.test/" + e.R2Key + "?other=foo&x=1"
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	stamped := string(StampTokens(feedXML, "query-token"))
	wantSelf := `href="https://feed.example.test/pg.xml?other=foo&amp;t=query-token&amp;x=1"`
	wantEnclosure := `url="https://feed.example.test/pg/essay.mp3?other=foo&amp;t=query-token&amp;x=1"`

	if !strings.Contains(stamped, wantSelf) {
		t.Fatalf("self-link query params not preserved; want %q in:\n%s", wantSelf, stamped)
	}
	if !strings.Contains(stamped, wantEnclosure) {
		t.Fatalf("enclosure query params not preserved; want %q in:\n%s", wantEnclosure, stamped)
	}
}

func TestStampTokens_EmptyTokenReturnsInputAndWarns(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml"

	feedXML, err := Generate(ch, []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	out := StampTokens(feedXML, "")
	if !bytes.Equal(out, feedXML) {
		t.Fatalf("empty token should return input unchanged")
	}
	if !strings.Contains(logs.String(), "access token empty") {
		t.Fatalf("expected warning log for empty token; got:\n%s", logs.String())
	}
}

func TestStampTokensFromEnv_EmptyTokenErrors(t *testing.T) {
	t.Setenv(accessTokenEnvVar, "")
	_, err := StampTokensFromEnv([]byte("<rss/>"))
	if err == nil {
		t.Fatal("expected error for empty token env")
	}
	if !strings.Contains(err.Error(), accessTokenEnvVar) {
		t.Fatalf("error should mention %s; got %v", accessTokenEnvVar, err)
	}
}

// wa-bo5: itunes:image href is also gated by the Worker token
// (R2 fronts the cover art), so StampTokens must rewrite it just like
// enclosure URLs and the atom:link self URL. Without the rewrite, a
// podcast client would 403 fetching channel artwork.
func TestStampTokens_StampsItunesImage(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml"
	ch.CoverImage = "https://feed.example.test/pg/cover.jpg"

	entries := []model.ManifestEntry{
		entry("essay-1", "Essay 1", 60, 1000, pub),
		entry("essay-2", "Essay 2", 60, 1000, pub.Add(time.Second)),
	}

	feedXML, err := Generate(ch, entries, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Sanity: pre-stamp, the cover URL is bare (no ?t=).
	if !strings.Contains(string(feedXML), `<itunes:image href="https://feed.example.test/pg/cover.jpg">`) {
		t.Fatalf("pre-stamp: itunes:image with bare URL missing\n%s", feedXML)
	}

	stamped := string(StampTokens(feedXML, "img-token"))

	// 1 channel itunes:image + N item itunes:image. The token-stamped
	// href appears once per occurrence; un-stamped href must NOT
	// remain.
	want := `<itunes:image href="https://feed.example.test/pg/cover.jpg?t=img-token">`
	got := strings.Count(stamped, want)
	if got != 1+len(entries) {
		t.Errorf("stamped itunes:image count: got %d, want %d (1 channel + %d items)\n%s",
			got, 1+len(entries), len(entries), stamped)
	}
	if strings.Contains(stamped, `<itunes:image href="https://feed.example.test/pg/cover.jpg">`) {
		t.Errorf("found un-stamped itunes:image href after StampTokens — token rewrite missed an element\n%s", stamped)
	}
}

// itunes:image href and atom:link self href both use `href="..."`. The
// rewrite passes are ordered so atom:link runs before itunes:image,
// and atom:link's regex requires `rel="self"` — verifying here that
// the self link does NOT acquire a duplicate `?t=` after the
// itunes:image pass.
func TestStampTokens_NoDoubleStampWhenBothHrefsPresent(t *testing.T) {
	pub := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	ch := fixtureChannel()
	ch.SelfLinkURL = "https://feed.example.test/pg.xml"
	ch.CoverImage = "https://feed.example.test/pg/cover.jpg"

	feedXML, err := Generate(ch, []model.ManifestEntry{entry("essay", "Essay", 60, 1000, pub)}, passthroughURL("https://feed.example.test"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	stamped := string(StampTokens(feedXML, "tok"))
	// `t=tok` should appear exactly once per stampable URL: 1 self
	// link + 1 channel image + 1 per-item image + 1 enclosure = 4.
	got := strings.Count(stamped, "t=tok")
	if got != 4 {
		t.Errorf("expected exactly 4 token occurrences (self + channel-image + item-image + enclosure); got %d\n%s", got, stamped)
	}
	// Specifically guard against a double-stamped self link with
	// `t=tok&amp;t=tok` — the cheapest way to detect ordering bugs.
	if strings.Contains(stamped, "t=tok&amp;t=") || strings.Contains(stamped, "t=tok&t=") {
		t.Errorf("double-stamped token appears in output:\n%s", stamped)
	}
}

func TestStampTokensFromEnv_ReadsEnv(t *testing.T) {
	t.Setenv(accessTokenEnvVar, "env-token")
	out, err := StampTokensFromEnv([]byte(`<rss><channel><atom:link rel="self" href="https://feed.example.test/pg.xml"></atom:link></channel></rss>`))
	if err != nil {
		t.Fatalf("StampTokensFromEnv: %v", err)
	}
	if !strings.Contains(string(out), `href="https://feed.example.test/pg.xml?t=env-token"`) {
		t.Fatalf("env token not stamped into self-link: %s", out)
	}
}
