package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// putBlob seeds the fake. Returns the R2-side ETag for tests that
// need to mirror it into manifest entries.
func putBlob(t *testing.T, store r2.Storage, key string, body []byte) string {
	t.Helper()
	etag, err := store.PutObject(context.Background(), key,
		bytes.NewReader(body), int64(len(body)), "audio/mpeg")
	if err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	return etag
}

func entry(slug, etag string) model.ManifestEntry {
	return model.ManifestEntry{
		Slug:   slug,
		Title:  strings.ToUpper(slug[:1]) + slug[1:],
		R2Key:  EpisodePrefix + slug + ".mp3",
		R2ETag: etag,
	}
}

func newManifest(entries ...model.ManifestEntry) *model.Manifest {
	m := &model.Manifest{Entries: make(map[string]model.ManifestEntry)}
	for _, e := range entries {
		m.Entries[e.Slug] = e
	}
	return m
}

// --- Single-state cases ---------------------------------------------------

func TestDiff_AllNew_EmptyBucket(t *testing.T) {
	store := r2.NewFake()
	m := newManifest(
		entry("alpha", "fake-etag-a"),
		entry("beta", "fake-etag-b"),
		entry("gamma", "fake-etag-c"),
	)

	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(plan.ToUpload) != 3 {
		t.Errorf("ToUpload len = %d; want 3", len(plan.ToUpload))
	}
	if len(plan.ToOverwrite) != 0 || len(plan.Stale) != 0 || len(plan.Unchanged) != 0 {
		t.Errorf("expected only ToUpload populated; got %+v", plan)
	}
	// Sorted by slug (alpha, beta, gamma).
	wantOrder := []string{"alpha", "beta", "gamma"}
	for i, e := range plan.ToUpload {
		if e.Slug != wantOrder[i] {
			t.Errorf("ToUpload[%d].Slug = %q; want %q", i, e.Slug, wantOrder[i])
		}
	}
}

func TestDiff_AllMatched_NoUploadNeeded(t *testing.T) {
	store := r2.NewFake()
	etagA := putBlob(t, store, "pg/alpha.mp3", []byte("alpha-bytes"))
	etagB := putBlob(t, store, "pg/beta.mp3", []byte("beta-bytes"))
	m := newManifest(entry("alpha", etagA), entry("beta", etagB))

	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(plan.ToUpload) != 0 || len(plan.ToOverwrite) != 0 || len(plan.Stale) != 0 {
		t.Errorf("expected empty diff for matched ETags; got %+v", plan)
	}
	if len(plan.Unchanged) != 2 {
		t.Errorf("Unchanged len = %d; want 2", len(plan.Unchanged))
	}
}

func TestDiff_OneETagMismatch(t *testing.T) {
	store := r2.NewFake()
	etagA := putBlob(t, store, "pg/alpha.mp3", []byte("alpha-bytes"))
	_ = putBlob(t, store, "pg/beta.mp3", []byte("real-beta-bytes"))
	m := newManifest(
		entry("alpha", etagA),
		entry("beta", "stale-etag-from-prior-publish"),
	)

	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(plan.ToOverwrite) != 1 || plan.ToOverwrite[0].Slug != "beta" {
		t.Errorf("expected beta in ToOverwrite; got %+v", plan.ToOverwrite)
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0].Slug != "alpha" {
		t.Errorf("expected alpha unchanged; got %+v", plan.Unchanged)
	}
	if len(plan.ToUpload) != 0 || len(plan.Stale) != 0 {
		t.Errorf("ToUpload/Stale should be empty; got %+v", plan)
	}
}

func TestDiff_StaleObjectInBucket(t *testing.T) {
	store := r2.NewFake()
	etagA := putBlob(t, store, "pg/alpha.mp3", []byte("alpha-bytes"))
	_ = putBlob(t, store, "pg/orphan.mp3", []byte("nobody-claims-this"))
	m := newManifest(entry("alpha", etagA))

	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(plan.Stale) != 1 || plan.Stale[0].Key != "pg/orphan.mp3" {
		t.Errorf("expected pg/orphan.mp3 in Stale; got %+v", plan.Stale)
	}
	if len(plan.Unchanged) != 1 {
		t.Errorf("alpha should be Unchanged; got %+v", plan.Unchanged)
	}
}

