package r2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNoSuchKey is the typed missing-object sentinel used across the
// fake and the minio-backed client. Callers should branch on
// errors.Is(err, ErrNoSuchKey), not string matching.
var ErrNoSuchKey = errors.New("r2: no such key")

// ObjectInfo is the metadata surface shared by HeadObject and
// ListObjects. ContentType is included so callers and tests can
// reason about feed / manifest / audio object uploads without
// fetching the full body.
type ObjectInfo struct {
	Key         string
	ETag        string
	Size        int64
	ContentType string
}

// Storage is the Phase F object-store contract. The minio-backed
// Client below and the in-memory Fake (fake.go) both satisfy it so
// manifest / feed / publish tests can run without live R2.
type Storage interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (etag string, err error)
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	HeadObject(ctx context.Context, key string) (ObjectInfo, error)
	DeleteObject(ctx context.Context, key string) error
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// Client wraps a minio-go client targeted at the Cloudflare R2
// endpoint. Region is pinned to "auto" — R2 uses a single global
// region keyword regardless of which physical region serves a given
// bucket. Secure: true forces HTTPS; R2 rejects plain HTTP.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New constructs an R2 Client. The endpoint is the bucket's R2
// hostname — `<account_id>.r2.cloudflarestorage.com`. A leading
// `https://` or `http://` is stripped so callers can pass either the
// hostname (what minio-go wants) or the full URL (what config files
// often carry verbatim from the Cloudflare dashboard).
func New(endpoint, accessKey, secretKey, bucket string) (*Client, error) {
	if endpoint == "" {
		return nil, errors.New("r2.New: endpoint is required")
	}
	if accessKey == "" {
		return nil, errors.New("r2.New: accessKey is required")
	}
	if secretKey == "" {
		return nil, errors.New("r2.New: secretKey is required")
	}
	if bucket == "" {
		return nil, errors.New("r2.New: bucket is required")
	}

	host := strings.TrimPrefix(endpoint, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")

	mc, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true,
		Region: "auto",
	})
	if err != nil {
		return nil, fmt.Errorf("r2.New: minio.New: %w", err)
	}
	return &Client{mc: mc, bucket: bucket}, nil
}

// PutObject streams r into bucket/key and returns the server-assigned
// ETag (quotes stripped — minio-go preserves the wire-format quoted
// ETag, callers want the bare hex). Pass size = -1 to enable
// multipart upload of an unknown-length body; for our manifest/MP3
// uploads the size is always known and passing it lets minio-go pick
// a single-part PUT.
func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	info, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", mapError(err)
	}
	return strings.Trim(info.ETag, `"`), nil
}

// GetObject returns a streaming reader for bucket/key. The minio-go
// Get is lazy — it doesn't issue the HTTP request until the first
// Read or Stat. We Stat() up front so a NoSuchKey surfaces as a
// typed error here instead of mid-stream when a downstream copy
// pipeline has already opened a destination file. The Stat() round
// trip is one HEAD request; the body request follows on first Read.
//
// Callers MUST Close the returned reader. The reader is not safe for
// concurrent reads (per minio-go *Object semantics).
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapError(err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, mapError(err)
	}
	return obj, nil
}

// HeadObject is a metadata-only fetch (no body). Cheap; used by the
// publish-diff path (wa-i1l.3) to compare server ETag against the
// local manifest without paying download cost.
func (c *Client) HeadObject(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, mapError(err)
	}
	return ObjectInfo{
		Key:         key,
		ETag:        strings.Trim(info.ETag, `"`),
		Size:        info.Size,
		ContentType: info.ContentType,
	}, nil
}

// DeleteObject removes bucket/key. Idempotency on the wire is
// up to the server: S3 / R2 returns 204 whether or not the key
// existed. We surface a typed ErrNoSuchKey only when the server
// explicitly says NoSuchKey — many real R2 deletes silently return
// "deleted" even for missing keys.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	if err := c.mc.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return mapError(err)
	}
	return nil
}

// ListObjects walks every object under prefix, recursively. Returns
// a sorted []ObjectInfo so the publish-diff produces stable output
// across runs (matches the Fake's contract). For our scale (53 PG
// essays under `pg/`) the full list comfortably fits in memory; if
// that ever changes, swap in a streaming variant.
func (c *Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	out := make([]ObjectInfo, 0)
	for obj := range c.mc.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, mapError(obj.Err)
		}
		out = append(out, ObjectInfo{
			Key:         obj.Key,
			ETag:        strings.Trim(obj.ETag, `"`),
			Size:        obj.Size,
			ContentType: obj.ContentType,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// Compile-time interface conformance — the Fake also asserts this in
// fake.go, so a Storage interface change forces both impls to follow.
var _ Storage = (*Client)(nil)
