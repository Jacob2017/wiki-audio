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
	urlAttrRe           = regexp.MustCompile(`\burl="([^"]*)"`)
	hrefAttrRe          = regexp.MustCompile(`\bhref="([^"]*)"`)
)

// StampTokens appends `t=<token>` to the feed self-link and every
// enclosure URL in a generated RSS document.
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
