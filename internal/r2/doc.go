// Package r2 is a thin wrapper over minio-go/v7 configured for the
// Cloudflare R2 endpoint (§8.6). minio-go was chosen over aws-sdk-go-v2
// for size — ~3 MB vs ~30 MB compiled — with no functional loss for
// the S3-compatible operations we use (Put, Head, Delete, List).
package r2
