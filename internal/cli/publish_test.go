package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jacob2017/wiki-audio/internal/cache"
	"github.com/Jacob2017/wiki-audio/internal/manifest"
	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// Tests for wa-i1l.7's runPublishCore. r2.Fake (pane-9, wa-i1l.17)
// stands in for the live store; no real R2, no real ElevenLabs (the
// publish path doesn't touch EL). Real feed.Generate runs — small
// XML doc, fast.

// publishFixture wires the cache, manifest, and Fake state for a
// publish test. Returns the Fake (so the test can call Operations()
// for call-order assertions and assert directly on stored objects)
// plus the cfg that runPublishCore needs.
type publishFixture struct {
	fake   *r2.Fake
	cfg    *model.Config
	cache  string // XDG_CACHE_HOME root
	tokens string
}

func setupPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)

	if err := cache.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	return &publishFixture{
		fake: r2.NewFake(),
		cfg: &model.Config{
			R2: model.R2Config{
				AccountID: "test-account",
				Bucket:    "wiki-audio",
			},
			Feed: model.FeedConfig{
				Title:       "PG Essays",
				Description: "Paul Graham essays read aloud.",
				Author:      "Paul Graham",
				OwnerEmail:  "test@example.com",
				BaseURL:     "https://wiki-audio.example.workers.dev",
				FeedPath:    "pg.xml",
				Language:    "en-us",
			},
		},
		cache:  xdg,
		tokens: "test-token-43chars-aaaaaaaaaaaaaaaaaaaaaaa",
	}
}

// addCacheMP3 writes a stub MP3 body to cache.OutPath(slug) so
// uploadOneEpisode can read it.
func (f *publishFixture) addCacheMP3(t *testing.T, slug string, body []byte) {
	t.Helper()
	path := cache.OutPath(slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// putManifestOnFake serializes m as JSON and writes it to the fake
// at manifest.PrimaryKey so manifest.Load picks it up.
func (f *publishFixture) putManifestOnFake(t *testing.T, m *model.Manifest) {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.fake.PutObject(context.Background(),
		manifest.PrimaryKey, bytes.NewReader(body), int64(len(body)), "application/json"); err != nil {
		t.Fatal(err)
	}
}

// frozenTime returns a deterministic clock for runPublishCore so
// PublishedAt is reproducible across runs.
func frozenTime() func() time.Time {
	return func() time.Time {
		return time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	}
}

// --- happy path ---

func TestPublish_EmptyBucketUploadsAll(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha", BodyHash: "h1"},
			"beta":  {Slug: "beta", Title: "Beta", BodyHash: "h2"},
			"gamma": {Slug: "gamma", Title: "Gamma", BodyHash: "h3"},
		},
	}
	f.putManifestOnFake(t, mft)
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		f.addCacheMP3(t, slug, []byte("mp3-bytes-"+slug))
	}

	var out bytes.Buffer
	err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime())
	if err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}

	// All 3 mp3 keys present.
	for _, slug := range []string{"alpha", "beta", "gamma"} {
		key := "pg/" + slug + ".mp3"
		if _, err := f.fake.HeadObject(context.Background(), key); err != nil {
			t.Errorf("expected %s on fake: %v", key, err)
		}
	}

	// pg.xml + manifest both written.
	if _, err := f.fake.HeadObject(context.Background(), "pg.xml"); err != nil {
		t.Errorf("pg.xml not on fake: %v", err)
	}
	if _, err := f.fake.HeadObject(context.Background(), manifest.PrimaryKey); err != nil {
		t.Errorf("manifest not on fake: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "diff: 3 new, 0 changed, 0 stale-on-r2") {
		t.Errorf("missing diff summary; got: %s", got)
	}
	if !strings.Contains(got, "feed live at https://wiki-audio.example.workers.dev/pg.xml?t=") {
		t.Errorf("missing feed URL line; got: %s", got)
	}
	if !strings.Contains(got, "regenerating pg.xml (3 items)") {
		t.Errorf("missing feed regen line; got: %s", got)
	}
}