func TestDiff_NewPlusChangedPlusStale(t *testing.T) {
	store := r2.NewFake()
	etagAlpha := putBlob(t, store, "pg/alpha.mp3", []byte("alpha-bytes"))
	_ = putBlob(t, store, "pg/beta.mp3", []byte("real-beta-bytes")) // ETag will mismatch
	_ = putBlob(t, store, "pg/orphan.mp3", []byte("orphan"))         // not in manifest
	m := newManifest(
		entry("alpha", etagAlpha),                 // matches → Unchanged
		entry("beta", "manifest-thinks-its-this"), // mismatch → ToOverwrite
		entry("gamma", "fake-etag"),               // missing in R2 → ToUpload
	)

	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(plan.ToUpload) != 1 || plan.ToUpload[0].Slug != "gamma" {
		t.Errorf("ToUpload: want [gamma]; got %v", slugList(plan.ToUpload))
	}
	if len(plan.ToOverwrite) != 1 || plan.ToOverwrite[0].Slug != "beta" {
		t.Errorf("ToOverwrite: want [beta]; got %v", slugList(plan.ToOverwrite))
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0].Slug != "alpha" {
		t.Errorf("Unchanged: want [alpha]; got %v", slugList(plan.Unchanged))
	}
	if len(plan.Stale) != 1 || plan.Stale[0].Key != "pg/orphan.mp3" {
		t.Errorf("Stale: want [pg/orphan.mp3]; got %v", staleKeys(plan.Stale))
	}
}

// --- §3 publish-output format pin ----------------------------------------

func TestPlan_StringMatchesSection3Format(t *testing.T) {
	p := &Plan{
		ToUpload:    []model.ManifestEntry{{Slug: "a"}, {Slug: "b"}, {Slug: "c"}, {Slug: "d"}},
		ToOverwrite: []model.ManifestEntry{{Slug: "e"}},
		Stale:       nil,
	}
	want := "diff: 4 new, 1 changed, 0 stale-on-r2"
	if got := p.String(); got != want {
		t.Errorf("Plan.String() = %q; want %q", got, want)
	}
}

func TestPlan_StringEmpty(t *testing.T) {
	p := &Plan{}
	want := "diff: 0 new, 0 changed, 0 stale-on-r2"
	if got := p.String(); got != want {
		t.Errorf("Plan.String() empty = %q; want %q", got, want)
	}
}

// --- Edge cases ----------------------------------------------------------

