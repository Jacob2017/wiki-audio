package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// putBlob is a small helper for seeding the fake.
func putBlob(t *testing.T, store r2.Storage, key string, body []byte, contentType string) {
	t.Helper()
	if _, err := store.PutObject(context.Background(), key,
		bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func mustEncode(t *testing.T, m *model.Manifest) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encode(m, &buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// --- Load -----------------------------------------------------------------

func TestLoad_MissingReturnsEmptyManifest(t *testing.T) {
	store := r2.NewFake()
	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load on empty bucket: %v", err)
	}
	if got.Version != KnownManifestVersion {
		t.Errorf("empty-bucket Version = %d; want %d", got.Version, KnownManifestVersion)
	}
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Errorf("empty-bucket Entries = %v; want non-nil empty map", got.Entries)
	}
}

func TestLoad_PrimaryPresentAndValid(t *testing.T) {
	store := r2.NewFake()
	want := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{
			"alpha": {Slug: "alpha", Title: "Alpha", BodyHash: "deadbeef"},
		},
	}
	putBlob(t, store, PrimaryKey, mustEncode(t, want), "application/json")

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Entries["alpha"].Title != "Alpha" || got.Entries["alpha"].BodyHash != "deadbeef" {
		t.Errorf("Load returned wrong entry: %+v", got.Entries["alpha"])
	}
}

func TestLoad_CorruptPrimaryFallsBackToBak(t *testing.T) {
	store := r2.NewFake()
	want := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{
			"backup-essay": {Slug: "backup-essay", Title: "Recovered"},
		},
	}
	putBlob(t, store, PrimaryKey, []byte(`{"version": 1, "entries":`), "application/json") // truncated
	putBlob(t, store, BackupKey, mustEncode(t, want), "application/json")

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(old)

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load with corrupt primary should fall back to .bak; got err: %v", err)
	}
	if got.Entries["backup-essay"].Title != "Recovered" {
		t.Errorf(".bak fallback returned wrong content: %+v", got.Entries)
	}
	for _, want := range []string{"level=WARN", PrimaryKey, BackupKey} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("fallback warning missing %q in logs:\n%s", want, logs.String())
		}
	}
}

func TestLoad_BothCorruptAborts(t *testing.T) {
	store := r2.NewFake()
	putBlob(t, store, PrimaryKey, []byte(`{"version":`), "application/json")
	putBlob(t, store, BackupKey, []byte(`also-not-json`), "application/json")

	_, err := Load(context.Background(), store)
	if err == nil {
		t.Fatal("expected error when both primary and backup are corrupt")
	}
	for _, want := range []string{
		"both " + PrimaryKey + " and " + BackupKey + " are corrupt",
		"refusing to overwrite",
		"mc cat r2/wiki-audio/" + PrimaryKey,
		"mc cat r2/wiki-audio/" + BackupKey,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q; got %q", want, err.Error())
		}
	}
}

func TestLoad_CorruptPrimary_MissingBak_Aborts(t *testing.T) {
	store := r2.NewFake()
	putBlob(t, store, PrimaryKey, []byte(`{"oops":`), "application/json")

	_, err := Load(context.Background(), store)
	if err == nil {
		t.Fatal("expected abort when primary is corrupt and no backup exists")
	}
	if !strings.Contains(err.Error(), "manual inspection required") {
		t.Errorf("error should suggest manual inspection; got %q", err.Error())
	}
}

func TestLoad_MainValidBakCorrupt_ReturnsPrimary(t *testing.T) {
	store := r2.NewFake()
	primary := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"x": {Slug: "x", Title: "Primary Wins"}},
	}
	putBlob(t, store, PrimaryKey, mustEncode(t, primary), "application/json")
	putBlob(t, store, BackupKey, []byte(`{"broken":`), "application/json")

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load should ignore corrupt backup when primary is valid: %v", err)
	}
	if got.Entries["x"].Title != "Primary Wins" {
		t.Fatalf("loaded wrong manifest: %+v", got.Entries)
	}
}

// --- Save -----------------------------------------------------------------

func TestSave_NoPriorWritesPrimaryOnly(t *testing.T) {
	store := r2.NewFake()
	m := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"first": {Slug: "first", Title: "First"}},
	}

	if err := Save(context.Background(), store, m); err != nil {
		t.Fatalf("Save fresh bucket: %v", err)
	}

	if _, err := store.HeadObject(context.Background(), PrimaryKey); err != nil {
		t.Errorf("primary should exist after fresh save: %v", err)
	}
	if _, err := store.HeadObject(context.Background(), BackupKey); !errors.Is(err, r2.ErrNoSuchKey) {
		t.Errorf("fresh save should NOT create a backup; HeadObject returned %v", err)
	}
}

