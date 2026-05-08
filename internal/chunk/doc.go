// Package chunk splits a CleanedDocument into paragraph-bounded
// chunks under the ElevenLabs character budget (§5.2). Paragraph
// boundaries are sufficient for PG-length prose, sidestepping the
// need for a sentence segmenter (§8.5 trade-off).
package chunk
