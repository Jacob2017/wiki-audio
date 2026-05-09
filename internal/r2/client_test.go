package r2

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

// Tests for the minio-backed Client. Covers the constructor's input
// validation + endpoint normalization, and mapError's classification
// of common S3 ErrorResponse codes. Behavioral round-trips through a
// live R2 endpoint live in wa-i1l.18 (integration bead, gated by
// WIKI_AUDIO_RUN_INTEGRATION). The Fake's contract is pinned by the
// pane-9 r2_test.go suite; THIS file pins the seams that the Fake
// can't touch.

// --- New ---

func TestNewRejectsEmptyInputs(t *testing.T) {
	cases := []struct {
		name                                 string
		endpoint, accessKey, secretKey, bkt  string
		want                                 string
	}{
		{"missing endpoint", "", "ak", "sk", "b", "endpoint"},
		{"missing accessKey", "h.example.com", "", "sk", "b", "accessKey"},
		{"missing secretKey", "h.example.com", "ak", "", "b", "secretKey"},
		{"missing bucket", "h.example.com", "ak", "sk", "", "bucket"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.endpoint, c.accessKey, c.secretKey, c.bkt)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error should name the missing field; got: %v", err)
			}
		})
	}
}

func TestNewAcceptsHostnameOrFullURL(t *testing.T) {
	cases := []string{
		"abc.r2.cloudflarestorage.com",
		"https://abc.r2.cloudflarestorage.com",
		"https://abc.r2.cloudflarestorage.com/",
		"http://abc.r2.cloudflarestorage.com",
	}
	for _, ep := range cases {
		t.Run(ep, func(t *testing.T) {
			c, err := New(ep, "ak", "sk", "wiki-audio")
			if err != nil {
				t.Fatalf("New(%q): %v", ep, err)
			}
			if c == nil || c.mc == nil {
				t.Fatalf("nil client / nil minio client")
			}
			if c.bucket != "wiki-audio" {
				t.Errorf("bucket = %q", c.bucket)
			}
		})
	}
}

// --- mapError classification ---

// fakeMinioErr wraps minio's ErrorResponse so a test can synthesize
// a server response without sending a real HTTP request. Mirrors how
// minio-go itself constructs ErrorResponse from response bodies.
func fakeMinioErr(code, message string) error {
	return minio.ErrorResponse{
		Code:       code,
		Message:    message,
		StatusCode: http.StatusBadRequest, // not load-bearing for mapError
	}
}

func TestMapErrorClassifiesNoSuchKey(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NoSuchBucket"} {
		t.Run(code, func(t *testing.T) {
			got := mapError(fakeMinioErr(code, "the thing"))
			if !errors.Is(got, ErrNoSuchKey) {
				t.Errorf("expected ErrNoSuchKey for %q; got %v", code, got)
			}
		})
	}
}

func TestMapErrorClassifiesAccessDenied(t *testing.T) {
	codes := []string{
		"AccessDenied",
		"InvalidAccessKeyId",
		"SignatureDoesNotMatch",
		"InvalidSignature",
		"ExpiredToken",
		"TokenRefreshRequired",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			got := mapError(fakeMinioErr(code, "nope"))
			if !errors.Is(got, ErrAccessDenied) {
				t.Errorf("expected ErrAccessDenied for %q; got %v", code, got)
			}
			// The code should appear in the wrapped message so log
			// surfaces can distinguish "bad key" from "expired token"
			// without needing the underlying error type.
			if !strings.Contains(got.Error(), code) {
				t.Errorf("error string should include code %q for log-side diagnosis; got %q",
					code, got.Error())
			}
		})
	}
}

func TestMapErrorClassifiesThrottled(t *testing.T) {
	codes := []string{"SlowDown", "RequestTimeTooSkewed", "ServiceUnavailable"}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			got := mapError(fakeMinioErr(code, "back off"))
			if !errors.Is(got, ErrThrottled) {
				t.Errorf("expected ErrThrottled for %q; got %v", code, got)
			}
		})
	}
}

// Transport-level errors (DNS, connect refused, mid-stream EOF, etc.)
// have no S3 ErrorCode. mapError classifies them as ErrNetwork and
// preserves the underlying error in the wrap chain so errors.Unwrap
// surfaces the cause.
func TestMapErrorTransportFallsBackToNetwork(t *testing.T) {
	transport := errors.New("dial tcp: connect: connection refused")
	got := mapError(transport)
	if !errors.Is(got, ErrNetwork) {
		t.Errorf("expected ErrNetwork for transport error; got %v", got)
	}
	// Underlying error is preserved.
	if !errors.Is(got, transport) {
		t.Errorf("underlying transport error should be in the wrap chain; got %v", got)
	}
}

// Unknown S3 codes pass through unchanged so a hypothetical future
// caller can switch on the original minio error if it cares.
func TestMapErrorUnknownCodePassesThrough(t *testing.T) {
	original := fakeMinioErr("FutureSomethingWeDontKnow", "?")
	got := mapError(original)
	if !errors.Is(got, original) {
		t.Errorf("unknown S3 code should pass through; got %v", got)
	}
	// Must NOT be classified as one of the named sentinels.
	for _, sentinel := range []error{ErrNoSuchKey, ErrAccessDenied, ErrThrottled, ErrNetwork} {
		if errors.Is(got, sentinel) {
			t.Errorf("unknown code mis-classified as %v", sentinel)
		}
	}
}

func TestMapErrorNilIsNil(t *testing.T) {
	if got := mapError(nil); got != nil {
		t.Errorf("mapError(nil) = %v, want nil", got)
	}
}

// --- compile-time interface conformance ---

// A redundant compile-time check (already in client.go) that the
// real Client and the Fake satisfy the same Storage interface. If a
// future Storage method is added without updating both impls, this
// test file fails to compile, surfacing the gap before any runtime
// path hits the missing method.
var (
	_ Storage = (*Client)(nil)
	_ Storage = (*Fake)(nil)
)