func TestSave_RotatesPriorToBak(t *testing.T) {
	store := r2.NewFake()
	prior := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Prior"}},
	}
	priorBytes := mustEncode(t, prior)
	putBlob(t, store, PrimaryKey, priorBytes, "application/json")

	next := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{
			"a": {Slug: "a", Title: "Updated"},
			"b": {Slug: "b", Title: "Brand New"},
		},
	}
	if err := Save(context.Background(), store, next); err != nil {
		t.Fatalf("Save with prior: %v", err)
	}

	// .bak should hold the *prior* content byte-for-byte.
	bakBody, err := store.GetObject(context.Background(), BackupKey)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	bakBytes, _ := io.ReadAll(bakBody)
	bakBody.Close()
	if !bytes.Equal(bakBytes, priorBytes) {
		t.Errorf(".bak content doesn't match prior primary;\n got %q\nwant %q", bakBytes, priorBytes)
	}

	// Primary should hold the *new* content.
	loaded, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("reload primary: %v", err)
	}
	if loaded.Entries["a"].Title != "Updated" || loaded.Entries["b"].Title != "Brand New" {
		t.Errorf("primary did not pick up new content: %+v", loaded.Entries)
	}
}

func TestSave_FutureVersionRefused(t *testing.T) {
	store := r2.NewFake()
	prior := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Prior"}},
	}
	putBlob(t, store, PrimaryKey, mustEncode(t, prior), "application/json")

	future := &model.Manifest{
		Version: KnownManifestVersion + 5,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Future"}},
	}
	err := Save(context.Background(), store, future)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("Save with future version should wrap ErrSchemaTooNew; got %v", err)
	}

	// Primary should be unchanged: the guard fires BEFORE Save touches R2.
	loaded, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("reload primary after refused save: %v", err)
	}
	if loaded.Entries["a"].Title != "Prior" {
		t.Errorf("primary mutated despite refused save: %+v", loaded.Entries)
	}

	// And .bak should never have been written.
	if _, err := store.HeadObject(context.Background(), BackupKey); !errors.Is(err, r2.ErrNoSuchKey) {
		t.Errorf("refused save must not touch .bak; HeadObject returned %v", err)
	}
}

func TestSave_OverwritesOldBak(t *testing.T) {
	store := r2.NewFake()
	putBlob(t, store, BackupKey, []byte(`{"stale":true}`), contentType)

	prior := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Prior"}},
	}
	priorBytes := mustEncode(t, prior)
	putBlob(t, store, PrimaryKey, priorBytes, contentType)

	next := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Next"}},
	}
	if err := Save(context.Background(), store, next); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rc, err := store.GetObject(context.Background(), BackupKey)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read backup body: %v", err)
	}
	if !bytes.Equal(got, priorBytes) {
		t.Fatalf("backup should be overwritten with prior primary bytes\n got: %s\nwant: %s", got, priorBytes)
	}
}

func TestSave_OlderVersionBumpsToCurrent(t *testing.T) {
	if KnownManifestVersion == 0 {
		t.Skip("no older version exists")
	}

	store := r2.NewFake()
	m := &model.Manifest{
		Version: KnownManifestVersion - 1,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Older"}},
	}
	if err := Save(context.Background(), store, m); err != nil {
		t.Fatalf("Save older version: %v", err)
	}

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load after save: %v", err)
	}
	if got.Version != KnownManifestVersion {
		t.Fatalf("Version after save/load = %d; want %d", got.Version, KnownManifestVersion)
	}
}

func TestSave_WritesAtomicPerObject(t *testing.T) {
	base := r2.NewFake()
	prior := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Prior"}},
	}
	priorBytes := mustEncode(t, prior)
	putBlob(t, base, PrimaryKey, priorBytes, contentType)

	next := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Next"}},
	}
	wantNextBytes := mustEncode(t, next)

	var midRead []byte
	var midReadErr error
	store := &observingStorage{
		base: base,
		onPrimaryPut: func() {
			rc, err := base.GetObject(context.Background(), PrimaryKey)
			if err != nil {
				midReadErr = err
				return
			}
			defer rc.Close()
			midRead, midReadErr = io.ReadAll(rc)
		},
	}

	if err := Save(context.Background(), store, next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if midReadErr != nil {
		t.Fatalf("mid-save read: %v", midReadErr)
	}
	if !bytes.Equal(midRead, priorBytes) {
		t.Fatalf("mid-save reader should see intact prior bytes, not a partial write\n got: %s\nwant: %s", midRead, priorBytes)
	}

	rc, err := base.GetObject(context.Background(), PrimaryKey)
	if err != nil {
		t.Fatalf("read final primary: %v", err)
	}
	defer rc.Close()
	finalBytes, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read final primary body: %v", err)
	}
	if !bytes.Equal(finalBytes, wantNextBytes) {
		t.Fatalf("final primary bytes mismatch\n got: %s\nwant: %s", finalBytes, wantNextBytes)
	}
}

