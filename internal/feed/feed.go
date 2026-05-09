package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Load-bearing namespace URIs. Apple Podcasts, Pocket Casts, and
// Overcast match by exact URI. Don't "modernize" these to https or
// shorten them — clients silently drop feeds whose namespace URI
// doesn't match byte-for-byte.
const (
	NSItunes = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	NSAtom   = "http://www.w3.org/2005/Atom"
)

// DefaultCategory is the v1 fallback for the channel-level
// `<itunes:category text="...">`. PLAN §10 doesn't pin a category;
// Apple's category list is non-extensible, so a literal that exists
// in their taxonomy is safer than inventing one.
const DefaultCategory = "Technology"

// Channel describes the podcast at the channel level. Caller fills it
// from FeedConfig (model.FeedConfig) plus the canonical self-link URL.
type Channel struct {
	// Title, Description, Author, OwnerEmail, Language pull from the
	// FeedConfig fields with the same names. All required.
	Title       string
	Description string
	Author      string
	OwnerEmail  string
	Language    string

	// Link is the channel-level <link>. Conventionally the feed's home
	// URL — for wiki-audio, FeedConfig.BaseURL.
	Link string

	// SelfLinkURL is the canonical URL of the feed file itself,
	// including any access-token query if the feed is gated. Emitted
	// as `<atom:link rel="self" type="application/rss+xml" href="...">`.
	// wa-i1l.6 supplies this with `?t=<token>` already appended.
	SelfLinkURL string

	// CoverImage is the optional `<itunes:image href="...">`. Empty
	// means omit the element. Cover art is deferred for v1 per §10;
	// callers can ship without it.
	CoverImage string

	// Category is the `<itunes:category text="...">` value. Empty →
	// DefaultCategory.
	Category string
}

// Generate emits an iTunes-namespaced RSS 2.0 XML document.
//
// enclosureURL is called once per entry that survives the eligibility
// filter; it returns the URL to put in `<enclosure url="...">`.
// Splitting URL construction from the generator lets wa-i1l.6 layer
// token-stamping on top without touching the XML shape.
//
// Eligibility: entries with nil PublishedAt OR empty R2Key are
// skipped — they're not fully published yet, and emitting an item
// whose enclosure URL would 404 would round-trip as a "missing
// episode" notice in podcast apps. (Resolves wa-hv6's [HIGH]
// finding on feed-emission rules for incomplete entries.)
//
// Items are emitted in PublishedAt-descending order (newest first).
// Apps key off this ordering for inbox layout; PLAN §5.5 makes it
// explicit. Ties on PublishedAt break on Slug ascending so
// regenerated feeds are byte-stable across runs (wa-hv6 [LOW]).
//
// Returns the XML bytes ready to be written to pg.xml. Bytes start
// with the standard `<?xml version="1.0" encoding="UTF-8"?>` prolog.
func Generate(
	channel Channel,
	entries []model.ManifestEntry,
	enclosureURL func(model.ManifestEntry) string,
) ([]byte, error) {
	if err := channel.validate(); err != nil {
		return nil, fmt.Errorf("feed: invalid channel: %w", err)
	}
	if enclosureURL == nil {
		return nil, errors.New("feed: enclosureURL builder is required")
	}

	eligible := filterEligible(entries)
	sortByPubDateDesc(eligible)

	cat := channel.Category
	if cat == "" {
		cat = DefaultCategory
	}

	r := rssRoot{
		Version:     "2.0",
		XMLNSItunes: NSItunes,
		XMLNSAtom:   NSAtom,
		Channel: channelXML{
			Title:       channel.Title,
			Link:        channel.Link,
			Description: channel.Description,
			Language:    channel.Language,
			AtomLink: atomLinkXML{
				Rel:  "self",
				Type: "application/rss+xml",
				Href: channel.SelfLinkURL,
			},
			ItunesAuthor: itunesAuthorXML{Value: channel.Author},
			ItunesOwner: &itunesOwnerXML{
				Name:  itunesNameXML{Value: channel.Author},
				Email: itunesEmailXML{Value: channel.OwnerEmail},
			},
			ItunesExplicit: itunesExplicitXML{Value: "false"},
			ItunesCategory: itunesCategoryXML{Text: cat},
		},
	}
	if channel.CoverImage != "" {
		r.Channel.ItunesImage = &itunesImageXML{Href: channel.CoverImage}
	}

	r.Channel.Items = make([]itemXML, 0, len(eligible))
	for _, e := range eligible {
		url := enclosureURL(e)
		r.Channel.Items = append(r.Channel.Items, itemXML{
			Title: e.Title,
			Enclosure: enclosureXML{
				URL:    url,
				Length: e.FileSizeBytes,
				Type:   "audio/mpeg",
			},
			Guid: guidXML{
				IsPermaLink: "false", // §5.5 — without this, RSS 2.0 defaults to "true" and clients try to GET the guid as a URL
				Value:       "pg-" + e.Slug,
			},
			// PublishedAt is non-nil here (filterEligible guaranteed).
			PubDate:        e.PublishedAt.UTC().Format(time.RFC1123Z),
			ItunesAuthor:   itunesAuthorXML{Value: channel.Author},
			ItunesDuration: itunesDurationXML{Value: int(e.DurationSeconds + 0.5)},
			ItunesExplicit: itunesExplicitXML{Value: "false"},
			// v1: no per-item description. ManifestEntry doesn't
			// carry PublishDateText; per-item summaries land in a
			// follow-up bead alongside cover art (§10 deferral).
			Description: "",
		})
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header) // <?xml version="1.0" encoding="UTF-8"?>
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("feed: marshal: %w", err)
	}
	if err := enc.Flush(); err != nil {
		return nil, fmt.Errorf("feed: flush: %w", err)
	}
	// xml.Encoder doesn't add a trailing newline; podcast validators
	// don't care, but `cat pg.xml` looks tidier.
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func (c Channel) validate() error {
	if c.Title == "" {
		return errors.New("Title is required")
	}
	if c.Description == "" {
		return errors.New("Description is required")
	}
	if c.Author == "" {
		return errors.New("Author is required")
	}
	if c.OwnerEmail == "" {
		return errors.New("OwnerEmail is required")
	}
	if c.Language == "" {
		return errors.New("Language is required")
	}
	if c.Link == "" {
		return errors.New("Link is required")
	}
	if c.SelfLinkURL == "" {
		return errors.New("SelfLinkURL is required")
	}
	return nil
}