// existing_etag_match_skips_upload — pre-populate Fake with matching
// content (same etag the Fake will assign on rewrite); manifest
// entry's R2ETag matches; Diff reports Unchanged; uploadOneEpisode
// is NOT called for that slug.
func TestPublish_ExistingEtagMatchSkipsUpload(t *testing.T) {
	f := setupPublishFixture(t)
	body := []byte("identical content")

	// Pre-populate the fake at the canonical key. Capture the etag
	// the Fake assigns; that's what HEAD will return.
	preEtag, err := f.fake.PutObject(context.Background(),
		"pg/alpha.mp3", bytes.NewReader(body), int64(len(body)), "audio/mpeg")
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {
				Slug: "alpha", Title: "Alpha", BodyHash: "h1",
				R2Key:       "pg/alpha.mp3",
				R2ETag:      preEtag,
				PublishedAt: &pub,
			},
		},
	}
	f.putManifestOnFake(t, mft)
	// Don't add a cache MP3 — if upload accidentally fired, the
	// missing source file would surface as a read error.

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}

	// Verify no PutObject call landed on the alpha key beyond the
	// pre-populate. Fake records each Put as an Operation.
	puts := 0
	for _, op := range f.fake.Operations() {
		if op.Name == "PutObject" && op.Key == "pg/alpha.mp3" {
			puts++
		}
	}
	if puts != 1 {
		t.Errorf("PutObject(pg/alpha.mp3) calls = %d, want 1 (only the pre-populate)", puts)
	}

	got := out.String()
	if !strings.Contains(got, "diff: 0 new, 0 changed, 0 stale-on-r2") {
		t.Errorf("expected unchanged diff; got: %s", got)
	}
}

// changed_etag_uploads — the manifest entry's R2ETag differs from
// what's on R2 (e.g., a manual mc cp). uploadOneEpisode runs and
// updates the manifest's etag to match the new upload.
func TestPublish_ChangedEtagUploadsOverwrite(t *testing.T) {
	f := setupPublishFixture(t)
	// Pre-populate with body A — the fake assigns etag(A).
	bodyOnR2 := []byte("body that's currently on R2")
	if _, err := f.fake.PutObject(context.Background(),
		"pg/alpha.mp3", bytes.NewReader(bodyOnR2), int64(len(bodyOnR2)), "audio/mpeg"); err != nil {
		t.Fatal(err)
	}
	// Manifest claims a DIFFERENT etag — mismatch triggers
	// ToOverwrite.
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {
				Slug:   "alpha",
				Title:  "Alpha",
				R2Key:  "pg/alpha.mp3",
				R2ETag: "stale-etag-from-an-old-publish",
			},
		},
	}
	f.putManifestOnFake(t, mft)

	cacheBody := []byte("the new body the operator just built")
	f.addCacheMP3(t, "alpha", cacheBody)

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}

	if !strings.Contains(out.String(), "diff: 0 new, 1 changed, 0 stale-on-r2") {
		t.Errorf("expected changed diff; got: %s", out.String())
	}

	// The fake now has the new body.
	rc, err := f.fake.GetObject(context.Background(), "pg/alpha.mp3")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, cacheBody) {
		t.Errorf("R2 body after overwrite = %q, want %q", got, cacheBody)
	}
}

// upload-order: MP3s → manifest → feed. Pin via the fake's
// Operations log.
func TestPublish_UploadOrderMP3sThenManifestThenFeed(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
			"beta":  {Slug: "beta", Title: "Beta"},
		},
	}
	f.putManifestOnFake(t, mft)
	f.addCacheMP3(t, "alpha", []byte("alpha-body"))
	f.addCacheMP3(t, "beta", []byte("beta-body"))

	if err := runPublishCore(context.Background(), io.Discard, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}

	// Walk Operations and find the index of: each mp3 PUT, the
	// manifest PUT, the feed PUT. Manifest must come AFTER both
	// MP3s; feed must come AFTER manifest.
	mp3End, manifestIdx, feedIdx := -1, -1, -1
	for i, op := range f.fake.Operations() {
		if op.Name != "PutObject" {
			continue
		}
		switch {
		case strings.HasPrefix(op.Key, "pg/") && strings.HasSuffix(op.Key, ".mp3"):
			if i > mp3End {
				mp3End = i
			}
		case op.Key == manifest.PrimaryKey:
			manifestIdx = i
		case op.Key == "pg.xml":
			feedIdx = i
		}
	}
	if mp3End == -1 || manifestIdx == -1 || feedIdx == -1 {
		t.Fatalf("missing ops: mp3End=%d manifest=%d feed=%d in %#v",
			mp3End, manifestIdx, feedIdx, f.fake.Operations())
	}
	if !(mp3End < manifestIdx && manifestIdx < feedIdx) {
		t.Errorf("upload order violated: mp3End=%d manifestIdx=%d feedIdx=%d (want mp3 < manifest < feed)",
			mp3End, manifestIdx, feedIdx)
	}
}

// partial_failure_leaves_consistent_state — a mid-batch upload
// failure means feed is NOT regenerated. The OLD pg.xml in R2 (if
// any) survives untouched, and the manifest's R2 representation is
// the OLD one (Save runs only on full success).
func TestPublish_PartialFailureLeavesOldFeedAndManifest(t *testing.T) {
	// Wrap r2.Fake with a failing-second-upload Storage. After 1
	// successful PUT under pg/, the wrapper returns ErrThrottled
	// for all subsequent PUTs to that prefix. PutWithRetries will
	// retry up to MaxAttempts and then surface the error, halting
	// runPublishCore before manifest.Save / feed PUT.
	f := setupPublishFixture(t)

	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
			"beta":  {Slug: "beta", Title: "Beta"},
		},
	}
	f.putManifestOnFake(t, mft)
	f.addCacheMP3(t, "alpha", []byte("alpha-body"))
	f.addCacheMP3(t, "beta", []byte("beta-body"))

	failing := &failAfterN{Storage: f.fake, allowedPuts: 1, prefix: "pg/"}

	err := runPublishCore(context.Background(), io.Discard, failing, f.cfg, f.tokens, modeDefault, false, frozenTime())
	if err == nil {
		t.Fatal("expected error from partial-failure")
	}

	// Feed must NOT be on the fake.
	if _, err := f.fake.HeadObject(context.Background(), "pg.xml"); !errors.Is(err, r2.ErrNoSuchKey) {
		t.Errorf("pg.xml unexpectedly present on fake; want ErrNoSuchKey, got: %v", err)
	}

	// Manifest on the fake must still be the ORIGINAL (no
	// patched R2Key/R2ETag values for any entry).
	rc, err := f.fake.GetObject(context.Background(), manifest.PrimaryKey)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	var stored model.Manifest
	if err := json.Unmarshal(got, &stored); err != nil {
		t.Fatal(err)
	}
	for slug, e := range stored.Entries {
		if e.R2Key != "" {
			t.Errorf("manifest R2.alpha.R2Key = %q; want empty (Save shouldn't have run on partial failure) for slug=%s",
				e.R2Key, slug)
		}
	}
}

// --- pre-flight ---

func TestPublish_TokenRequiredPreUpload(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
		},
	}
	f.putManifestOnFake(t, mft)
	f.addCacheMP3(t, "alpha", []byte("body"))

	// runPublishCore takes the token as a param, so pre-flight on
	// empty-token lives in runPublish, not runPublishCore. We
	// can't drive runPublish here without a full cobra Command
	// fixture, so verify the sentinel exists + has the right
	// shape — runPublish_test would be the integration path for
	// the real flag-driven flow.
	if errPublishNoToken == nil {
		t.Fatal("errPublishNoToken sentinel missing")
	}
	if !strings.Contains(errPublishNoToken.Error(), "WIKI_AUDIO_ACCESS_TOKEN") {
		t.Errorf("error should name the env var; got: %v", errPublishNoToken)
	}
	if !strings.Contains(errPublishNoToken.Error(), "doctor") {
		t.Errorf("error should hint wiki-audio doctor; got: %v", errPublishNoToken)
	}
}

