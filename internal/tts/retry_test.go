package tts

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const retryBaseSeconds = 2.0

func TestClassifyTransportError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantVerdict  retryVerdict
		wantSleepMin time.Duration
		wantSleepMax time.Duration
	}{
		{
			name:         "timeout_deadline_exceeded",
			err:          context.DeadlineExceeded,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "timeout_net_timeout",
			err:          timeoutError{},
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "network_error_dns",
			err:          &net.DNSError{Err: "temporary failure", Name: "api.elevenlabs.io", IsTemporary: true},
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "connection_reset",
			err:          errors.New("read tcp 127.0.0.1: connection reset by peer"),
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:        "other_error_is_fatal",
			err:         errors.New("bad request"),
			wantVerdict: retryVerdictFatal,
		},
		{
			name:        "nil_error_is_success",
			err:         nil,
			wantVerdict: retryVerdictSuccess,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logBuf strings.Builder
			logger := slog.New(slog.NewTextHandler(&logBuf, nil)).With("test", t.Name())

			got := classifyTransportError(tt.err, 0, retryBaseSeconds, deterministicRand())
			logger.Info("classified", "verdict", got.verdict.String(), "sleep", got.sleep)

			if got.verdict != tt.wantVerdict {
				t.Fatalf("verdict = %s; want %s", got.verdict, tt.wantVerdict)
			}
			if tt.wantVerdict == retryVerdictRetryable {
				if got.sleep < tt.wantSleepMin || got.sleep >= tt.wantSleepMax {
					t.Fatalf("sleep = %v; want in [%v, %v)", got.sleep, tt.wantSleepMin, tt.wantSleepMax)
				}
			} else if got.sleep != 0 {
				t.Fatalf("sleep = %v; want 0 for %s", got.sleep, tt.wantVerdict)
			}
			if !strings.Contains(logBuf.String(), "verdict="+tt.wantVerdict.String()) {
				t.Fatalf("log missing verdict %q: %q", tt.wantVerdict, logBuf.String())
			}
		})
	}
}

func TestClassifyHTTPResponse(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 9, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		statusCode   int
		headers      map[string]string
		wantVerdict  retryVerdict
		wantSleep    time.Duration
		wantSleepMin time.Duration
		wantSleepMax time.Duration
	}{
		{
			name:        "http_200",
			statusCode:  http.StatusOK,
			wantVerdict: retryVerdictSuccess,
		},
		{
			name:        "http_429_with_retry_after",
			statusCode:  http.StatusTooManyRequests,
			headers:     map[string]string{"Retry-After": "17"},
			wantVerdict: retryVerdictRetryable,
			wantSleep:   17 * time.Second,
		},
		{
			name:         "http_429_no_retry_after",
			statusCode:   http.StatusTooManyRequests,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "http_500",
			statusCode:   http.StatusInternalServerError,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "http_502",
			statusCode:   http.StatusBadGateway,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "http_503",
			statusCode:   http.StatusServiceUnavailable,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "http_504",
			statusCode:   http.StatusGatewayTimeout,
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:        "http_402",
			statusCode:  http.StatusPaymentRequired,
			wantVerdict: retryVerdictFatal,
		},
		{
			name:        "http_403",
			statusCode:  http.StatusForbidden,
			wantVerdict: retryVerdictFatal,
		},
		{
			name:        "http_400",
			statusCode:  http.StatusBadRequest,
			wantVerdict: retryVerdictFatal,
		},
		{
			name:        "http_404",
			statusCode:  http.StatusNotFound,
			wantVerdict: retryVerdictFatal,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logBuf strings.Builder
			logger := slog.New(slog.NewTextHandler(&logBuf, nil)).With("test", t.Name())

			resp := responseFromServer(t, tt.statusCode, tt.headers)
			t.Cleanup(func() { _ = resp.Body.Close() })

			got := classifyHTTPResponse(resp, 0, retryBaseSeconds, deterministicRand(), now)
			logger.Info("classified", "verdict", got.verdict.String(), "sleep", got.sleep, "status", tt.statusCode)

			if got.verdict != tt.wantVerdict {
				t.Fatalf("verdict = %s; want %s", got.verdict, tt.wantVerdict)
			}
			switch {
			case tt.wantSleep != 0:
				if got.sleep != tt.wantSleep {
					t.Fatalf("sleep = %v; want %v", got.sleep, tt.wantSleep)
				}
			case tt.wantVerdict == retryVerdictRetryable:
				if got.sleep < tt.wantSleepMin || got.sleep >= tt.wantSleepMax {
					t.Fatalf("sleep = %v; want in [%v, %v)", got.sleep, tt.wantSleepMin, tt.wantSleepMax)
				}
			default:
				if got.sleep != 0 {
					t.Fatalf("sleep = %v; want 0 for %s", got.sleep, tt.wantVerdict)
				}
			}
			if !strings.Contains(logBuf.String(), "verdict="+tt.wantVerdict.String()) {
				t.Fatalf("log missing verdict %q: %q", tt.wantVerdict, logBuf.String())
			}
		})
	}
}

