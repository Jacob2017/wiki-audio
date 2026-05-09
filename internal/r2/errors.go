package r2

import (
	"errors"
	"fmt"

	"github.com/minio/minio-go/v7"
)

// Typed error sentinels. Callers branch on these via errors.Is so
// the publish path (wa-i1l.4) can distinguish "object missing —
// upload it" from "auth failed — abort the run" without scraping
// strings.
//
// AlreadyExists isn't here because S3/R2 PutObject is overwrite-by-
// default; there's no client-facing "already exists" response. If
// future bucket-level operations need it (CreateBucket etc.), add
// then.
var (
	// ErrNoSuchKey is defined in client.go (next to ObjectInfo)
	// because the Fake also reaches for it.

	// ErrAccessDenied wraps 403-class responses (signature failure,
	// missing IAM permissions, expired credentials).
	ErrAccessDenied = errors.New("r2: access denied")

	// ErrThrottled wraps R2's rate-limit / slow-down responses.
	// wa-i1l.4's uploader should treat this as retryable with
	// backoff (the EL retry classifier from wa-3gf is the model).
	ErrThrottled = errors.New("r2: throttled")

	// ErrNetwork wraps any non-S3 transport-level failure: DNS,
	// connect refused, TLS handshake, mid-stream EOF. The wrapped
	// error preserves the underlying cause for log-side diagnosis.
	ErrNetwork = errors.New("r2: network error")
)

// mapError classifies a minio-go return value into one of the typed
// sentinels above (or ErrNoSuchKey, defined in client.go). Returns
// nil unchanged.
//
// The classification is by S3 ErrorCode. minio.ToErrorResponse
// returns a zero-value ErrorResponse (Code="") for non-S3 errors
// (transport, DNS, etc.) — those route to ErrNetwork.
//
// fmt.Errorf("%w: %s", sentinel, msg) preserves the sentinel for
// errors.Is while keeping the server-side message string for log
// surfaces. Don't switch this to %v, %w on the inner err — callers
// branch on the sentinel, not on equality with the wire error.
func mapError(err error) error {
	if err == nil {
		return nil
	}

	er := minio.ToErrorResponse(err)
	switch er.Code {
	case "":
		// Not an S3 error — transport-level. Could be net.Error,
		// context.DeadlineExceeded, mid-stream io.ErrUnexpectedEOF,
		// etc. Wrap behind ErrNetwork so the uploader's retry path
		// can branch on the sentinel and the operator still sees
		// the underlying cause via errors.Unwrap chain.
		return fmt.Errorf("%w: %w", ErrNetwork, err)

	case "NoSuchKey", "NoSuchBucket":
		// NoSuchBucket is rare in production (config validation
		// catches an absent bucket at doctor time) but we map it
		// here for symmetry — both surface as "the thing you
		// asked for isn't there."
		return fmt.Errorf("%w: %s", ErrNoSuchKey, er.Message)

	case "AccessDenied", "InvalidAccessKeyId",
		"SignatureDoesNotMatch", "InvalidSignature",
		"ExpiredToken", "TokenRefreshRequired":
		return fmt.Errorf("%w: %s (code=%s)", ErrAccessDenied, er.Message, er.Code)

	case "SlowDown", "RequestTimeTooSkewed", "ServiceUnavailable":
		// SlowDown is the canonical R2 rate-limit code.
		// RequestTimeTooSkewed surfaces when local clock drifts
		// >15 min from the server; technically not throttling but
		// callers should retry after fixing the clock.
		// ServiceUnavailable from R2 is treated as throttled —
		// retryable with backoff.
		return fmt.Errorf("%w: %s (code=%s)", ErrThrottled, er.Message, er.Code)

	default:
		// An S3 ErrorCode we don't classify. Return the original
		// minio error so callers can switch on it if they care;
		// most won't.
		return err
	}
}
