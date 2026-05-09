// Package concat shells out to ffmpeg to join per-chunk MP3s into one
// episode file using stream-copy via the `concat` demuxer (PLAN §5.4 +
// wa-fse defect closure).
//
// Algorithm: write a `concat-list.txt` with one `file 'PATH'` line per
// input, then run a single
//
//	ffmpeg -f concat -safe 0 -i list.txt -c copy out.mp3
//
// invocation. Stream copy is bit-exact: every input MP3 frame is
// written into the output verbatim, with NO re-encode. There is one
// ffmpeg subprocess for the whole essay, regardless of N.
//
// Why NOT acrossfade pairwise reduce (the previous implementation,
// shipped through wa-50g):
//
//   - acrossfade requires decode + filter + re-encode at every step,
//     so an N-chunk pairwise reduce subjected the original chunk 0 to
//     N-1 generation-loss cycles. On a real 18-chunk PG essay the
//     accumulated quality damage was audible (smeared transients,
//     distorted sibilants, high-frequency loss); user-observed in the
//     wa-4cw.8 / wa-fse listening test.
//   - The pairwise approach also turned a roughly O(N) operation into
//     an O(N^2) one because each step re-decoded the cumulative left
//     side. Stream-copy is O(N) wall time and I/O bound.
//
// Trade-off accepted with wa-fse: the 50ms acrossfade between chunks
// is GONE. ElevenLabs' chunk endings naturally sit on sentence
// boundaries with brief silence; the joints are inaudible in
// practice. If a future essay surfaces an audible click at a chunk
// boundary, the right fix is a tiny pre-encoded silence file inserted
// between joints in the concat list (still stream-copy, still zero
// generation loss) — NOT to bring back acrossfade.
//
// On success: `concat-list.txt` in tmpDir is removed.
// On failure: the list file is retained so the user can
// `cat tmpDir/concat-list.txt` to see exactly what ffmpeg was asked to
// concatenate, and re-run with `-loglevel debug` for diagnosis.
package concat
