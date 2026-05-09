// Package cache manages ~/.cache/wiki-audio/ — the XDG-spec scratch
// directory for per-essay TTS intermediates, finalized MP3s awaiting
// publish, and the malformed-essay skip log (§6).
//
// Layout:
//
//	~/.cache/wiki-audio/
//	├── tmp/<slug>/<index>.mp3   per-chunk intermediates; deleted on success
//	├── out/<slug>.mp3           final concatenated MP3 awaiting publish
//	└── skipped.txt              one slug per line — malformed essays
//
// XDG-aware: $XDG_CACHE_HOME, when set, replaces $HOME/.cache as the
// base — `Dir()` returns `$XDG_CACHE_HOME/wiki-audio/` in that case.
//
// Cleanup policy:
//   - After a successful concat, the build pipeline calls CleanupTmp
//     to remove tmp/<slug>/. On concat FAILURE the directory is kept
//     intact so an operator can rerun ffmpeg by hand against the raw
//     chunk MP3s (§6 "ffmpeg concatenation failure").
//   - After a successful publish, out/<slug>.mp3 is KEPT by default
//     for fast republish on token rotation (~600 MB across the
//     53-essay corpus is tolerable). CleanupOut exists for operators
//     who hit disk pressure; the publish pipeline does NOT call it
//     automatically.
//
// Distinct from ~/.wiki-audio/ (config + secrets). Conflating the
// two would mean either checking secrets into a regeneratable cache
// or making config un-deletable; XDG basedir spec separates them
// for exactly that reason.
package cache
