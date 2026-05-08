// Package extract turns a raw markdown essay into a CleanedDocument
// suitable for TTS: strips Metadata/Full Document headers, splits
// prose vs Notes, parses footnote_map, and removes image refs, links,
// wikilinks, and code blocks (§5.1).
package extract
