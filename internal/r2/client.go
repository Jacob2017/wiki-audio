package r2

import "github.com/minio/minio-go/v7"

// Client wraps a minio-go client targeted at the Cloudflare R2 endpoint.
// Fleshed out in Phase E — see wa-cfn.* beads.
type Client struct {
	mc *minio.Client
}

// New constructs an R2 client. Stub: not yet implemented.
func New(endpoint, accessKey, secretKey, bucket string) (*Client, error) {
	return nil, errStr("r2.New: not yet implemented")
}

type errStr string

func (e errStr) Error() string { return string(e) }
