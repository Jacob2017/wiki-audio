package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

const maxAPIErrorBodyBytes = 4096

var apiBaseURL = "https://api.elevenlabs.io"

// Client wraps the v1 text-to-speech endpoint for a single configured
// voice/model/output tuple.
type Client struct {
	apiKey       string
	httpClient   *http.Client
	voiceID      string
	modelID      string
	outputFormat string
	timeout      time.Duration
}

// APIError classifies non-200 responses so higher layers can apply
// retry/backoff policy without scraping strings.
//
// RetryAfter carries the parsed `Retry-After` header value (RFC 7231
// §7.1.3 — supports both seconds and HTTP-date forms). Zero when
// the header was absent, malformed, or specified a delay outside
// the [0, 300s] range that parseRetryAfter accepts. Callers should
// honor this hint over computed backoff when non-zero, capped to
// whatever per-call ceiling the caller enforces.
type APIError struct {
	StatusCode int
	Body       []byte
	Retryable  bool
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e == nil {
		return "<nil>"
	}

	body := strings.TrimSpace(string(e.Body))
	if body == "" {
		return fmt.Sprintf("elevenlabs tts: status %d", e.StatusCode)
	}
	return fmt.Sprintf("elevenlabs tts: status %d: %s", e.StatusCode, body)
}

// NewClient wires the HTTP client and request defaults from TTSConfig.
// Config defaults are normally applied by internal/config, but this
// constructor also falls back to the model defaults so direct unit use
// stays safe.
func NewClient(cfg model.TTSConfig, apiKey string) *Client {
	modelID := cfg.ModelID
	if modelID == "" {
		modelID = model.DefaultModelID
	}

	outputFormat := cfg.OutputFormat
	if outputFormat == "" {
		outputFormat = model.DefaultOutputFormat
	}

	timeout := time.Duration(cfg.RequestTimeoutS * float64(time.Second))
	if timeout <= 0 {
		timeout = time.Duration(model.DefaultRequestTimeoutS * float64(time.Second))
	}

	return &Client{
		apiKey:       apiKey,
		httpClient:   &http.Client{Timeout: timeout},
		voiceID:      cfg.VoiceID,
		modelID:      modelID,
		outputFormat: outputFormat,
		timeout:      timeout,
	}
}

func (c *Client) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	if c == nil {
		return nil, fmt.Errorf("tts: nil client")
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("tts: api key is required")
	}
	if c.voiceID == "" {
		return nil, fmt.Errorf("tts: voice_id is required")
	}

	reqBody, err := json.Marshal(struct {
		Text         string `json:"text"`
		ModelID      string `json:"model_id"`
		OutputFormat string `json:"output_format"`
	}{
		Text:         text,
		ModelID:      c.modelID,
		OutputFormat: c.outputFormat,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tts request: %w", err)
	}

	endpoint := apiBaseURL + "/v1/text-to-speech/" + url.PathEscape(c.voiceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Accept", "audio/mpeg")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}

	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxAPIErrorBodyBytes))
	if readErr != nil {
		return nil, fmt.Errorf("read error response: %w", readErr)
	}

	// Parse Retry-After per RFC 7231 §7.1.3. parseRetryAfter (in
	// retry.go) returns (0, false) on missing/malformed/out-of-range
	// values, which surfaces here as RetryAfter == 0 — exactly what
	// the higher-level retry loop expects as the "no hint" signal.
	retryAfter, _ := parseRetryAfter(resp.Header, time.Now())

	return nil, &APIError{
		StatusCode: resp.StatusCode,
		Body:       body,
		Retryable:  retryableStatusCode(resp.StatusCode),
		RetryAfter: retryAfter,
	}
}

func retryableStatusCode(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusRequestTimeout ||
		code >= http.StatusInternalServerError
}
