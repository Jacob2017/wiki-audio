package r2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"
)

// UploadOpts tunes PutWithRetries. Zero-value fields fall back to
// the spec-pinned defaults from wa-i1l.4 (max 3 attempts, 2s base,
// jittered, capped at 30s, 120s per-request timeout).
//
// The defaults match the bead's "Concrete retry policy" table and
// the §6 retry rationale. Tests can shrink them; production callers
// should pass an empty struct.
type UploadOpts struct {
	// MaxAttempts caps the total number of PutObject calls (initial
	// + retries). 3 = initial + 2 retries.
	MaxAttempts int

	// BackoffBaseSeconds is the base for the per-attempt exponential
	// backoff. Sleep on attempt N is base * 2^(N-1) + uniform[0,1)s
	// jitter, clamped at BackoffCap.
	BackoffBaseSeconds float64

	// BackoffCap clamps the per-attempt sleep. Lower than the TTS
	// retry cap (60s) — R2 uploads are fast, long sleeps are a
	// give-up signal.
	BackoffCap time.Duration

	// PerRequestTimeout caps each PutObject call. Wider than TTS
	// because MP3 bodies are 5-10 MiB.
	PerRequestTimeout time.Duration

	// Rng is injected so tests can pin jitter; nil → fresh
	// rand.Source seeded from time.Now().UnixNano().
	Rng *rand.Rand

	// Logger is the slog destination for retry / failure records.
	// nil → slog.Default().
	Logger *slog.Logger
}

// Default* mirror the wa-i1l.4 retry-policy table. The struct
// (not const) keeps them exposed via the godoc index.
const (
	defaultMaxAttempts        = 3
	defaultBackoffBaseSeconds = 2.0
	defaultBackoffCap         = 30 * time.Second
	defaultPerRequestTimeout  = 120 * time.Second
)

func (o UploadOpts) applyDefaults() UploadOpts {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultMaxAttempts
	}
	if o.BackoffBaseSeconds <= 0 {
		o.BackoffBaseSeconds = defaultBackoffBaseSeconds
	}
	if o.BackoffCap <= 0 {
		o.BackoffCap = defaultBackoffCap
	}
	if o.PerRequestTimeout <= 0 {
		o.PerRequestTimeout = defaultPerRequestTimeout
	}
	if o.Rng == nil {
		o.Rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// PutWithRetries uploads body to bucket/key with exponential backoff
// retry on transient errors. Returns the server-assigned ETag on
// success or a wrapped error after MaxAttempts have been exhausted.
//
// body is []byte (not io.Reader) because retry requires re-reading
// the same content from offset 0; bytes.NewReader gives us a fresh
// reader per attempt without buffering at the storage layer. For
// our scale (5-10 MiB MP3s, 4-50 KiB manifests) the in-memory copy
// cost is rounding error against the upload time itself.
//
// Atomicity: R2 PutObject is atomic per object — a failed upload
// does NOT replace a previously-good object at the same key. Safe
// to retry without coordinating with readers. (The minio-go client
// also forwards the same Content-Md5 across retries by default,
// which prevents partial-body acceptance on the server side.)
//
// Classification:
//   - errors.Is(err, ErrAccessDenied) → FATAL (auth fail), surface
//     to caller without retry; the operator must fix credentials.
//   - errors.Is(err, ErrThrottled)    → retryable, exponential
//     backoff. Retry-After honoring is a known gap — minio-go's
//     ErrorResponse doesn't expose response headers, so we can't
//     extract the server's hint cleanly. Capped backoff is the
//     practical fallback. See bead's "Concrete retry policy" note.
//   - errors.Is(err, ErrNetwork)      → retryable; transport-level
//     issues (DNS, connect refused, TLS, mid-stream EOF) are
//     classified by Client.PutObject's mapError.
//   - errors.Is(err, ErrNoSuchKey)    → FATAL (NoSuchBucket leaks
//     here); caller should run wiki-audio doctor to verify bucket.
//   - any other error                 → FATAL conservatively. Better
//     to surface an unknown error to the operator than to retry-loop
//     on a programmer error like 400 Bad Request.
//
// ctx cancellation aborts the loop between attempts AND inside the
// per-attempt PerRequestTimeout context; either way the function
// returns ctx.Err() promptly.
func PutWithRetries(
	ctx context.Context,
	storage Storage,
	key string,
	body []byte,
	contentType string,
	opts UploadOpts,
) (string, error) {
	opts = opts.applyDefaults()

	var lastErr error
	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		if attempt > 1 {
			delay := computeUploadBackoff(attempt-1, opts.BackoffBaseSeconds, opts.BackoffCap, opts.Rng)
			opts.Logger.Info("r2 upload retry",
				"key", key,
				"attempt", attempt,
				"of", opts.MaxAttempts,
				"delay", delay.String())
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}

		attemptCtx, cancel := context.WithTimeout(ctx, opts.PerRequestTimeout)
		etag, err := storage.PutObject(attemptCtx, key, bytes.NewReader(body), int64(len(body)), contentType)
		cancel()

		if err == nil {
			if attempt > 1 {
				opts.Logger.Info("r2 upload succeeded after retry",
					"key", key, "attempt", attempt, "etag", etag)
			}
			return etag, nil
		}

		// Honor an external cancellation BEFORE classifying — a
		// canceled-context error from minio-go shouldn't be looped
		// on as if it were a transient.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}

		lastErr = err
		retryable := isRetryableUploadError(err)
		if !retryable {
			opts.Logger.Warn("r2 upload fatal — not retrying",
				"key", key, "attempt", attempt, "err", err.Error())
			return "", fmt.Errorf("r2 upload %s: %w", key, err)
		}
		opts.Logger.Warn("r2 upload retryable error",
			"key", key, "attempt", attempt, "of", opts.MaxAttempts, "err", err.Error())
	}

	return "", fmt.Errorf("r2 upload %s: gave up after %d attempts: %w", key, opts.MaxAttempts, lastErr)
}

// isRetryableUploadError applies the wa-i1l.4 classification table.
// Throttled + Network errors retry; AccessDenied + NoSuchKey-class
// + everything-else are fatal.
//
// Kept as a small free function (not a method on opts) so tests can
// pass synthetic errors and pin the classifier directly.
func isRetryableUploadError(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrThrottled), errors.Is(err, ErrNetwork):
		return true
	case errors.Is(err, ErrAccessDenied), errors.Is(err, ErrNoSuchKey):
		return false
	default:
		// Unknown error: be conservative. Retrying a programmer
		// error like 400 Bad Request would just burn attempts.
		return false
	}
}

// computeUploadBackoff returns base*2^(attempt-1) + uniform[0,1)
// seconds, clamped at cap. attempt is the 1-based RETRY index (so
// the FIRST retry — i.e. attempt=2 in the outer loop, this argument
// = 1 — gets ~base seconds + jitter, the second retry gets ~2*base,
// etc).
func computeUploadBackoff(retryIndex int, baseSeconds float64, cap time.Duration, rng *rand.Rand) time.Duration {
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	seconds := baseSeconds*math.Pow(2, float64(retryIndex-1)) + rng.Float64()
	delay := time.Duration(seconds * float64(time.Second))
	if delay > cap {
		return cap
	}
	return delay
}