func TestDiff_EmptyBucketEmptyManifest(t *testing.T) {
	store := r2.NewFake()
	plan, err := Diff(context.Background(), store, &model.Manifest{
		Entries: make(map[string]model.ManifestEntry),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToUpload)+len(plan.ToOverwrite)+len(plan.Stale)+len(plan.Unchanged) != 0 {
		t.Errorf("expected fully-empty plan; got %+v", plan)
	}
}

func TestDiff_NilManifestErrors(t *testing.T) {
	_, err := Diff(context.Background(), r2.NewFake(), nil)
	if err == nil {
		t.Fatal("nil manifest must error")
	}
	if !strings.Contains(err.Error(), "nil manifest") {
		t.Errorf("error should mention nil manifest; got %q", err.Error())
	}
}

func TestDiff_RespectsExplicitR2Key(t *testing.T) {
	// Manifest entry with R2Key overriding the EpisodePrefix+slug
	// convention — the override wins.
	store := r2.NewFake()
	etag := putBlob(t, store, "custom-prefix/custom-key.mp3", []byte("body"))
	e := entry("alpha", etag)
	e.R2Key = "custom-prefix/custom-key.mp3"
	m := newManifest(e)

	// Even though the slug-derived path is "pg/alpha.mp3", which does
	// not exist on the fake, the explicit R2Key points at a real
	// object whose ETag matches — so this is Unchanged, not ToUpload.
	plan, err := Diff(context.Background(), store, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToUpload) != 0 {
		t.Errorf("explicit R2Key with matching object should not be ToUpload; got %v", slugList(plan.ToUpload))
	}
	// "pg/alpha.mp3" doesn't exist in the bucket and the manifest
	// doesn't claim it, so it doesn't show up anywhere. The custom
	// key is matched, hence Unchanged.
	if len(plan.Unchanged) != 1 {
		t.Errorf("Unchanged len = %d; want 1", len(plan.Unchanged))
	}
	// The custom key was visited via R2Key, so it should NOT be
	// reported as Stale even though it's outside EpisodePrefix —
	// but ListObjects(EpisodePrefix) wouldn't have returned it
	// either. Both effects align: no spurious Stale entry.
	if len(plan.Stale) != 0 {
		t.Errorf("Stale should be empty when explicit R2Key matches; got %v", staleKeys(plan.Stale))
	}
}

// --- Error propagation: §6 "R2 listing failure during diff — abort" ------

// errStorage is a minimal r2.Storage that returns configurable errors
// for ListObjects / HeadObject. Exists so the §6 abort-on-list rows
// can be exercised without making the in-memory Fake error-injecting.
type errStorage struct {
	listErr error
	headErr error
}

func (s *errStorage) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	return "", errors.New("errStorage: PutObject must not be called by Diff (read-only)")
}
func (s *errStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return nil, errors.New("errStorage: GetObject must not be called by Diff")
}
func (s *errStorage) HeadObject(ctx context.Context, key string) (r2.ObjectInfo, error) {
	if s.headErr != nil {
		return r2.ObjectInfo{}, s.headErr
	}
	return r2.ObjectInfo{}, r2.ErrNoSuchKey
}
func (s *errStorage) DeleteObject(ctx context.Context, key string) error {
	return errors.New("errStorage: DeleteObject must not be called by Diff")
}
func (s *errStorage) ListObjects(ctx context.Context, prefix string) ([]r2.ObjectInfo, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}

var _ r2.Storage = (*errStorage)(nil)

func TestDiff_ListObjectsErrorAborts(t *testing.T) {
	want := errors.New("503 service unavailable")
	store := &errStorage{listErr: want}
	m := newManifest(entry("alpha", "etag"))

	_, err := Diff(context.Background(), store, m)
	if err == nil {
		t.Fatal("expected error from ListObjects failure")
	}
	if !errors.Is(err, want) {
		t.Errorf("err should wrap %v; got %v", want, err)
	}
}

func TestDiff_HeadObjectErrorAborts(t *testing.T) {
	want := errors.New("503 service unavailable")
	store := &errStorage{headErr: want}
	m := newManifest(entry("alpha", "etag"))

	_, err := Diff(context.Background(), store, m)
	if err == nil {
		t.Fatal("expected error from HeadObject failure")
	}
	if !errors.Is(err, want) {
		t.Errorf("err should wrap %v; got %v", want, err)
	}
}

// TestDiff_NoMutatingCallsOnFake — defensive check that Diff is
// purely read-only. After Diff(), the Fake's operation log should
// contain only ListObjects + HeadObject calls; no PutObject /
// DeleteObject. This pins the §6 "safe to retry after listing
// failure" property at the call-site level.
func TestDiff_NoMutatingCallsOnFake(t *testing.T) {
	store := r2.NewFake()
	etagA := putBlob(t, store, "pg/alpha.mp3", []byte("alpha-bytes"))
	m := newManifest(entry("alpha", etagA))

	preOps := len(store.Operations())
	if _, err := Diff(context.Background(), store, m); err != nil {
		t.Fatal(err)
	}
	postOps := store.Operations()[preOps:]
	for _, op := range postOps {
		switch op.Name {
		case "ListObjects", "HeadObject":
			// expected
		default:
			t.Errorf("Diff issued mutating op %s(%s); want only List/Head", op.Name, op.Key)
		}
	}
}

// --- helpers --------------------------------------------------------------

func slugList(es []model.ManifestEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Slug
	}
	return out
}

func staleKeys(os []r2.ObjectInfo) []string {
	out := make([]string, len(os))
	for i, o := range os {
		out[i] = o.Key
	}
	return out
}

// silence unused-import nag for fmt when iterating during dev.
var _ = fmt.Sprintf
