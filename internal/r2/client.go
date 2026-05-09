package r2

import (
	"context"
	"errors"
	"io"

	"github.com/minio/minio-go/v7"
)

// ErrNoSuchKey is the typed missing-object sentinel used across the
// fake and the future minio-backed client. Callers should branch on
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
// client in wa-i1l.1 and the in-memory Fake in wa-i1l.17 both satisfy
// it so manifest/feed/publish tests can run without live R2.
type Storage interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (etag string, err error)
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	HeadObject(ctx context.Context, key string) (ObjectInfo, error)
	DeleteObject(ctx context.Context, key string) error
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)
}

// Client wraps a minio-go client targeted at the Cloudflare R2
// endpoint. The real behavior lands in wa-i1l.1; wa-i1l.17 defines
// the stable public surface first so downstream tests can consume the
// Fake immediately.
type Client struct {
	mc     *minio.Client
	bucket string
}

// New constructs an R2 client. Stub: not yet implemented.
func New(endpoint, accessKey, secretKey, bucket string) (*Client, error) {
	return nil, errStr("r2.New: not yet implemented")
}

func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	return "", errStr("r2.Client.PutObject: not yet implemented")
}

func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errStr("r2.Client.GetObject: not yet implemented")
}

func (c *Client) HeadObject(ctx context.Context, key string) (ObjectInfo, error) {
	return ObjectInfo{}, errStr("r2.Client.HeadObject: not yet implemented")
}

func (c *Client) DeleteObject(ctx context.Context, key string) error {
	return errStr("r2.Client.DeleteObject: not yet implemented")
}

func (c *Client) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	return nil, errStr("r2.Client.ListObjects: not yet implemented")
}

type errStr string

func (e errStr) Error() string { return string(e) }

var _ Storage = (*Client)(nil)
