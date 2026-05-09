package tts

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxRetryAfter = 300 * time.Second
	maxBackoff    = 60 * time.Second
)

type retryVerdict uint8

const (
	retryVerdictSuccess retryVerdict = iota
	retryVerdictRetryable
	retryVerdictFatal
)

func (v retryVerdict) String() string {
	switch v {
	case retryVerdictSuccess:
		return "success"
	case retryVerdictRetryable:
		return "retryable"
	case retryVerdictFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

type retryDecision struct {
	verdict retryVerdict
	sleep   time.Duration
}

// classifyAPIError adapts the APIError shape from client.go into the
// retry classifier. If the caller still has the live http.Response,
// prefer classifyHTTPResponse so Retry-After headers can be honored.
func classifyHTTPResponse(resp *http.Response, attempt int, baseSeconds float64, rng *rand.Rand, now time.Time) retryDecision {
	if resp == nil {
		return retryDecision{verdict: retryVerdictFatal}
	}
	return classifyHTTPStatus(resp.StatusCode, resp.Header, attempt, baseSeconds, rng, now)
}

func classifyAPIError(apiErr *APIError, attempt int, baseSeconds float64, rng *rand.Rand) retryDecision {
	if apiErr == nil {
		return retryDecision{verdict: retryVerdictSuccess}
	}
	if apiErr.Retryable {
		return retryDecision{verdict: retryVerdictRetryable, sleep: computeBackoff(attempt, baseSeconds, rng)}
	}
	return retryDecision{verdict: retryVerdictFatal}
}

func classifyHTTPStatus(statusCode int, headers http.Header, attempt int, baseSeconds float64, rng *rand.Rand, now time.Time) retryDecision {
	switch {
	case statusCode == http.StatusOK:
		return retryDecision{verdict: retryVerdictSuccess}
	case statusCode == http.StatusTooManyRequests:
		if sleep, ok := parseRetryAfter(headers, now); ok {
			return retryDecision{verdict: retryVerdictRetryable, sleep: sleep}
		}
		return retryDecision{verdict: retryVerdictRetryable, sleep: computeBackoff(attempt, baseSeconds, rng)}
	case statusCode == http.StatusPaymentRequired || statusCode == http.StatusForbidden:
		return retryDecision{verdict: retryVerdictFatal}
	case statusCode >= 500 && statusCode <= 599:
		return retryDecision{verdict: retryVerdictRetryable, sleep: computeBackoff(attempt, baseSeconds, rng)}
	case statusCode >= 400 && statusCode <= 499:
		return retryDecision{verdict: retryVerdictFatal}
	default:
		return retryDecision{verdict: retryVerdictFatal}
	}
}

func classifyTransportError(err error, attempt int, baseSeconds float64, rng *rand.Rand) retryDecision {
	if err == nil {
		return retryDecision{verdict: retryVerdictSuccess}
	}
	if isRetryableTransportError(err) {
		return retryDecision{verdict: retryVerdictRetryable, sleep: computeBackoff(attempt, baseSeconds, rng)}
	}
	return retryDecision{verdict: retryVerdictFatal}
}

func computeBackoff(attempt int, baseSeconds float64, rng *rand.Rand) time.Duration {
	if baseSeconds < 0 {
		baseSeconds = 0
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	seconds := (baseSeconds * math.Pow(2, float64(attempt))) + rng.Float64()
	sleep := time.Duration(seconds * float64(time.Second))
	if sleep > maxBackoff {
		return maxBackoff
	}
	return sleep
}

func parseRetryAfter(headers http.Header, now time.Time) (time.Duration, bool) {
	if headers == nil {
		return 0, false
	}
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}

	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		switch {
		case seconds < 0:
			return 0, false
		case seconds == 0:
			return 0, true
		case seconds > int64(maxRetryAfter/time.Second):
			return 0, false
		default:
			return time.Duration(seconds) * time.Second, true
		}
	}

	if now.IsZero() {
		now = time.Now()
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	delta := when.Sub(now)
	if delta < 0 || delta > maxRetryAfter {
		return 0, false
	}
	return delta, true
}

func isRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection reset by peer"),
		strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "server closed idle connection"),
		strings.Contains(msg, "stream error"):
		return true
	default:
		return false
	}
}
