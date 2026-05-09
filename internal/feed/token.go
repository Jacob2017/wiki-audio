package feed

import (
	"bytes"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const accessTokenEnvVar = "WIKI_AUDIO_ACCESS_TOKEN"

var (
	enclosureStartTagRe = regexp.MustCompile(`<enclosure\b[^>]*>`)
	atomSelfLinkTagRe   = regexp.MustCompile(`<atom:link\b[^>]*\brel="self"[^>]*>`)
	itunesImageTagRe    = regexp.MustCompile(`<itunes:image\b[^>]*>`) // wa-bo5
	urlAttrRe           = regexp.MustCompile(`\burl="([^"]*)"`)
	hrefAttrRe          = regexp.MustCompile(`\bhref="([^"]*)"`)
)

// StampTokens appends `t=<token>` to the feed self-link, every
// enclosure URL, and every `<itunes:image href="...">` URL in a
// generated RSS document. The itunes:image rewrites are wa-bo5 —
// without them, podcast clients would 403 trying to fetch the
// channel/per-item cover art (R2 is gated by the same Worker token).
//
// Empty token is treated as a caller bug but not a hard error here:
// the function logs a warning and returns the input bytes unchanged.
// wa-i1l.7's publish path uses StampTokensFromEnv and fails closed
// before writing a feed when the env var is empty.
func StampTokens(feedXML []byte, token string) []byte {
	token = strings.TrimSpace(token)
	if token == "" {
		slog.Warn("feed: access token empty; returning unstamped feed XML",
			"env_var", accessTokenEnvVar)
		return feedXML
	}

	out := enclosureStartTagRe.ReplaceAllFunc(feedXML, func(tag []byte) []byte {
		return stampAttr(tag, urlAttrRe, token)
	})
	out = atomSelfLinkTagRe.ReplaceAllFunc(out, func(tag []byte) []byte {
		return stampAttr(tag, hrefAttrRe, token)
	})
	// itunes:image href — channel-level + per-item. Apply AFTER the
	// atom:link pass because both use href; the atom regex requires
	// `rel="self"` so they don't overlap, but ordering ensures we
	// don't accidentally double-stamp.
	out = itunesImageTagRe.ReplaceAllFunc(out, func(tag []byte) []byte {
		return stampAttr(tag, hrefAttrRe, token)
	})
	return out
}

// StampTokensFromEnv loads the access token from process env and
// stamps it onto feed XML. Callers that publish feeds should use this
// wrapper so an empty token fails closed instead of silently emitting
// bare URLs.
func StampTokensFromEnv(feedXML []byte) ([]byte, error) {
	token := strings.TrimSpace(os.Getenv(accessTokenEnvVar))
	if token == "" {
		return nil, fmt.Errorf("feed: %s is empty", accessTokenEnvVar)
	}
	return StampTokens(feedXML, token), nil
}

func stampAttr(tag []byte, attrRe *regexp.Regexp, token string) []byte {
	m := attrRe.FindSubmatchIndex(tag)
	if m == nil {
		return tag
	}

	rawValue := string(tag[m[2]:m[3]])
	stampedValue := stampURL(rawValue, token)
	if stampedValue == rawValue {
		return tag
	}

	var out bytes.Buffer
	out.Grow(len(tag) + len(stampedValue) - len(rawValue))
	out.Write(tag[:m[2]])
	out.WriteString(stampedValue)
	out.Write(tag[m[3]:])
	return out.Bytes()
}

func stampURL(rawValue, token string) string {
	decoded := html.UnescapeString(rawValue)
	u, err := url.Parse(decoded)
	if err != nil {
		slog.Warn("feed: malformed URL left unstamped",
			"url", decoded,
			"err", err.Error())
		return rawValue
	}

	q := u.Query()
	q.Set("t", token)
	u.RawQuery = q.Encode()
	return html.EscapeString(u.String())
}