func TestClassifyAPIError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          *APIError
		wantVerdict  retryVerdict
		wantSleepMin time.Duration
		wantSleepMax time.Duration
	}{
		{
			name:        "nil_error_is_success",
			err:         nil,
			wantVerdict: retryVerdictSuccess,
		},
		{
			name: "retryable_429_falls_back_to_computed_backoff",
			err: &APIError{
				StatusCode: http.StatusTooManyRequests,
				Retryable:  true,
			},
			wantVerdict:  retryVerdictRetryable,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name: "fatal_401_stays_fatal",
			err: &APIError{
				StatusCode: http.StatusUnauthorized,
				Retryable:  false,
			},
			wantVerdict: retryVerdictFatal,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := classifyAPIError(tt.err, 0, retryBaseSeconds, deterministicRand())
			if got.verdict != tt.wantVerdict {
				t.Fatalf("verdict = %s; want %s", got.verdict, tt.wantVerdict)
			}
			if tt.wantVerdict == retryVerdictRetryable {
				if got.sleep < tt.wantSleepMin || got.sleep >= tt.wantSleepMax {
					t.Fatalf("sleep = %v; want in [%v, %v)", got.sleep, tt.wantSleepMin, tt.wantSleepMax)
				}
				return
			}
			if got.sleep != 0 {
				t.Fatalf("sleep = %v; want 0 for %s", got.sleep, tt.wantVerdict)
			}
		})
	}
}

func TestRetryAfterValidation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.May, 9, 5, 0, 0, 0, time.UTC)
	httpDate := now.Add(30 * time.Second).Format(http.TimeFormat)
	pastDate := now.Add(-30 * time.Second).Format(http.TimeFormat)

	tests := []struct {
		name         string
		headers      map[string]string
		attempt      int
		wantSleep    time.Duration
		wantSleepMin time.Duration
		wantSleepMax time.Duration
	}{
		{
			name:         "retry_after_negative_falls_back",
			headers:      map[string]string{"Retry-After": "-5"},
			attempt:      1,
			wantSleepMin: 4 * time.Second,
			wantSleepMax: 5 * time.Second,
		},
		{
			name:         "retry_after_garbage_falls_back",
			headers:      map[string]string{"Retry-After": "banana"},
			attempt:      1,
			wantSleepMin: 4 * time.Second,
			wantSleepMax: 5 * time.Second,
		},
		{
			name:         "retry_after_huge_falls_back",
			headers:      map[string]string{"Retry-After": "99999"},
			attempt:      1,
			wantSleepMin: 4 * time.Second,
			wantSleepMax: 5 * time.Second,
		},
		{
			name:      "retry_after_zero_uses_zero",
			headers:   map[string]string{"Retry-After": "0"},
			wantSleep: 0,
		},
		{
			name:         "retry_after_http_date_in_past",
			headers:      map[string]string{"Retry-After": pastDate},
			attempt:      1,
			wantSleepMin: 4 * time.Second,
			wantSleepMax: 5 * time.Second,
		},
		{
			name:      "retry_after_http_date_within_window",
			headers:   map[string]string{"Retry-After": httpDate},
			wantSleep: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := responseFromServer(t, http.StatusTooManyRequests, tt.headers)
			t.Cleanup(func() { _ = resp.Body.Close() })

			got := classifyHTTPResponse(resp, tt.attempt, retryBaseSeconds, deterministicRand(), now)
			if got.verdict != retryVerdictRetryable {
				t.Fatalf("verdict = %s; want %s", got.verdict, retryVerdictRetryable)
			}
			if tt.wantSleep != 0 || tt.name == "retry_after_zero_uses_zero" {
				if got.sleep != tt.wantSleep {
					t.Fatalf("sleep = %v; want %v", got.sleep, tt.wantSleep)
				}
				return
			}
			if got.sleep < tt.wantSleepMin || got.sleep >= tt.wantSleepMax {
				t.Fatalf("sleep = %v; want in [%v, %v)", got.sleep, tt.wantSleepMin, tt.wantSleepMax)
			}
		})
	}
}

func TestComputeBackoffSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		attempt      int
		baseSeconds  float64
		wantSleepMin time.Duration
		wantSleepMax time.Duration
	}{
		{
			name:         "attempt_0_in_range",
			attempt:      0,
			baseSeconds:  2,
			wantSleepMin: 2 * time.Second,
			wantSleepMax: 3 * time.Second,
		},
		{
			name:         "attempt_1_in_range",
			attempt:      1,
			baseSeconds:  2,
			wantSleepMin: 4 * time.Second,
			wantSleepMax: 5 * time.Second,
		},
		{
			name:         "attempt_2_in_range",
			attempt:      2,
			baseSeconds:  2,
			wantSleepMin: 8 * time.Second,
			wantSleepMax: 9 * time.Second,
		},
		{
			name:         "backoff_cap_60s",
			attempt:      5,
			baseSeconds:  200,
			wantSleepMin: 60 * time.Second,
			wantSleepMax: 60*time.Second + time.Nanosecond,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := computeBackoff(tt.attempt, tt.baseSeconds, deterministicRand())
			if got < tt.wantSleepMin || got >= tt.wantSleepMax {
				t.Fatalf("backoff = %v; want in [%v, %v)", got, tt.wantSleepMin, tt.wantSleepMax)
			}
		})
	}
}

func deterministicRand() *rand.Rand {
	return rand.New(rand.NewSource(1))
}

func responseFromServer(t *testing.T, statusCode int, headers map[string]string) *http.Response {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte("stub"))
	}))
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("GET test server: %v", err)
	}
	return resp
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }
