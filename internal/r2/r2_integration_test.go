//go:build integration

package r2

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/config"
	"github.com/Jacob2017/wiki-audio/internal/testutil"
)

// Live-R2 integration tests for wa-i1l.1 + wa-i1l.4. Build-tag and
// env-var gated per testutil — the canonical commands are:
//
//	# Skip: build tag absent
//	go test ./internal/r2/...
//
//	# Skip: tag on, env-gate off
//	go test -tags=integration ./internal/r2/...
//
//	# Run: tag on, env-gate on
//	WIKI_AUDIO_RUN_INTEGRATION=1 go test -tags=integration ./internal/r2/...
//
// CI runs the unit form only. Integration tests upload <50 MB and
// download same; R2 free egress + ~$0.015/GB-month storage means the
// total spend per run is a fraction of a cent.
//
// Cleanup contract: every key under _integration_test/<run-id>/ gets
// a Delete in the parent t.Cleanup. A failed delete is logged but
// doesn't fail the test (don't mask the underlying failure). The
// run-id prefix means orphans, if any, are easy to scrub via
// `mc rm --recursive r2/wiki-audio/_integration_test/`.

func TestIntegrationR2(t *testing.T) {
	testutil.RequireIntegration(t)
	testutil.RequireCredentials(t, "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY")

	cfgPath := os.Getenv("WIKI_AUDIO_INTEGRATION_CONFIG")
	if cfgPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("integration: cannot resolve home dir for default config: %v", err)
		}
		cfgPath = home + "/.wiki-audio/config.toml"
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Skipf("integration: cannot load %s (set WIKI_AUDIO_INTEGRATION_CONFIG to override): %v", cfgPath, err)
	}
	if cfg.R2.AccountID == "" || cfg.R2.Bucket == "" {
		t.Skip("integration: [r2].account_id + [r2].bucket required in config.toml")
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.R2.AccountID)
	client, err := New(endpoint,
		os.Getenv("R2_ACCESS_KEY_ID"),
		os.Getenv("R2_SECRET_ACCESS_KEY"),
		cfg.R2.Bucket)
	if err != nil {
		t.Fatalf("integration: r2.New: %v", err)
	}

	runID := makeRunID(t)
	prefix := "_integration_test/" + runID + "/"
	logger := slog.With(
		"test", t.Name(),
		"service", "r2",
		"bucket", cfg.R2.Bucket,
		"prefix", prefix,
	)
	logger.Info("integration test starting", "endpoint", endpoint)

	// Track every key we create so the parent t.Cleanup can scrub.
	// sync.Map for concurrent appenders (concurrent_put goroutines).
	var created sync.Map
	track := func(key string) { created.Store(key, struct{}{}) }

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var errCount int
		created.Range(func(k, _ any) bool {
			key := k.(string)
			if err := client.DeleteObject(ctx, key); err != nil {
				// NoSuchKey on cleanup is fine — the test already
				// deleted (e.g. put_overwrite leaves a final, but
				// some test variants delete inline). Anything else
				// gets logged but doesn't fail the run.
				if !errors.Is(err, ErrNoSuchKey) {
					errCount++
					logger.Warn("integration cleanup failed",
						"key", key, "err", err.Error())
				}
			}
			return true
		})
		if errCount > 0 {
			logger.Warn("integration cleanup partial",
				"failed", errCount,
				"hint", "scrub with: mc rm --recursive r2/"+cfg.R2.Bucket+"/"+prefix)
		} else {
			logger.Info("integration cleanup complete")
		}
	})

	// --- subtests ---

	t.Run("put_then_head", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "put_then_head.txt"
		body := []byte("hello R2 — wa-i1l.18 put_then_head")
		etag, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain")
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		track(key)
		if etag == "" {
			t.Errorf("etag empty")
		}

		info, err := client.HeadObject(ctx, key)
		if err != nil {
			t.Fatalf("HeadObject: %v", err)
		}
		if info.Size != int64(len(body)) {
			t.Errorf("size = %d, want %d", info.Size, len(body))
		}
		if info.ETag == "" {
			t.Errorf("HEAD returned empty ETag")
		}
		// minio-go strips quotes from ETag in our wrapper, but
		// being permissive about leading/trailing quotes here
		// guards against a future regression that re-introduces
		// them.
		if strings.Trim(info.ETag, `"`) != strings.Trim(etag, `"`) {
			t.Errorf("HEAD ETag %q differs from PUT ETag %q", info.ETag, etag)
		}
	})

	t.Run("put_then_get", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "put_then_get.bin"
		body := bytes.Repeat([]byte("ABCDEFGHIJ"), 100) // 1 KiB

		if _, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream"); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		track(key)

		rc, err := client.GetObject(ctx, key)
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("body bytes differ: %d bytes got, %d bytes wanted", len(got), len(body))
		}
	})

	t.Run("put_overwrite", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "put_overwrite.txt"
		first := []byte("v1")
		second := []byte("v2 — different content, different size")

		etag1, err := client.PutObject(ctx, key, bytes.NewReader(first), int64(len(first)), "text/plain")
		if err != nil {
			t.Fatalf("first PUT: %v", err)
		}
		track(key)

		etag2, err := client.PutObject(ctx, key, bytes.NewReader(second), int64(len(second)), "text/plain")
		if err != nil {
			t.Fatalf("second PUT: %v", err)
		}
		if etag1 == etag2 {
			t.Errorf("overwrite should produce different etag; got %q twice", etag1)
		}

		info, err := client.HeadObject(ctx, key)
		if err != nil {
			t.Fatalf("HeadObject after overwrite: %v", err)
		}
		if info.Size != int64(len(second)) {
			t.Errorf("size after overwrite = %d, want %d", info.Size, len(second))
		}
	})

	t.Run("head_missing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "definitely-not-there-" + randHex(t, 4) + ".txt"
		_, err := client.HeadObject(ctx, key)
		if err == nil {
			t.Fatal("HeadObject on missing key returned nil err")
		}
		if !errors.Is(err, ErrNoSuchKey) {
			t.Errorf("expected ErrNoSuchKey; got %v", err)
		}
	})

	t.Run("get_missing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "definitely-not-there-" + randHex(t, 4) + ".bin"
		_, err := client.GetObject(ctx, key)
		if err == nil {
			t.Fatal("GetObject on missing key returned nil err")
		}
		if !errors.Is(err, ErrNoSuchKey) {
			t.Errorf("expected ErrNoSuchKey; got %v", err)
		}
	})

	t.Run("list_with_prefix_and_etags", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Put 3 keys under our prefix; verify ListObjects returns
		// only those (filter on the prefix means real episodes
		// at pg/* don't leak into the list) and the listed ETags
		// match what HEAD returns for each key.
		listPrefix := prefix + "list/"
		keys := []string{listPrefix + "a", listPrefix + "b", listPrefix + "c"}
		bodies := map[string][]byte{
			keys[0]: []byte("alpha"),
			keys[1]: []byte("bravo body — slightly longer"),
			keys[2]: []byte("charlie"),
		}
		putETags := make(map[string]string, 3)
		for _, k := range keys {
			etag, err := client.PutObject(ctx, k, bytes.NewReader(bodies[k]), int64(len(bodies[k])), "text/plain")
			if err != nil {
				t.Fatalf("put %s: %v", k, err)
			}
			track(k)
			putETags[k] = etag
		}

		listed, err := client.ListObjects(ctx, listPrefix)
		if err != nil {
			t.Fatalf("ListObjects: %v", err)
		}
		if len(listed) != len(keys) {
			t.Fatalf("listed %d, want %d: %v", len(listed), len(keys), listKeyList(listed))
		}
		// listed is sorted (Client guarantee).
		sortedKeys := append([]string(nil), keys...)
		sort.Strings(sortedKeys)
		for i, info := range listed {
			if info.Key != sortedKeys[i] {
				t.Errorf("listed[%d].Key = %q, want %q", i, info.Key, sortedKeys[i])
			}
			// LIST etags should match PUT etags. R2 normalizes
			// quotes the same way our client does.
			if info.ETag != putETags[info.Key] {
				t.Errorf("LIST etag for %s = %q, want %q (from PUT)",
					info.Key, info.ETag, putETags[info.Key])
			}
		}

		// And confirm a HEAD on each key returns the same etag
		// the LIST surfaced — closes the wa-i1l.18 row
		// "list_returns_etags".
		for _, info := range listed {
			head, err := client.HeadObject(ctx, info.Key)
			if err != nil {
				t.Fatalf("HEAD %s: %v", info.Key, err)
			}
			if head.ETag != info.ETag {
				t.Errorf("HEAD etag %q != LIST etag %q for %s",
					head.ETag, info.ETag, info.Key)
			}
		}
	})

	t.Run("put_small_single_part", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "small_100k.bin"
		body := bytes.Repeat([]byte{0xAB}, 100*1024) // 100 KiB

		etag, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream")
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		track(key)
		if etag == "" {
			t.Errorf("etag empty")
		}
		info, err := client.HeadObject(ctx, key)
		if err != nil {
			t.Fatalf("HeadObject: %v", err)
		}
		if info.Size != int64(len(body)) {
			t.Errorf("size = %d, want %d", info.Size, len(body))
		}
	})

	t.Run("put_large_multipart", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// minio-go's default multipart threshold is ~64 MiB; 10 MiB
		// stays single-part. We pick 10 MiB anyway (the bead's
		// table calls for 10 MB) — the size MUST round-trip
		// either way, which is what we verify. Tests for the
		// >64 MiB multipart path live in production where MP3s
		// hit that threshold.
		const sz = 10 * 1024 * 1024
		body := make([]byte, sz)
		// Fill with a deterministic pattern so a corruption-mid-
		// flight bug surfaces as a byte mismatch rather than zero
		// bytes (which would also match an empty body).
		for i := range body {
			body[i] = byte(i)
		}
		key := prefix + "large_10m.bin"
		etag, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream")
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		track(key)
		if etag == "" {
			t.Errorf("etag empty")
		}

		// Verify by GET + comparing first/last KiB rather than
		// full body — saves test memory but still catches a
		// truncation or reorder regression.
		rc, err := client.GetObject(ctx, key)
		if err != nil {
			t.Fatalf("GetObject: %v", err)
		}
		defer rc.Close()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if int64(len(got)) != sz {
			t.Errorf("got %d bytes, want %d", len(got), sz)
		}
		if !bytes.Equal(got[:1024], body[:1024]) {
			t.Errorf("first 1 KiB differs")
		}
		if !bytes.Equal(got[len(got)-1024:], body[len(body)-1024:]) {
			t.Errorf("last 1 KiB differs")
		}
	})

	t.Run("content_type_audio_mpeg", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		key := prefix + "ct_audio.mp3"
		body := []byte{0xFF, 0xFB, 0x90, 0x00} // MPEG audio header

		if _, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "audio/mpeg"); err != nil {
			t.Fatalf("PutObject: %v", err)
		}
		track(key)

		info, err := client.HeadObject(ctx, key)
		if err != nil {
			t.Fatalf("HeadObject: %v", err)
		}
		if info.ContentType != "audio/mpeg" {
			t.Errorf("ContentType = %q, want audio/mpeg", info.ContentType)
		}
	})

	t.Run("concurrent_put", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		const goroutines = 5
		const perGoroutine = 5
		var wg sync.WaitGroup
		var fails atomic.Int32

		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < perGoroutine; i++ {
					key := fmt.Sprintf("%sconcurrent/g%d-i%d.txt", prefix, g, i)
					body := []byte(fmt.Sprintf("g=%d i=%d", g, i))
					if _, err := client.PutObject(ctx, key, bytes.NewReader(body), int64(len(body)), "text/plain"); err != nil {
						fails.Add(1)
						t.Errorf("g=%d i=%d: %v", g, i, err)
						return
					}
					track(key)
				}
			}(g)
		}
		wg.Wait()
		if got := fails.Load(); got > 0 {
			t.Errorf("%d concurrent PutObject calls failed", got)
		}
	})

	// Implicit coverage: every subtest above used Region="auto" via
	// the New() constructor. If the region setting were wrong, the
	// first PUT would fail with a 400 RegionMismatch and the whole
	// suite would abort. No standalone test row needed.
}

// --- helpers ---

// makeRunID returns a 12-char hex token unique to each test
// invocation. Used as the integration-test prefix segment so
// concurrent CI / local runs don't collide on object keys.
func makeRunID(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("makeRunID: %v", err)
	}
	return hex.EncodeToString(buf)
}

// randHex returns a hex string of the given byte length. Used for
// uniqueness markers on missing-key probes.
func randHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("randHex: %v", err)
	}
	return hex.EncodeToString(buf)
}

// listKeyList renders just the keys of an ObjectInfo slice for
// failure messages — without the etags / sizes, so a 100-key list
// is still readable.
func listKeyList(info []ObjectInfo) []string {
	keys := make([]string, len(info))
	for i, x := range info {
		keys[i] = x.Key
	}
	return keys
}