// TestSave_RotateThenWriteOrdering pins the wa-i1l.2 step ordering:
// a Save with prior content → .bak is written BEFORE the new primary
// is uploaded, so a hypothetical step-4 failure would leave the .bak
// holding what was previously primary (the recovery path).
func TestSave_RotateThenWriteOrdering(t *testing.T) {
	store := r2.NewFake()
	prior := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Prior"}},
	}
	putBlob(t, store, PrimaryKey, mustEncode(t, prior), "application/json")

	next := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"a": {Slug: "a", Title: "Next"}},
	}
	if err := Save(context.Background(), store, next); err != nil {
		t.Fatal(err)
	}

	// Ops sequence (after seed): GetObject(PrimaryKey) → PutObject(BackupKey)
	// → PutObject(PrimaryKey). The seed PutObject is the first op; assert
	// the post-seed tail.
	ops := store.Operations()
	if len(ops) < 4 {
		t.Fatalf("expected ≥4 ops; got %d (%v)", len(ops), ops)
	}
	tail := ops[len(ops)-3:]
	wantNames := []string{"GetObject", "PutObject", "PutObject"}
	wantKeys := []string{PrimaryKey, BackupKey, PrimaryKey}
	for i, op := range tail {
		if op.Name != wantNames[i] || op.Key != wantKeys[i] {
			t.Errorf("op %d = %s(%s); want %s(%s)", i, op.Name, op.Key, wantNames[i], wantKeys[i])
		}
	}
}

// TestLoad_PrimaryAndBackupBothPresent_PrefersPrimary — sanity check
// that the backup path is the FALLBACK, not the default. With both
// present and both valid, primary wins.
func TestLoad_PrimaryAndBackupBothPresent_PrefersPrimary(t *testing.T) {
	store := r2.NewFake()
	primary := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"x": {Slug: "x", Title: "Primary"}},
	}
	backup := &model.Manifest{
		Version: KnownManifestVersion,
		Entries: map[string]model.ManifestEntry{"x": {Slug: "x", Title: "Backup"}},
	}
	putBlob(t, store, PrimaryKey, mustEncode(t, primary), "application/json")
	putBlob(t, store, BackupKey, mustEncode(t, backup), "application/json")

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries["x"].Title != "Primary" {
		t.Errorf("Load should prefer primary when both decode; got %q", got.Entries["x"].Title)
	}
}

// TestLoad_FutureVersionDecodes confirms reading is forgiving — a
// future-schema manifest decodes successfully via Load. Save will
// refuse on the way out (covered by TestSave_FutureVersionRefused).
func TestLoad_FutureVersionDecodes(t *testing.T) {
	store := r2.NewFake()
	body, _ := json.Marshal(map[string]any{
		"version":                               KnownManifestVersion + 100,
		"entries":                               map[string]any{},
		"new_field_only_known_to_future_binary": true,
	})
	putBlob(t, store, PrimaryKey, body, "application/json")

	got, err := Load(context.Background(), store)
	if err != nil {
		t.Fatalf("Load should be forgiving on read: %v", err)
	}
	if got.Version != KnownManifestVersion+100 {
		t.Errorf("future-version Version = %d; want %d", got.Version, KnownManifestVersion+100)
	}
}

func TestEncode_RoundTripPreservesUnknownRootFields(t *testing.T) {
	m, err := Decode(strings.NewReader(`{
  "version": 1,
  "entries": {},
  "future_root_field": {"nested": true}
}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	var buf bytes.Buffer
	if err := Encode(m, &buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if _, ok := raw["future_root_field"]; !ok {
		t.Fatalf("future_root_field dropped during round-trip: %s", buf.Bytes())
	}
}

type observingStorage struct {
	base         *r2.Fake
	onPrimaryPut func()
}

func (s *observingStorage) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if key == PrimaryKey && s.onPrimaryPut != nil {
		s.onPrimaryPut()
	}
	return s.base.PutObject(ctx, key, r, size, contentType)
}

func (s *observingStorage) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return s.base.GetObject(ctx, key)
}

func (s *observingStorage) HeadObject(ctx context.Context, key string) (r2.ObjectInfo, error) {
	return s.base.HeadObject(ctx, key)
}

func (s *observingStorage) DeleteObject(ctx context.Context, key string) error {
	return s.base.DeleteObject(ctx, key)
}

func (s *observingStorage) ListObjects(ctx context.Context, prefix string) ([]r2.ObjectInfo, error) {
	return s.base.ListObjects(ctx, prefix)
}