// --- overlayLocalFeedFields (wa-bo5) ---

// Surgical fix in publish.go: the live feed regenerates from R2's
// manifest, which can predate the wa-bo5 schema. The overlay folds
// per-essay SourceURL + Description from the local manifest into
// the in-memory mft just before buildFeed. Pin the contract so a
// future "we restructured publish" PR doesn't drop the overlay
// silently and quietly ship a feed without per-item link/description.
func TestOverlayLocalFeedFields_OverlaysOnlyEmptyFields(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("HOME", cacheDir) // cache.Dir() may resolve under $HOME

	if err := os.MkdirAll(cache.Dir(), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	local := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", SourceURL: "http://example.test/a", Description: "Alpha summary."},
			"beta":  {Slug: "beta", SourceURL: "http://example.test/b", Description: "Beta summary."},
		},
	}
	body, _ := json.Marshal(local)
	if err := os.WriteFile(filepath.Join(cache.Dir(), "manifest.json"), body, 0o644); err != nil {
		t.Fatalf("write local manifest: %v", err)
	}

	mft := &model.Manifest{
		Entries: map[string]model.ManifestEntry{
			// alpha: R2 has no overlay fields → both populated from local.
			"alpha": {Slug: "alpha"},
			// beta: R2 already has its own description → that wins; only
			// SourceURL gets overlaid.
			"beta": {Slug: "beta", Description: "Pre-existing R2 desc."},
			// gamma: not in local manifest → unchanged.
			"gamma": {Slug: "gamma"},
		},
	}

	overlayLocalFeedFields(mft, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if got := mft.Entries["alpha"].SourceURL; got != "http://example.test/a" {
		t.Errorf("alpha SourceURL: got %q", got)
	}
	if got := mft.Entries["alpha"].Description; got != "Alpha summary." {
		t.Errorf("alpha Description: got %q", got)
	}
	if got := mft.Entries["beta"].SourceURL; got != "http://example.test/b" {
		t.Errorf("beta SourceURL: got %q", got)
	}
	if got := mft.Entries["beta"].Description; got != "Pre-existing R2 desc." {
		t.Errorf("beta Description should be preserved when R2 already has one; got %q", got)
	}
	if got := mft.Entries["gamma"].SourceURL; got != "" {
		t.Errorf("gamma should have no SourceURL (not in local manifest); got %q", got)
	}
}

// Missing local manifest is best-effort: overlay logs and returns
// without modifying mft. Pin so a future "reject if local missing"
// refactor surfaces.
func TestOverlayLocalFeedFields_MissingLocalManifestIsBestEffort(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("HOME", cacheDir)

	mft := &model.Manifest{
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha"},
		},
	}
	overlayLocalFeedFields(mft, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if mft.Entries["alpha"].SourceURL != "" {
		t.Errorf("expected no overlay when local manifest missing; got %q", mft.Entries["alpha"].SourceURL)
	}
}

// --- feed URL composition ---

func TestBuildFeedURL(t *testing.T) {
	cases := []struct {
		base, token, want string
	}{
		{
			"https://w.example.com",
			"token-abc",
			"https://w.example.com/pg.xml?t=token-abc",
		},
		{
			"https://w.example.com/", // trailing slash tolerated
			"token-abc",
			"https://w.example.com/pg.xml?t=token-abc",
		},
		{
			"https://w.example.com",
			"weird+/=token", // url-escaped
			"https://w.example.com/pg.xml?t=weird%2B%2F%3Dtoken",
		},
	}
	for _, c := range cases {
		got, err := buildFeedURL(c.base, c.token)
		if err != nil {
			t.Errorf("buildFeedURL(%q): %v", c.base, err)
			continue
		}
		if got != c.want {
			t.Errorf("buildFeedURL(%q, %q) = %q; want %q", c.base, c.token, got, c.want)
		}
	}
}

// --- summary line semantics ---

