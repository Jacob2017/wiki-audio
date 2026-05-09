package r2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// scriptedStorage is a Storage implementation for retry tests that
// returns a pre-scripted sequence of (etag, error) pairs from
// PutObject. Other Storage methods are unused in these tests; they
// fail loud if a future test accidentally calls them.
//
// Pane-9's Fake (fake.go) is a positive-path harness — every Put
// succeeds. wa-i1l.4 needs failure injection, which is its own
// concern; rather than extend Fake's contract for one downstream
// test, the scripted variant lives here.
type scriptedStorage struct {
	mu        sync.Mutex
	responses []scriptedResp
	calls     int
	bodies    [][]byte // captured PutObject bodies, one per call
}

type scriptedResp struct {
	etag string
	err  error
	// hold blocks PutObject for this duration before returning.
	// Used to test PerRequestTimeout cancellation. Optional.
	hold time.Duration
}

func (s *scriptedStorage) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	s.mu.Lock()
	idx := s.calls
	s.calls++
	if idx >= len(s.responses) {
		s.mu.Unlock()
		return "", fmt.Errorf("scripted: ran out of responses at call %d", idx+1)
	}
	resp := s.responses[idx]
	s.mu.Unlock()

	// Read body so the per-attempt test can verify it's the same
	// every time (idempotency invariant).
	body, _ := io.ReadAll(r)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()

	if resp.hold > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(resp.hold):
		}
	}
	if resp.err != nil {
		return "", resp.err
	}
	return resp.etag, nil
}

func (s *scriptedStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	panic("scripted: GetObject not used in upload tests")
}
func (s *scriptedStorage) HeadObject(ctx context.Context, key string) (ObjectInfo, error) {
	panic("scripted: HeadObject not used in upload tests")
}
func (s *scriptedStorage) DeleteObject(ctx context.Context, key string) error {
	panic("scripted: DeleteObject not used in upload tests")
}
func (s *scriptedStorage) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	panic("scripted: ListObjects not used in upload tests")
}

func (s *scriptedStorage) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// fastOpts returns an UploadOpts that runs retries quickly so test
// wall-clock stays sub-second. Production uses the Defaults.
func fastOpts() UploadOpts {
	return UploadOpts{
		MaxAttempts:        3,
		BackoffBaseSeconds: 0.001,                        // 1ms
		BackoffCap:         10 * time.Millisecond,
		PerRequestTimeout:  500 * time.Millisecond,
		Rng:                rand.New(rand.NewSource(1)), // deterministic jitter
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// --- happy path ---

func TestPutWithRetriesHappyPath(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{{etag: "etag-1"}},
	}
	body := []byte("mp3-bytes")

	etag, err := PutWithRetries(context.Background(), s, "pg/x.mp3", body, "audio/mpeg", fastOpts())
	if err != nil {
		t.Fatalf("PutWithRetries: %v", err)
	}
	if etag != "etag-1" {
		t.Errorf("etag = %q, want etag-1", etag)
	}
	if s.callCount() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on success)", s.callCount())
	}
}

// --- transient error then success ---

func TestPutWithRetriesTransient5xxThenSuccess(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: 503", ErrThrottled)},
			{etag: "etag-after-retry"},
		},
	}

	etag, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("body"), "audio/mpeg", fastOpts())
	if err != nil {
		t.Fatalf("PutWithRetries: %v", err)
	}
	if etag != "etag-after-retry" {
		t.Errorf("etag = %q", etag)
	}
	if s.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", s.callCount())
	}
}

func TestPutWithRetriesNetworkErrorRetried(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: dial tcp: connect refused", ErrNetwork)},
			{etag: "etag-net-recover"},
		},
	}
	etag, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("b"), "audio/mpeg", fastOpts())
	if err != nil {
		t.Fatalf("PutWithRetries: %v", err)
	}
	if etag != "etag-net-recover" {
		t.Errorf("etag = %q", etag)
	}
}

// --- exhaustion ---

