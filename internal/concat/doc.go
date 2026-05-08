// Package concat shells out to ffmpeg to join per-chunk MP3s into
// one episode file using a pairwise acrossfade chain (§5.4). The
// ffmpeg binary is expected on PATH; absence is reported by `doctor`.
package concat