func TestPublish_DiffCountLogged(t *testing.T) {
	f := setupPublishFixture(t)

	// One unchanged (pre-populated, etag matches), one new, one
	// changed (etag mismatch).
	preEtag, _ := f.fake.PutObject(context.Background(),
		"pg/unchanged.mp3", bytes.NewReader([]byte("u")), 1, "audio/mpeg")
	staleEtag, _ := f.fake.PutObject(context.Background(),
		"pg/changed.mp3", bytes.NewReader([]byte("c-old")), 5, "audio/mpeg")
	_ = staleEtag

	pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"unchanged": {Slug: "unchanged", R2Key: "pg/unchanged.mp3", R2ETag: preEtag, PublishedAt: &pub},
			"changed":   {Slug: "changed", R2Key: "pg/changed.mp3", R2ETag: "old-etag-doesnt-match"},
			"newone":    {Slug: "newone"},
		},
	}
	f.putManifestOnFake(t, mft)
	f.addCacheMP3(t, "changed", []byte("c-new"))
	f.addCacheMP3(t, "newone", []byte("n-body"))

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}

	if !strings.Contains(out.String(), "diff: 1 new, 1 changed, 0 stale-on-r2") {
		t.Errorf("expected mixed diff line; got: %s", out.String())
	}
}

// --- modeFeedOnly (wa-i1l.8) ---

// feed_only_skips_uploads: zero PutObject calls under pg/*.mp3
// even when the diff would otherwise upload many. Use case: token
// rotation regenerates the feed without re-uploading episodes.
func TestPublish_FeedOnlySkipsMP3Uploads(t *testing.T) {
	f := setupPublishFixture(t)
	pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha", R2Key: "pg/alpha.mp3", R2ETag: "e1", PublishedAt: &pub},
			"beta":  {Slug: "beta", Title: "Beta", R2Key: "pg/beta.mp3", R2ETag: "e2", PublishedAt: &pub},
		},
	}
	f.putManifestOnFake(t, mft)
	// Deliberately DON'T addCacheMP3 — feed-only must NOT read
	// from cache. If the orchestrator accidentally tries to upload
	// an MP3, the missing source file would surface as an error.

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeFeedOnly, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore feed-only: %v", err)
	}

	for _, op := range f.fake.Operations() {
		if op.Name != "PutObject" {
			continue
		}
		if strings.HasPrefix(op.Key, "pg/") && strings.HasSuffix(op.Key, ".mp3") {
			t.Errorf("feed-only mode performed mp3 PUT on %s; want zero", op.Key)
		}
	}

	got := out.String()
	if !strings.Contains(got, "feed-only mode: skipping diff + MP3 upload") {
		t.Errorf("missing feed-only banner; got: %s", got)
	}
	if !strings.Contains(got, "regenerating pg.xml") {
		t.Errorf("feed-only must still regenerate pg.xml; got: %s", got)
	}
	if !strings.Contains(got, "feed live at https://wiki-audio.example.workers.dev/pg.xml?t=") {
		t.Errorf("feed-only must print the feed URL; got: %s", got)
	}
}

// feed_only_uploads_pgxml: exactly one PutObject for pg.xml. The
// manifest is NOT re-saved — the manifest content is unchanged in
// the rotation scenario, and skipping its Save avoids the .bak
// rotation overhead for a no-op write.
func TestPublish_FeedOnlyUploadsPgxmlAndSkipsManifest(t *testing.T) {
	f := setupPublishFixture(t)
	pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha", R2Key: "pg/alpha.mp3", R2ETag: "e1", PublishedAt: &pub},
		},
	}
	f.putManifestOnFake(t, mft)

	if err := runPublishCore(context.Background(), io.Discard, f.fake, f.cfg, f.tokens, modeFeedOnly, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore feed-only: %v", err)
	}

	pgxmlPuts, manifestPuts := 0, 0
	for _, op := range f.fake.Operations() {
		if op.Name != "PutObject" {
			continue
		}
		switch op.Key {
		case "pg.xml":
			pgxmlPuts++
		case manifest.PrimaryKey:
			manifestPuts++
		}
	}
	if pgxmlPuts != 1 {
		t.Errorf("pg.xml PUTs = %d, want exactly 1", pgxmlPuts)
	}
	// The setup's putManifestOnFake counts as 1 manifest PUT
	// before runPublishCore ran. feed-only mode must not add to
	// that count (no Save).
	if manifestPuts != 1 {
		t.Errorf("manifest PUTs = %d, want exactly 1 (test setup only — feed-only must not Save)", manifestPuts)
	}
}

