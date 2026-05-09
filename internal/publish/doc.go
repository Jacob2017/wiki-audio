// Package publish holds Phase F orchestration logic that sits above
// internal/r2 (write-side surface) and internal/manifest (state-of-
// truth surface) — diffing, uploading, RSS regeneration, and the
// post-upload manifest update. The split keeps r2 a transport-only
// package and manifest a serialization-only package: both stay
// dependency-free of each other while publish composes them.
package publish
