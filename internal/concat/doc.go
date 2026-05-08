// Package concat shells out to ffmpeg to join per-chunk MP3s into one
// episode file using a pairwise acrossfade chain (PLAN §5.4).
//
// Pairwise reduce, not one big filter_complex: ffmpeg's filter_complex
// syntax doesn't chain `acrossfade` cleanly across N inputs, so we run
// `ffmpeg -i prev -i next -filter_complex "[0:a][1:a]acrossfade=d=X" out`
// in a loop, treating each step's output as the next step's left input.
//
// Crossfade duration is 50ms by default — long enough to mask the chunk
// boundary, short enough to be inaudible as an actual fade. Adjust via
// Options.CrossfadeSeconds if §E spike listening reveals artifacts.
//
// On success: intermediate `step_NNN.mp3` files in tmpDir are removed.
// On failure: intermediates are deliberately retained so the user can
// re-run an individual ffmpeg step with -loglevel debug to find what broke.
package concat