// --- modeDryRun (wa-i1l.9) ---

// dry_run_zero_writes: NO PUT/Delete calls beyond the test's
// pre-populate. Use case: pre-bulk-run sanity check.
func TestPublish_DryRunZeroWrites(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
			"beta":  {Slug: "beta", Title: "Beta"},
		},
	}
	f.putManifestOnFake(t, mft)
	// Don't addCacheMP3 — dry-run prints "size unknown" when the
	// cache is absent and that's fine; the test should still pass.

	// Snapshot Operations count BEFORE runPublishCore so the
	// pre-populate's PUT doesn't mask a misbehaving dry-run.
	preOps := len(f.fake.Operations())

	if err := runPublishCore(context.Background(), io.Discard, f.fake, f.cfg, f.tokens, modeDryRun, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore dry-run: %v", err)
	}

	postOps := f.fake.Operations()
	for _, op := range postOps[preOps:] {
		switch op.Name {
		case "PutObject", "DeleteObject":
			t.Errorf("dry-run performed %s on %s; want zero writes", op.Name, op.Key)
		}
	}
}

// dry_run_prints_diff: stdout includes "would upload" text + the
// final-state summary. Operator can read this and predict what
// the real publish run would do.
func TestPublish_DryRunPrintsDiffAndWouldUploadLines(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
			"beta":  {Slug: "beta", Title: "Beta"},
		},
	}
	f.putManifestOnFake(t, mft)
	// Add cache file for one slug so size formatting differs.
	f.addCacheMP3(t, "alpha", bytes.Repeat([]byte("X"), 4096))

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDryRun, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore dry-run: %v", err)
	}

	got := out.String()
	mustContain := []string{
		"diff: 2 new, 0 changed, 0 stale-on-r2",
		"would upload alpha.mp3",
		"would upload beta.mp3",
		"r2://wiki-audio/pg/alpha.mp3",
		"r2://wiki-audio/pg/beta.mp3",
		"would regenerate pg.xml",
		"dry-run: no writes performed",
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in dry-run output; got:\n%s", want, got)
		}
	}

	// Cache-present alpha shows a real size; cache-absent beta
	// shows "size unknown". Both flow through formatBytes /
	// dryRunBodySize without erroring.
	if !strings.Contains(got, "alpha.mp3 (4.0 KiB)") {
		t.Errorf("expected alpha to show cached size; got:\n%s", got)
	}
	if !strings.Contains(got, "beta.mp3 (size unknown)") {
		t.Errorf("expected beta to show size-unknown; got:\n%s", got)
	}
}

// --- --prune (wa-i1l.10) ---

// prune_disabled_by_default: 3 stale objects on R2 → zero
// DeleteObject calls when --prune is NOT set. The default
// behavior leaves stale objects untouched; the operator must opt
// in explicitly.
func TestPublish_PruneDisabledByDefault(t *testing.T) {
	f := setupPublishFixture(t)
	// 3 stale objects under pg/ that no manifest entry claims.
	for _, key := range []string{"pg/old-essay-1.mp3", "pg/old-essay-2.mp3", "pg/old-essay-3.mp3"} {
		if _, err := f.fake.PutObject(context.Background(),
			key, bytes.NewReader([]byte("stale")), 5, "audio/mpeg"); err != nil {
			t.Fatal(err)
		}
	}
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{},
	}
	f.putManifestOnFake(t, mft)

	preOps := len(f.fake.Operations())
	if err := runPublishCore(context.Background(), io.Discard, f.fake, f.cfg, f.tokens, modeDefault, false, frozenTime()); err != nil {
		t.Fatalf("runPublishCore: %v", err)
	}
	for _, op := range f.fake.Operations()[preOps:] {
		if op.Name == "DeleteObject" {
			t.Errorf("default mode performed DeleteObject(%s); want zero (--prune off)", op.Key)
		}
	}
	// And the stale objects survive on R2.
	for _, key := range []string{"pg/old-essay-1.mp3", "pg/old-essay-2.mp3", "pg/old-essay-3.mp3"} {
		if _, err := f.fake.HeadObject(context.Background(), key); err != nil {
			t.Errorf("stale %s should survive when --prune is off; got %v", key, err)
		}
	}
}

