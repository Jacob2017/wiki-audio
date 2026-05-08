// Package tts wraps the ElevenLabs HTTP API: synthesize one chunk
// to MP3 bytes, retry/backoff on transient errors, surface 4xx as
// non-retryable (§5.3, §8.6). One client per process; no global state.
package tts
