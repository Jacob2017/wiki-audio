// Package atomic provides "write a file or leave the old one
// untouched" helpers used everywhere wiki-audio writes regenerable
// state to disk (final MP3s, manifest snapshots in ~/.cache/,
// skipped.txt, the .env scaffold). The idiom is the standard
// CreateTemp + fsync + Chmod + Rename dance — but call sites should
// not have to remember it.
//
// The package is the §6 "Disk full during write" defense site: a
// power failure or an ENOSPC mid-write leaves the previous-good
// target in place. Use WriteFile for byte-buffer writes and
// WriteAtomic for streamed writes (so a 30-MB MP3 doesn't have to
// fit in one allocation).
package atomic