// prune_deletes_stale: --prune + 3 stale → 3 DeleteObject calls,
// stale list printed, summary line printed.
func TestPublish_PruneDeletesStale(t *testing.T) {
	f := setupPublishFixture(t)
	staleKeys := []string{"pg/old-essay-1.mp3", "pg/old-essay-2.mp3", "pg/old-essay-3.mp3"}
	for _, key := range staleKeys {
		if _, err := f.fake.PutObject(context.Background(),
			key, bytes.NewReader([]byte("stale")), 5, "audio/mpeg"); err != nil {
			t.Fatal(err)
		}
	}
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{},
	}
	f.putManifestOnFake(t, mft)

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, true, frozenTime()); err != nil {
		t.Fatalf("runPublishCore --prune: %v", err)
	}

	deletes := 0
	for _, op := range f.fake.Operations() {
		if op.Name == "DeleteObject" {
			deletes++
		}
	}
	if deletes != len(staleKeys) {
		t.Errorf("DeleteObject calls = %d, want %d", deletes, len(staleKeys))
	}

	// Each stale key was actually removed.
	for _, key := range staleKeys {
		if _, err := f.fake.HeadObject(context.Background(), key); !errors.Is(err, r2.ErrNoSuchKey) {
			t.Errorf("stale %s should be gone after --prune; HEAD err = %v", key, err)
		}
	}

	got := out.String()
	if !strings.Contains(got, "prune: deleting 3 stale R2 object(s):") {
		t.Errorf("missing prune banner; got:\n%s", got)
	}
	if !strings.Contains(got, "pruned 3 stale R2 object(s)") {
		t.Errorf("missing prune summary; got:\n%s", got)
	}
	for _, key := range staleKeys {
		if !strings.Contains(got, "pruned r2://wiki-audio/"+key) {
			t.Errorf("missing per-key prune line for %s; got:\n%s", key, got)
		}
	}
}

// prune_no_op_on_clean: --prune + zero stale → no DeleteObject
// calls + a clean status line. Surfacing "nothing to delete" is
// useful so operators know the flag was honored even when there
// was nothing to do.
func TestPublish_PruneNoOpOnClean(t *testing.T) {
	f := setupPublishFixture(t)
	mft := &model.Manifest{
		Version: model.ManifestSchemaVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha"},
		},
	}
	f.putManifestOnFake(t, mft)
	f.addCacheMP3(t, "alpha", []byte("alpha-body"))

	var out bytes.Buffer
	if err := runPublishCore(context.Background(), &out, f.fake, f.cfg, f.tokens, modeDefault, true, frozenTime()); err != nil {
		t.Fatalf("runPublishCore --prune (clean): %v", err)
	}

	for _, op := range f.fake.Operations() {
		if op.Name == "DeleteObject" {
			t.Errorf("clean prune should perform 0 DeleteObject; got delete on %s", op.Key)
		}
	}
	if !strings.Contains(out.String(), "prune: nothing to delete (0 stale objects on r2)") {
		t.Errorf("missing clean-prune status line; got:\n%s", out.String())
	}
}

// --- stub Storage that fails after N successful Puts ---

type failAfterN struct {
	r2.Storage
	allowedPuts int
	puts        int
	prefix      string // only count puts whose key matches this prefix
}

func (s *failAfterN) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if strings.HasPrefix(key, s.prefix) {
		s.puts++
		if s.puts > s.allowedPuts {
			return "", fmt.Errorf("%w: simulated mid-batch throttle", r2.ErrThrottled)
		}
	}
	return s.Storage.PutObject(ctx, key, r, size, contentType)
}
