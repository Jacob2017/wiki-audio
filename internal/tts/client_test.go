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
