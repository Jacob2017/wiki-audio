package manifest

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// --- CheckCompatible — the three version-comparison cases ---------------

func TestCheckCompatible_EqualVersionOK(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion}
	if err := CheckCompatible(m); err != nil {
		t.Errorf("equal version should be ok; got %v", err)
	}
}

func TestCheckCompatible_OlderVersionOK(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion - 1}
	if err := CheckCompatible(m); err != nil {
		t.Errorf("older version should be ok (read forgiving); got %v", err)
	}
}

func TestCheckCompatible_NewerVersionRefused(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion + 1}
	err := CheckCompatible(m)
	if err == nil {
		t.Fatal("newer version must error")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("error should wrap ErrSchemaTooNew; got %v", err)
	}
}

func TestCheckCompatible_NilManifest(t *testing.T) {
	err := CheckCompatible(nil)
	if err == nil {
		t.Fatal("nil manifest must error")
	}
	if errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("nil manifest should NOT report as ErrSchemaTooNew (programmer error vs version mismatch)")
	}
}

// --- Sentinel message — wa-76r.1 user-facing copy guarantee --------------

func TestVersionGuardMessage_IsActionable(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion + 5}
	err := CheckCompatible(m)
	if err == nil {
		t.Fatal("expected error for future schema")
	}
	const wantPhrase = "manifest schema is newer than this binary; run `wiki-audio upgrade` and try again"
	if !strings.Contains(err.Error(), wantPhrase) {
		t.Errorf("error message missing the §6 actionable copy;\n got: %q\nwant substring: %q", err.Error(), wantPhrase)
	}
	// Numbers should appear so the user knows the gap.
	if !strings.Contains(err.Error(), "loaded version=") {
		t.Errorf("error should report the loaded version; got %q", err.Error())
	}
}

// --- Save — the three "behavior pinned" rows from the bead ---------------

func TestSave_EqualVersionWritesAsIs(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion}
	var buf bytes.Buffer
	if err := Save(m, &buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	round, err := Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if round.Version != KnownManifestVersion {
		t.Errorf("round-trip Version = %d; want %d", round.Version, KnownManifestVersion)
	}
}

func TestSave_OlderVersionBumpsToCurrent(t *testing.T) {
	if KnownManifestVersion == 0 {
		t.Skip("no older version exists for KnownManifestVersion=0")
	}
	m := &model.Manifest{Version: KnownManifestVersion - 1}
	var buf bytes.Buffer
	if err := Save(m, &buf); err != nil {
		t.Fatalf("Save (older): %v", err)
	}
	if m.Version != KnownManifestVersion {
		t.Errorf("Save should bump in-memory Version to current; got %d", m.Version)
	}
	round, err := Load(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if round.Version != KnownManifestVersion {
		t.Errorf("on-disk Version after upgrade-save = %d; want %d", round.Version, KnownManifestVersion)
	}
}

func TestSave_NewerVersionRefusedNothingWritten(t *testing.T) {
	m := &model.Manifest{Version: KnownManifestVersion + 1}
	var buf bytes.Buffer
	err := Save(m, &buf)
	if err == nil {
		t.Fatal("Save with future version must error")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("Save error should wrap ErrSchemaTooNew; got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing should be written on refusal; got %d bytes: %q", buf.Len(), buf.String())
	}
}

// --- Load — happy path + null-entries safety ----------------------------

func TestLoad_DecodesValidJSON(t *testing.T) {
	const j = `{"version": 1, "entries": {"slug-a": {"slug":"slug-a","title":"A","body_hash":"h","voice_id":"v","model_id":"m","char_count":1,"chunk_count":1,"duration_seconds":1,"file_size_bytes":1,"generated_at":"2026-05-08T00:00:00Z"}}}`
	m, err := Load(strings.NewReader(j))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 {
		t.Errorf("Version = %d; want 1", m.Version)
	}
	if len(m.Entries) != 1 || m.Entries["slug-a"].Title != "A" {
		t.Errorf("entries not populated: %v", m.Entries)
	}
}

func TestLoad_EmptyEntriesIsInitialized(t *testing.T) {
	m, err := Load(strings.NewReader(`{"version": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Entries == nil {
		t.Error("Entries should be a non-nil map even when on-disk JSON omits it")
	}
}

func TestLoad_RejectsInvalidJSON(t *testing.T) {
	_, err := Load(strings.NewReader(`{"version":`))
	if err == nil {
		t.Fatal("malformed JSON must error")
	}
}

// --- Load-then-Save with a future schema rejects on Save -----------------
// Mirrors the wa-i1l.16 "version_guard_refuses_overwrite" pin: a newer
// on-disk schema can be inspected (read-only) but Save refuses.

func TestLoadThenSave_FutureSchemaRejectedOnSave(t *testing.T) {
	future := `{"version": 9999, "entries": {}}`
	m, err := Load(strings.NewReader(future))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = Save(m, &buf)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("Save should refuse a future-schema manifest; got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nothing written on refusal; got %q", buf.String())
	}
}

// --- KnownManifestVersion mirrors model.ManifestSchemaVersion ------------

func TestKnownManifestVersion_MirrorsModel(t *testing.T) {
	if KnownManifestVersion != model.ManifestSchemaVersion {
		t.Errorf("KnownManifestVersion (%d) must equal model.ManifestSchemaVersion (%d) — single source of truth",
			KnownManifestVersion, model.ManifestSchemaVersion)
	}
}