// filterEligible drops entries that are not yet fully published — nil
// PublishedAt or empty R2Key indicates an in-progress upload (e.g. an
// upload that failed mid-batch and will retry on the next publish; see
// PLAN §6 row "R2 upload failure"). Emitting such entries would 404
// in podcast apps. Resolves wa-hv6's [HIGH] feed-eligibility finding.
func filterEligible(entries []model.ManifestEntry) []model.ManifestEntry {
	out := make([]model.ManifestEntry, 0, len(entries))
	for _, e := range entries {
		if e.PublishedAt == nil {
			continue
		}
		if e.R2Key == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// sortByPubDateDesc sorts in place; newest first per PLAN §5.5.
// Stable sort with an explicit slug tie-break keeps the output
// byte-stable across regenerations (wa-hv6 [LOW] item-ordering).
func sortByPubDateDesc(entries []model.ManifestEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		ti := entries[i].PublishedAt.UTC()
		tj := entries[j].PublishedAt.UTC()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return entries[i].Slug < entries[j].Slug
	})
}

// =====================================================================
// XML structs.
//
// encoding/xml doesn't model XML namespaces the RSS-prefixed way
// (xmlns:itunes="..."). The workaround is to put the literal prefixed
// name in the XMLName tag (e.g. `xml:"itunes:author"`) and declare the
// xmlns attributes by hand on the rss root. Clients see the same
// bytes; Go's marshaller leaves the prefix as a literal name.
// =====================================================================

type rssRoot struct {
	XMLName     xml.Name   `xml:"rss"`
	Version     string     `xml:"version,attr"`
	XMLNSItunes string     `xml:"xmlns:itunes,attr"`
	XMLNSAtom   string     `xml:"xmlns:atom,attr"`
	Channel     channelXML `xml:"channel"`
}

type channelXML struct {
	Title          string            `xml:"title"`
	Link           string            `xml:"link"`
	AtomLink       atomLinkXML       `xml:"atom:link"`
	Description    string            `xml:"description"`
	Language       string            `xml:"language"`
	ItunesAuthor   itunesAuthorXML   `xml:"itunes:author"`
	ItunesOwner    *itunesOwnerXML   `xml:"itunes:owner,omitempty"`
	ItunesExplicit itunesExplicitXML `xml:"itunes:explicit"`
	ItunesCategory itunesCategoryXML `xml:"itunes:category"`
	ItunesImage    *itunesImageXML   `xml:"itunes:image,omitempty"`
	Items          []itemXML         `xml:"item"`
}

type atomLinkXML struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type itunesAuthorXML struct {
	Value string `xml:",chardata"`
}

type itunesOwnerXML struct {
	Name  itunesNameXML  `xml:"itunes:name"`
	Email itunesEmailXML `xml:"itunes:email"`
}

type itunesNameXML struct {
	Value string `xml:",chardata"`
}

type itunesEmailXML struct {
	Value string `xml:",chardata"`
}

type itunesExplicitXML struct {
	Value string `xml:",chardata"`
}

type itunesCategoryXML struct {
	Text string `xml:"text,attr"`
}

type itunesImageXML struct {
	Href string `xml:"href,attr"`
}

type itunesDurationXML struct {
	// itunes:duration accepts seconds-as-integer or HH:MM:SS. Apple
	// canonically prefers the integer form for any duration < 24h
	// (PLAN §5.5 sample shows `<itunes:duration>4924</itunes:duration>`).
	Value int `xml:",chardata"`
}

type itemXML struct {
	Title          string            `xml:"title"`
	Enclosure      enclosureXML      `xml:"enclosure"`
	Guid           guidXML           `xml:"guid"`
	PubDate        string            `xml:"pubDate"`
	ItunesAuthor   itunesAuthorXML   `xml:"itunes:author"`
	ItunesDuration itunesDurationXML `xml:"itunes:duration"`
	ItunesExplicit itunesExplicitXML `xml:"itunes:explicit"`
	Description    string            `xml:"description,omitempty"`
}

type enclosureXML struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type guidXML struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}