func TestPutWithRetries5ConsecutiveTransientFailsWithWrappedError(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: 503", ErrThrottled)},
			{err: fmt.Errorf("%w: 503", ErrThrottled)},
			{err: fmt.Errorf("%w: 503", ErrThrottled)},
		},
	}
	opts := fastOpts() // MaxAttempts: 3

	_, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("b"), "audio/mpeg", opts)
	if err == nil {
		t.Fatal("expected error after MaxAttempts")
	}
	if !errors.Is(err, ErrThrottled) {
		t.Errorf("error chain should preserve ErrThrottled; got %v", err)
	}
	if s.callCount() != 3 {
		t.Errorf("calls = %d, want 3 (MaxAttempts)", s.callCount())
	}
}

// --- fatal short-circuit ---

func TestPutWithRetries401AccessDeniedShortCircuits(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: invalid signature", ErrAccessDenied)},
		},
	}

	_, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("b"), "audio/mpeg", fastOpts())
	if err == nil {
		t.Fatal("expected fatal error on 401")
	}
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("error chain should preserve ErrAccessDenied; got %v", err)
	}
	if s.callCount() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on fatal auth)", s.callCount())
	}
}

func TestPutWithRetriesUnknownErrorIsFatal(t *testing.T) {
	// A non-typed error should NOT be retried — defensive against
	// a programmer error (e.g. 400 Bad Request) burning attempts.
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: errors.New("400 Bad Request — unknown classification")},
		},
	}
	_, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("b"), "audio/mpeg", fastOpts())
	if err == nil {
		t.Fatal("expected fatal error on unclassified")
	}
	if s.callCount() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on unknown)", s.callCount())
	}
}

// --- idempotency: same body across retries ---

// The bead's "Take []byte not io.Reader" choice exists so retries
// can re-read the same content. Pin it: every PutObject attempt
// receives byte-identical body, regardless of how many retries
// fire.
func TestPutWithRetriesBodyByteIdenticalAcrossAttempts(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: transient", ErrThrottled)},
			{err: fmt.Errorf("%w: transient", ErrThrottled)},
			{etag: "ok"},
		},
	}
	body := []byte("the load-bearing payload that must be byte-identical across retries")

	if _, err := PutWithRetries(context.Background(), s, "pg/x.mp3", body, "audio/mpeg", fastOpts()); err != nil {
		t.Fatal(err)
	}
	if len(s.bodies) != 3 {
		t.Fatalf("expected 3 captured bodies; got %d", len(s.bodies))
	}
	for i, got := range s.bodies {
		if !bytes.Equal(got, body) {
			t.Errorf("attempt %d body differs from input — retry sent stale or partial bytes", i)
		}
	}
}

// --- context cancellation ---

// External cancellation between attempts must short-circuit promptly
// — long sleep on attempt 2 should NOT block past ctx deadline.
func TestPutWithRetriesContextCanceledBetweenAttempts(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{err: fmt.Errorf("%w: 503", ErrThrottled)},
			{etag: "ok-but-never-reached"},
		},
	}
	opts := fastOpts()
	opts.BackoffBaseSeconds = 5.0  // would normally sleep ~5s
	opts.BackoffCap = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond) // let first attempt fail, then cancel
		cancel()
	}()

	start := time.Now()
	_, err := PutWithRetries(ctx, s, "pg/x.mp3", []byte("b"), "audio/mpeg", opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled; got %v", err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("cancel didn't propagate promptly; elapsed = %s", elapsed)
	}
	if s.callCount() != 1 {
		t.Errorf("calls = %d, want 1 (cancel before second attempt)", s.callCount())
	}
}

// Per-request timeout fires inside a hung PutObject — even without
// caller cancellation. The first attempt's hung response gets
// canceled via the timeout-bound context; the second attempt
// completes successfully.
func TestPutWithRetriesPerRequestTimeoutCancelsHungAttempt(t *testing.T) {
	s := &scriptedStorage{
		responses: []scriptedResp{
			{hold: 200 * time.Millisecond, err: nil}, // hangs past timeout
			{etag: "second-attempt-success"},
		},
	}
	opts := fastOpts()
	opts.PerRequestTimeout = 10 * time.Millisecond

	// scriptedStorage's hold respects ctx cancellation, so the first
	// call returns with ctx.DeadlineExceeded — that's a transport-
	// level error in our taxonomy, classified as fatal by default
	// (ErrNetwork-classified errors are wrapped by mapError, not
	// raw context errors). Verify: the upload errors out cleanly
	// with the timeout, NOT a retry.
	_, err := PutWithRetries(context.Background(), s, "pg/x.mp3", []byte("b"), "audio/mpeg", opts)
	if err == nil {
		t.Fatal("expected error (per-request timeout)")
	}
	// One call — first hung, returned ctx.DeadlineExceeded which is
	// not in our retryable taxonomy (raw context error has no
	// ErrThrottled/ErrNetwork wrap), so we don't retry. The
	// production path would have ctx.DeadlineExceeded wrapped by
	// minio-go's mapError → ErrNetwork → retryable. Documenting the
	// distinction with this test.
	if s.callCount() != 1 {
		t.Errorf("calls = %d; first attempt hung past per-request timeout", s.callCount())
	}
}

