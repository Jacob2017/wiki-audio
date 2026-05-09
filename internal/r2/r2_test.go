package r2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestFakePutThenGetRoundTrip(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	body := "hello wiki audio"
	etag, err := fake.PutObject(context.Background(), "pg/essay.mp3", strings.NewReader(body), int64(len(body)), "audio/mpeg")
	if err != nil {
		t.Fatalf("PutObject() error = %v", err)
	}

	wantETag := sha256Hex(body)
	if etag != wantETag {
		t.Fatalf("etag = %q; want %q", etag, wantETag)
	}

	rc, err := fake.GetObject(context.Background(), "pg/essay.mp3")
	if err != nil {
		t.Fatalf("GetObject() error = %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q; want %q", string(got), body)
	}
}

func TestFakeMissingKeyReturnsErrNoSuchKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(*Fake) error
	}{
		{
			name: "get_missing_returns_NoSuchKey",
			call: func(fake *Fake) error {
				rc, err := fake.GetObject(context.Background(), "missing")
				if rc != nil {
					_ = rc.Close()
				}
				return err
			},
		},
		{
			name: "head_missing_returns_NoSuchKey",
			call: func(fake *Fake) error {
				_, err := fake.HeadObject(context.Background(), "missing")
				return err
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := NewFake()
			err := tt.call(fake)
			if !errors.Is(err, ErrNoSuchKey) {
				t.Fatalf("errors.Is(err, ErrNoSuchKey) = false; err = %v", err)
			}
		})
	}
}

func TestFakeHeadAfterPut(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	body := "mp3-bytes"
	etag, err := fake.PutObject(context.Background(), "pg/how-to-do-great-work.mp3", strings.NewReader(body), int64(len(body)), "audio/mpeg")
	if err != nil {
		t.Fatalf("PutObject() error = %v", err)
	}

	info, err := fake.HeadObject(context.Background(), "pg/how-to-do-great-work.mp3")
	if err != nil {
		t.Fatalf("HeadObject() error = %v", err)
	}
	if info.Key != "pg/how-to-do-great-work.mp3" {
		t.Fatalf("Key = %q; want %q", info.Key, "pg/how-to-do-great-work.mp3")
	}
	if info.ETag != etag {
		t.Fatalf("ETag = %q; want %q", info.ETag, etag)
	}
	if info.Size != int64(len(body)) {
		t.Fatalf("Size = %d; want %d", info.Size, len(body))
	}
	if info.ContentType != "audio/mpeg" {
		t.Fatalf("ContentType = %q; want %q", info.ContentType, "audio/mpeg")
	}
}

func TestFakeListPrefixFiltersCorrectly(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	mustPut(t, fake, "pg/a.mp3", "a", "audio/mpeg")
	mustPut(t, fake, "pg/nested/b.mp3", "b", "audio/mpeg")
	mustPut(t, fake, "manifests/pg.manifest.json", "{}", "application/json")

	got, err := fake.ListObjects(context.Background(), "pg/")
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if got == nil {
		t.Fatal("ListObjects() returned nil slice")
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d; want 2", len(got))
	}
	if got[0].Key != "pg/a.mp3" || got[1].Key != "pg/nested/b.mp3" {
		t.Fatalf("keys = [%q, %q]; want [pg/a.mp3, pg/nested/b.mp3]", got[0].Key, got[1].Key)
	}
}

func TestFakeListEmptyReturnsNonNilSlice(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	got, err := fake.ListObjects(context.Background(), "pg/")
	if err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if got == nil {
		t.Fatal("ListObjects() returned nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("len(got) = %d; want 0", len(got))
	}
}

func TestFakeDeleteRemovesKey(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	mustPut(t, fake, "pg/to-delete.mp3", "body", "audio/mpeg")

	if err := fake.DeleteObject(context.Background(), "pg/to-delete.mp3"); err != nil {
		t.Fatalf("DeleteObject() error = %v", err)
	}
	if _, err := fake.HeadObject(context.Background(), "pg/to-delete.mp3"); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("HeadObject() after delete should return ErrNoSuchKey; got %v", err)
	}
}

func TestFakePutOverwritesExisting(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	etag1, err := fake.PutObject(context.Background(), "pg/essay.mp3", strings.NewReader("old"), 3, "audio/mpeg")
	if err != nil {
		t.Fatalf("first PutObject() error = %v", err)
	}
	etag2, err := fake.PutObject(context.Background(), "pg/essay.mp3", strings.NewReader("new-body"), int64(len("new-body")), "audio/mpeg")
	if err != nil {
		t.Fatalf("second PutObject() error = %v", err)
	}
	if etag1 == etag2 {
		t.Fatalf("etag overwrite should change; both = %q", etag1)
	}
	info, err := fake.HeadObject(context.Background(), "pg/essay.mp3")
	if err != nil {
		t.Fatalf("HeadObject() error = %v", err)
	}
	if info.Size != int64(len("new-body")) {
		t.Fatalf("Size = %d; want %d", info.Size, len("new-body"))
	}
}

func TestFakeConcurrentPutGet(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "pg/object-" + string(rune('a'+i)) + ".mp3"
			body := strings.Repeat("x", i+1)
			if _, err := fake.PutObject(context.Background(), key, strings.NewReader(body), int64(len(body)), "audio/mpeg"); err != nil {
				t.Errorf("PutObject(%q) error = %v", key, err)
				return
			}
			rc, err := fake.GetObject(context.Background(), key)
			if err != nil {
				t.Errorf("GetObject(%q) error = %v", key, err)
				return
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Errorf("ReadAll(%q) error = %v", key, err)
				return
			}
			if string(got) != body {
				t.Errorf("body for %q = %q; want %q", key, string(got), body)
			}
		}(i)
	}
	wg.Wait()
}

func TestFakeRecordsOperations(t *testing.T) {
	t.Parallel()

	fake := NewFake()
	mustPut(t, fake, "pg/a.mp3", "a", "audio/mpeg")
	rc, err := fake.GetObject(context.Background(), "pg/a.mp3")
	if err != nil {
		t.Fatalf("GetObject() error = %v", err)
	}
	_ = rc.Close()
	if _, err := fake.HeadObject(context.Background(), "pg/a.mp3"); err != nil {
		t.Fatalf("HeadObject() error = %v", err)
	}
	if _, err := fake.ListObjects(context.Background(), "pg/"); err != nil {
		t.Fatalf("ListObjects() error = %v", err)
	}
	if err := fake.DeleteObject(context.Background(), "pg/a.mp3"); err != nil {
		t.Fatalf("DeleteObject() error = %v", err)
	}

	ops := fake.Operations()
	want := []string{"PutObject", "GetObject", "HeadObject", "ListObjects", "DeleteObject"}
	if len(ops) != len(want) {
		t.Fatalf("len(ops) = %d; want %d", len(ops), len(want))
	}
	for i, name := range want {
		if ops[i].Name != name {
			t.Fatalf("ops[%d].Name = %q; want %q", i, ops[i].Name, name)
		}
	}
}

func mustPut(t *testing.T, fake *Fake, key, body, contentType string) {
	t.Helper()
	if _, err := fake.PutObject(context.Background(), key, strings.NewReader(body), int64(len(body)), contentType); err != nil {
		t.Fatalf("PutObject(%q) error = %v", key, err)
	}
}

func sha256Hex(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
