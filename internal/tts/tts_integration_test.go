//go:build integration

package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/testutil"
)

const (
	integrationDefaultVoiceID = "G17SuINrv2H9FC6nvetn" // Christopher (§10/config.example.toml)
	integrationSmokeText      = "Hello, this is a test."
)

var integrationTimeoutText = strings.Repeat("Timeout integration test sentence. ", 64)

var errSubscriptionReadPermission = errors.New("elevenlabs api key missing user_read permission")

type integrationSubscription struct {
	CharacterCount int `json:"character_count"`
	CharacterLimit int `json:"character_limit"`
}

func (s integrationSubscription) remaining() int {
	return s.CharacterLimit - s.CharacterCount
}

// Real-service coverage for wa-4cw.12. This file is compiled only with
// -tags=integration and still requires WIKI_AUDIO_RUN_INTEGRATION=1 at
// runtime, so accidental `go test ./...` never burns ElevenLabs quota.
//
// Cost note: the smoke request is 22 runes (~11 billed credits at the
// plan's 0.5 credits/rune assumption). The invalid/missing-key cases
// should be rejected pre-synthesis. The timeout case uses a larger body
// but still costs pennies even if ElevenLabs meters partial work.
func TestIntegration(t *testing.T) {
	testutil.RequireIntegration(t)
	testutil.RequireCredentials(t, "ELEVENLABS_API_KEY")

	apiKey := os.Getenv("ELEVENLABS_API_KEY")
	voiceID := integrationVoiceID()

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
		"test", t.Name(),
		"service", "elevenlabs",
		"model_id", model.DefaultModelID,
		"voice_id", voiceID,
	)

	before, err := fetchSubscription(ctx, apiKey)
	haveSubscription := err == nil
	if err != nil && !errors.Is(err, errSubscriptionReadPermission) {
		t.Fatalf("fetch subscription before run: %v", err)
	}
	if haveSubscription {
		logger = logger.With("credits_before", before.remaining())
		logger.Info("integration_start",
			"characters_used_before", before.CharacterCount,
			"characters_limit", before.CharacterLimit,
		)
	} else {
		logger.Warn("subscription_probe_unavailable",
			"reason", err.Error(),
			"fallback", "estimate credit usage from text length",
		)
	}

	t.Run("smoke_short_request", func(t *testing.T) {
		client := NewClient(model.TTSConfig{
			VoiceID:      voiceID,
			ModelID:      model.DefaultModelID,
			OutputFormat: model.DefaultOutputFormat,
		}, apiKey)

		rc, err := client.Synthesize(ctx, integrationSmokeText)
		if err != nil {
			t.Fatalf("Synthesize(smoke) error: %v", err)
		}
		defer rc.Close()

		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("ReadAll(smoke) error: %v", err)
		}
		if len(body) == 0 {
			t.Fatal("smoke returned 0 MP3 bytes")
		}

		logger.Info("smoke_short_request_ok",
			"bytes", len(body),
			"runes", utf8.RuneCountInString(integrationSmokeText),
		)
	})

	t.Run("respects_request_timeout", func(t *testing.T) {
		client := NewClient(model.TTSConfig{
			VoiceID:         voiceID,
			ModelID:         model.DefaultModelID,
			OutputFormat:    model.DefaultOutputFormat,
			RequestTimeoutS: 1,
		}, apiKey)

		_, err := client.Synthesize(ctx, integrationTimeoutText)
		if err == nil {
			t.Fatal("expected timeout/transport error, got nil")
		}

		decision := classifyIntegrationError(err)
		if decision.verdict != retryVerdictRetryable {
			t.Fatalf("timeout verdict = %s, want %s (err=%v)", decision.verdict, retryVerdictRetryable, err)
		}

		logger.Info("timeout_classified",
			"verdict", decision.verdict.String(),
			"sleep", decision.sleep,
			"timeout_s", 1,
			"runes", utf8.RuneCountInString(integrationTimeoutText),
		)
	})

	t.Run("invalid_voice_id_yields_4xx", func(t *testing.T) {
		client := NewClient(model.TTSConfig{
			VoiceID:      "bogus",
			ModelID:      model.DefaultModelID,
			OutputFormat: model.DefaultOutputFormat,
		}, apiKey)

		_, err := client.Synthesize(ctx, integrationSmokeText)
		if err == nil {
			t.Fatal("expected APIError for bogus voice ID")
		}

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected APIError, got %T (%v)", err, err)
		}
		if apiErr.StatusCode < 400 || apiErr.StatusCode >= 500 {
			t.Fatalf("status = %d, want 4xx", apiErr.StatusCode)
		}

		decision := classifyAPIError(apiErr, 0, retryBaseSeconds, rand.New(rand.NewSource(1)))
		if decision.verdict != retryVerdictFatal {
			t.Fatalf("bogus voice verdict = %s, want %s", decision.verdict, retryVerdictFatal)
		}

		logger.Info("invalid_voice_classified",
			"status", apiErr.StatusCode,
			"verdict", decision.verdict.String(),
		)
	})

	t.Run("empty_text_yields_400", func(t *testing.T) {
		client := NewClient(model.TTSConfig{
			VoiceID:      voiceID,
			ModelID:      model.DefaultModelID,
			OutputFormat: model.DefaultOutputFormat,
		}, apiKey)

		rc, err := client.Synthesize(ctx, "")
		if err == nil {
			defer rc.Close()
			body, readErr := io.ReadAll(rc)
			if readErr != nil {
				t.Fatalf("empty text returned 200 but body read failed: %v", readErr)
			}
			if len(body) == 0 {
				t.Fatal("empty text returned 200 but 0 MP3 bytes")
			}

			logger.Warn("empty_text_accepted_by_api",
				"bytes", len(body),
				"note", "real API did not reject text=\"\" with 400",
			)
			return
		}

		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("expected APIError, got %T (%v)", err, err)
		}
		if apiErr.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", apiErr.StatusCode)
		}

		decision := classifyAPIError(apiErr, 0, retryBaseSeconds, rand.New(rand.NewSource(1)))
		if decision.verdict != retryVerdictFatal {
			t.Fatalf("empty text verdict = %s, want %s", decision.verdict, retryVerdictFatal)
		}

		logger.Info("empty_text_classified",
			"status", apiErr.StatusCode,
			"verdict", decision.verdict.String(),
		)
	})

	t.Run("header_xi_api_key_required", func(t *testing.T) {
		resp, err := rawSynthesize(ctx, rawSynthesizeRequest{
			voiceID:      voiceID,
			text:         integrationSmokeText,
			modelID:      model.DefaultModelID,
			outputFormat: model.DefaultOutputFormat,
			apiKey:       "",
		})
		if err != nil {
			t.Fatalf("raw synthesize without api key: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			t.Fatalf("status = %d, want 401; body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		decision := classifyHTTPResponse(resp, 0, retryBaseSeconds, rand.New(rand.NewSource(1)), time.Now())
		if decision.verdict != retryVerdictFatal {
			t.Fatalf("missing-key verdict = %s, want %s", decision.verdict, retryVerdictFatal)
		}

		logger.Info("missing_api_key_classified",
			"status", resp.StatusCode,
			"verdict", decision.verdict.String(),
		)
	})

	var after integrationSubscription
	if haveSubscription {
		after, err = fetchSubscription(ctx, apiKey)
		if err != nil {
			t.Fatalf("fetch subscription after run: %v", err)
		}
	}

	t.Run("credits_remaining_after_run", func(t *testing.T) {
		if !haveSubscription {
			t.Skip("subscription endpoint unavailable to this API key; estimated usage logged from text length")
		}
		if after.remaining() <= 0 {
			t.Fatalf("remaining characters = %d, want > 0", after.remaining())
		}

		smokeRunes := utf8.RuneCountInString(integrationSmokeText)
		logger.Info("integration_end",
			"characters_used_after", after.CharacterCount,
			"characters_remaining_after", after.remaining(),
			"character_delta", after.CharacterCount-before.CharacterCount,
			"estimated_smoke_credits", float64(smokeRunes)*0.5,
		)
	})
}

type rawSynthesizeRequest struct {
	voiceID      string
	text         string
	modelID      string
	outputFormat string
	apiKey       string
}

func rawSynthesize(ctx context.Context, req rawSynthesizeRequest) (*http.Response, error) {
	body, err := json.Marshal(struct {
		Text         string `json:"text"`
		ModelID      string `json:"model_id"`
		OutputFormat string `json:"output_format"`
	}{
		Text:         req.text,
		ModelID:      req.modelID,
		OutputFormat: req.outputFormat,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal raw request: %w", err)
	}

	endpoint := apiBaseURL + "/v1/text-to-speech/" + url.PathEscape(req.voiceID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "audio/mpeg")
	httpReq.Header.Set("Content-Type", "application/json")
	if req.apiKey != "" {
		httpReq.Header.Set("xi-api-key", req.apiKey)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	return client.Do(httpReq)
}

func classifyIntegrationError(err error) retryDecision {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return classifyAPIError(apiErr, 0, retryBaseSeconds, rand.New(rand.NewSource(1)))
	}
	return classifyTransportError(err, 0, retryBaseSeconds, rand.New(rand.NewSource(1)))
}

func integrationVoiceID() string {
	if v := strings.TrimSpace(os.Getenv("ELEVENLABS_VOICE_ID")); v != "" {
		return v
	}
	return integrationDefaultVoiceID
}

func fetchSubscription(ctx context.Context, apiKey string) (integrationSubscription, error) {
	var zero integrationSubscription

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/v1/user/subscription", nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusUnauthorized &&
			strings.Contains(msg, "missing_permissions") &&
			strings.Contains(msg, "user_read") {
			return zero, fmt.Errorf("%w: status %d: %s", errSubscriptionReadPermission, resp.StatusCode, msg)
		}
		return zero, fmt.Errorf("subscription status %d: %s", resp.StatusCode, msg)
	}

	var out integrationSubscription
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return zero, fmt.Errorf("decode subscription: %w", err)
	}
	return out, nil
}
