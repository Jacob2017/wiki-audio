package tts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

func TestClientSynthesizeHappyPath(t *testing.T) {
	t.Cleanup(setAPIBaseURLForTest(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got, want := r.URL.Path, "/v1/text-to-speech/voice-123"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("xi-api-key"), "test-key"; got != want {
			t.Fatalf("xi-api-key = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Accept"), "audio/mpeg"; got != want {
			t.Fatalf("accept = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Fatalf("content-type = %q, want %q", got, want)
		}

		var req struct {
			Text         string `json:"text"`
			ModelID      string `json:"model_id"`
			OutputFormat string `json:"output_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if got, want := req.Text, "hello world"; got != want {
			t.Fatalf("text = %q, want %q", got, want)
		}
		if got, want := req.ModelID, model.DefaultModelID; got != want {
			t.Fatalf("model_id = %q, want %q", got, want)
		}
		if got, want := req.OutputFormat, model.DefaultOutputFormat; got != want {
			t.Fatalf("output_format = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("mp3-bytes"))
	})).URL))

	client := NewClient(model.TTSConfig{
		VoiceID: "voice-123",
	}, "test-key")

	rc, err := client.Synthesize(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got, want := string(body), "mp3-bytes"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}

	if got, want := client.timeout, 60*time.Second; got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
	if got, want := client.httpClient.Timeout, 60*time.Second; got != want {
		t.Fatalf("httpClient.Timeout = %s, want %s", got, want)
	}
}

func TestClientSynthesizeUnauthorizedIsNotRetryable(t *testing.T) {
	client := newClientAgainstStatus(t, http.StatusUnauthorized, `{"detail":"invalid api key"}`)

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if apiErr.Retryable {
		t.Fatalf("401 should not be retryable")
	}
	if got, want := apiErr.StatusCode, http.StatusUnauthorized; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if !strings.Contains(apiErr.Error(), "invalid api key") {
		t.Fatalf("error string should include body, got %q", apiErr.Error())
	}
}

func TestClientSynthesizeRateLimitIsRetryable(t *testing.T) {
	client := newClientAgainstStatus(t, http.StatusTooManyRequests, `{"detail":"too many requests"}`)

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if !apiErr.Retryable {
		t.Fatalf("429 should be retryable")
	}
}

func TestClientSynthesizeServerErrorIsRetryable(t *testing.T) {
	client := newClientAgainstStatus(t, http.StatusBadGateway, `{"detail":"upstream failed"}`)

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if !apiErr.Retryable {
		t.Fatalf("5xx should be retryable")
	}
}

func TestClientSynthesizeContextCancellationPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	client := NewClient(model.TTSConfig{
		VoiceID:         "voice-123",
		RequestTimeoutS: 1,
	}, "test-key")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Synthesize(ctx, "hello")
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %T (%v)", err, err)
	}
}

// --- wa-3gf: Retry-After header threading ---

// 429 with Retry-After in seconds form must populate
// APIError.RetryAfter. The build pipeline honors this hint over its
// computed exponential backoff, so dropping the header (the original
// wa-3gf bug) leads to too-eager retries that re-trip the 429.
func TestClientSynthesizePopulatesRetryAfterFromSeconds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, `{"detail":"slow down"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	client := NewClient(model.TTSConfig{VoiceID: "voice-123"}, "test-key")

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if apiErr.RetryAfter != 10*time.Second {
		t.Errorf("RetryAfter = %s, want 10s", apiErr.RetryAfter)
	}
	if !apiErr.Retryable {
		t.Errorf("429 should remain retryable")
	}
}

// HTTP-date form is also valid per RFC 7231 §7.1.3. parseRetryAfter
// computes the delta from now; verify the rounded result is in the
// expected ballpark (parsing has 1s resolution).
func TestClientSynthesizePopulatesRetryAfterFromHTTPDate(t *testing.T) {
	const wantApprox = 30 * time.Second
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		future := time.Now().UTC().Add(wantApprox)
		w.Header().Set("Retry-After", future.Format(http.TimeFormat))
		http.Error(w, `{"detail":"come back later"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	client := NewClient(model.TTSConfig{VoiceID: "voice-123"}, "test-key")

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	// Allow ±2s round-trip slop (HTTP-date has 1s resolution +
	// network latency between request build and server receipt).
	low, high := wantApprox-2*time.Second, wantApprox+1*time.Second
	if apiErr.RetryAfter < low || apiErr.RetryAfter > high {
		t.Errorf("RetryAfter = %s, want approx %s (±2s)", apiErr.RetryAfter, wantApprox)
	}
}

// No Retry-After header → RetryAfter zero. Build pipeline falls
// back to its computed exponential backoff in this case.
func TestClientSynthesizeRetryAfterAbsentIsZero(t *testing.T) {
	client := newClientAgainstStatus(t, http.StatusTooManyRequests, `{"detail":"slow"}`)

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0 (no header)", apiErr.RetryAfter)
	}
}

// Malformed Retry-After (not a number, not an HTTP-date) → zero.
// The client treats it as "no hint" rather than failing the
// request — the body has already been read; refusing here would
// lose the underlying status info.
func TestClientSynthesizeRetryAfterMalformedIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "soon-ish")
		http.Error(w, `{"detail":"slow"}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	client := NewClient(model.TTSConfig{VoiceID: "voice-123"}, "test-key")

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0 (malformed header)", apiErr.RetryAfter)
	}
}

// Retry-After is also valid on 5xx responses per RFC 7231. Verify
// the parser fires for any retryable status, not just 429.
func TestClientSynthesizeRetryAfterAlsoOn503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"detail":"upstream busy"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	client := NewClient(model.TTSConfig{VoiceID: "voice-123"}, "test-key")

	_, err := client.Synthesize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T (%v)", err, err)
	}
	if apiErr.RetryAfter != 5*time.Second {
		t.Errorf("RetryAfter = %s, want 5s on 503", apiErr.RetryAfter)
	}
	if !apiErr.Retryable {
		t.Errorf("503 should be retryable")
	}
}

func newClientAgainstStatus(t *testing.T, status int, body string) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, body, status)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(setAPIBaseURLForTest(srv.URL))

	return NewClient(model.TTSConfig{
		VoiceID: "voice-123",
	}, "test-key")
}

func setAPIBaseURLForTest(baseURL string) func() {
	prev := apiBaseURL
	apiBaseURL = baseURL
	return func() {
		apiBaseURL = prev
	}
}