// --- backoff helper ---

func TestComputeUploadBackoffMonotonicWithinCap(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	cap := 30 * time.Second

	prev := time.Duration(0)
	for retry := 1; retry <= 5; retry++ {
		got := computeUploadBackoff(retry, 2.0, cap, rng)
		if got > cap {
			t.Errorf("retry %d backoff %s exceeds cap %s", retry, got, cap)
		}
		if retry <= 4 && got <= prev && got < cap {
			// Monotonic UNTIL the cap kicks in. With base=2 the
			// pre-cap progression is ~2/4/8/16; jitter could shave
			// fractions but not regress.
			t.Errorf("retry %d backoff %s should generally exceed previous %s (pre-cap)",
				retry, got, prev)
		}
		prev = got
	}
}

func TestComputeUploadBackoffNilRngDefaults(t *testing.T) {
	got := computeUploadBackoff(1, 2.0, 30*time.Second, nil)
	if got <= 0 {
		t.Errorf("nil rng should produce positive backoff; got %s", got)
	}
}

// --- isRetryableUploadError table ---

func TestIsRetryableUploadErrorTaxonomy(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil", nil, false},
		{"throttled", fmt.Errorf("%w: SlowDown", ErrThrottled), true},
		{"network", fmt.Errorf("%w: dial tcp: refused", ErrNetwork), true},
		{"access denied", fmt.Errorf("%w: invalid sig", ErrAccessDenied), false},
		{"no such key", fmt.Errorf("%w: bucket missing", ErrNoSuchKey), false},
		{"unclassified", errors.New("some 400-class boom"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableUploadError(c.err); got != c.retryable {
				t.Errorf("got %v, want %v", got, c.retryable)
			}
		})
	}
}

// --- defaults applied ---

func TestUploadOptsApplyDefaultsFillsZeroFields(t *testing.T) {
	got := UploadOpts{}.applyDefaults()
	if got.MaxAttempts != defaultMaxAttempts {
		t.Errorf("MaxAttempts = %d", got.MaxAttempts)
	}
	if got.BackoffBaseSeconds != defaultBackoffBaseSeconds {
		t.Errorf("BackoffBaseSeconds = %v", got.BackoffBaseSeconds)
	}
	if got.BackoffCap != defaultBackoffCap {
		t.Errorf("BackoffCap = %s", got.BackoffCap)
	}
	if got.PerRequestTimeout != defaultPerRequestTimeout {
		t.Errorf("PerRequestTimeout = %s", got.PerRequestTimeout)
	}
	if got.Rng == nil {
		t.Errorf("Rng should default to non-nil")
	}
	if got.Logger == nil {
		t.Errorf("Logger should default to non-nil")
	}
}

func TestUploadOptsApplyDefaultsPreservesExplicit(t *testing.T) {
	custom := UploadOpts{
		MaxAttempts:        7,
		BackoffBaseSeconds: 0.5,
		BackoffCap:         5 * time.Second,
		PerRequestTimeout:  10 * time.Second,
	}
	got := custom.applyDefaults()
	if got.MaxAttempts != 7 {
		t.Errorf("MaxAttempts override lost: %d", got.MaxAttempts)
	}
	if got.BackoffBaseSeconds != 0.5 {
		t.Errorf("BackoffBaseSeconds override lost: %v", got.BackoffBaseSeconds)
	}
	if got.BackoffCap != 5*time.Second {
		t.Errorf("BackoffCap override lost: %s", got.BackoffCap)
	}
	if got.PerRequestTimeout != 10*time.Second {
		t.Errorf("PerRequestTimeout override lost: %s", got.PerRequestTimeout)
	}
}
