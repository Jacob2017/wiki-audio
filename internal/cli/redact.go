package cli

import "net/url"

// RedactURL returns s with any token-bearing query parameters replaced
// by "<redacted>". Use it before logging any URL whose query string may
// include the access token (the `t` parameter served by the Cloudflare
// Worker). See wa-76r.3 — "log lines that print URLs should always
// strip the query string before logging".
func RedactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.RawQuery == "" {
		return s
	}
	q := u.Query()
	changed := false
	for _, key := range []string{"t", "token", "access_token"} {
		if _, ok := q[key]; ok {
			q.Set(key, "<redacted>")
			changed = true
		}
	}
	if !changed {
		return s
	}
	u.RawQuery = q.Encode()
	return u.String()
}
