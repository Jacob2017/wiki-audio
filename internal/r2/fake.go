package r2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Operation records one Fake storage call. Tests in this and
// downstream packages can inspect the sequence without reaching into
// internal maps.
type Operation struct {
	Name        string
	Key         string
	Prefix      string
	Size        int64
	ContentType string
}

type fakeObject struct {
	body        []byte
	etag        string
	contentType string
}

// Fake is an in-memory, thread-safe Storage implementation for unit
// tests. It stores exact body bytes plus metadata and returns stable,
// deterministic list ordering.
type Fake struct {
	mu   sync.RWMutex
	objs map[string]fakeObject
	ops  []Operation
}

// NewFake constructs an empty Fake with a non-nil backing map.
func NewFake() *Fake {
	return &Fake{
		objs: make(map[string]fakeObject),
		ops:  make([]Operation, 0),
	}
}

func (f *Fake) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("fake r2 put %q: read body: %w", key, err)
	}
	if size >= 0 && int64(len(body)) != size {
		return "", fmt.Errorf("fake r2 put %q: size mismatch: got %d want %d", key, len(body), size)
	}

	etag := hexDigest(body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objs[key] = fakeObject{
		body:        bytes.Clone(body),
		etag:        etag,
		contentType: contentType,
	}
	f.ops = append(f.ops, Operation{
		Name:        "PutObject",
		Key:         key,
		Size:        int64(len(body)),
		ContentType: contentType,
	})
	return etag, nil
}

func (f *Fake) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, Operation{Name: "GetObject", Key: key})
	obj, ok := f.objs[key]
	if !ok {
		return nil, fmt.Errorf("fake r2 get %q: %w", key, ErrNoSuchKey)
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(obj.body))), nil
}

func (f *Fake) HeadObject(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, Operation{Name: "HeadObject", Key: key})
	obj, ok := f.objs[key]
	if !ok {
		return ObjectInfo{}, fmt.Errorf("fake r2 head %q: %w", key, ErrNoSuchKey)
	}
	return ObjectInfo{
		Key:         key,
		ETag:        obj.etag,
		Size:        int64(len(obj.body)),
		ContentType: obj.contentType,
	}, nil
}

func (f *Fake) DeleteObject(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, Operation{Name: "DeleteObject", Key: key})
	if _, ok := f.objs[key]; !ok {
		return fmt.Errorf("fake r2 delete %q: %w", key, ErrNoSuchKey)
	}
	delete(f.objs, key)
	return nil
}

func (f *Fake) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, Operation{Name: "ListObjects", Prefix: prefix})

	out := make([]ObjectInfo, 0)
	for key, obj := range f.objs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, ObjectInfo{
			Key:         key,
			ETag:        obj.etag,
			Size:        int64(len(obj.body)),
			ContentType: obj.contentType,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// Operations returns a snapshot of the recorded call log.
func (f *Fake) Operations() []Operation {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Operation, len(f.ops))
	copy(out, f.ops)
	return out
}

func hexDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

var _ Storage = (*Fake)(nil)
