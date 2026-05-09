// Package feed renders the RSS XML (with the iTunes podcast namespace)
// from the manifest using stdlib encoding/xml + custom struct tags
// (PLAN §5.5, §8.6 — no third-party RSS library; the surface is small
// enough to own).
//
// Public API:
//
//	feed.Generate(channel feed.Channel,
//	              entries []model.ManifestEntry,
//	              enclosureURL func(model.ManifestEntry) string) ([]byte, error)
//
// The URL builder injected via `enclosureURL` lets wa-i1l.6 (the
// token-stamping layer) decide whether the per-item URLs carry a
// `?t=<token>` query. wa-i1l.7 (publish orchestrator) wires the builder
// from the live access token + FeedConfig.BaseURL + ManifestEntry.R2Key.
//
// Output is iTunes-namespaced RSS 2.0:
//
//	<rss version="2.0"
//	     xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"
//	     xmlns:atom="http://www.w3.org/2005/Atom">
//	  <channel> ... <item>...</item>* </channel>
//	</rss>
//
// Both namespace URIs are load-bearing — Apple, Pocket Casts, and
// Overcast match by exact URI. Don't change them.
package feed
