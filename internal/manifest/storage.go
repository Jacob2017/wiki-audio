package manifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// R2 keys for the manifest pair.
const (
	// PrimaryKey is the canonical manifest object in R2.
	PrimaryKey = "pg.manifest.json"

	// BackupKey is the previous-good manifest, rotated before each
	// successful Save. Recovery target when PrimaryKey is corrupt
	// (§6 "Manifest JSON corruption" recovery story).
	BackupKey = "pg.manifest.json.bak"

	// contentType is the MIME type stamped on both manifest objects.
	contentType = "application/json"
)

// Load fetches the manifest from R2 (PrimaryKey), falling back to
// BackupKey if the primary is missing/corrupt, and finally returning
// a fresh empty manifest if nothing is on R2 yet.
//
// Outcomes (wa-i1l.2):
//
//   - Primary missing                → empty Manifest{Version: KnownManifestVersion, Entries: {}}
//   - Primary present + decodes OK   → parsed Manifest
//   - Primary present + decode fails → fall back to BackupKey
//                                       → backup decodes OK   → return backup
//                                       → backup missing/bad  → abort with manual-inspection error
//
// Reading is forgiving (json.Unmarshal ignores unknown fields), so a
// future-version manifest reads fine here. The version guard fires
// at write-time inside Save; CheckCompatible is the predicate.
func Load(ctx context.Context, store r2.Storage) (*model.Manifest, error) {
	primaryBody, err := store.GetObject(ctx, PrimaryKey)
	if errors.Is(err, r2.ErrNoSuchKey) {
		return &model.Manifest{
			Version: KnownManifestVersion,
			Entries: make(map[string]model.ManifestEntry),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manifest: load primary %s: %w", PrimaryKey, err)
	}

	primaryBytes, readErr := io.ReadAll(primaryBody)
	_ = primaryBody.Close()
	if readErr != nil {
		return nil, fmt.Errorf("manifest: read primary %s: %w", PrimaryKey, readErr)
	}

	if m, decodeErr := Decode(bytes.NewReader(primaryBytes)); decodeErr == nil {
		return m, nil
	} else {
		// Primary did not decode — try backup.
		bakBody, bakErr := store.GetObject(ctx, BackupKey)
		if bakErr != nil {
			return nil, fmt.Errorf(
				"manifest: %s failed to decode (%v); backup %s unavailable (%v); manual inspection required",
				PrimaryKey, decodeErr, BackupKey, bakErr,
			)
		}
		bakBytes, bakReadErr := io.ReadAll(bakBody)
		_ = bakBody.Close()
		if bakReadErr != nil {
			return nil, fmt.Errorf(
				"manifest: %s failed to decode (%v); could not read %s (%v); manual inspection required",
				PrimaryKey, decodeErr, BackupKey, bakReadErr,
			)
		}
		bakM, bakDecodeErr := Decode(bytes.NewReader(bakBytes))
		if bakDecodeErr != nil {
			return nil, fmt.Errorf(
				"manifest: %s failed to decode (%v); backup %s also corrupt (%v); manual inspection required",
				PrimaryKey, decodeErr, BackupKey, bakDecodeErr,
			)
		}
		return bakM, nil
	}
}

// Save uploads m to R2 with the wa-i1l.2 .bak rotation:
//
//  1. Refuse-and-upgrade guard: if m.Version exceeds the known
//     schema, return ErrSchemaTooNew without touching R2.
//  2. Read the current PrimaryKey, if any. If missing, the rotation
//     is a graceful no-op (first save on a fresh bucket).
//  3. PUT the prior bytes to BackupKey, overwriting any older .bak.
//  4. PUT the new manifest to PrimaryKey.
//
// R2's PutObject is atomic per object, so a partial upload during
// step 3 or 4 leaves the previous-good object intact. Steps are
// strictly ordered — a step-3 failure aborts before step 4 fires,
// keeping the on-disk pair self-consistent (primary still has the
// pre-Save content; .bak still has whatever it had before).
//
// Save is NOT atomic across the pair: a crash between steps 3 and 4
// leaves PrimaryKey unchanged but BackupKey holding what is now ALSO
// PrimaryKey (i.e. the same content under both keys). Subsequent
// Loads still observe a consistent view; the next Save updates the
// pair correctly. This trade-off is documented because the
// alternative (single-blob CAS or a real S3 multi-object transaction)
// is unavailable on R2 and not worth synthesising.
func Save(ctx context.Context, store r2.Storage, m *model.Manifest) error {
	if err := CheckCompatible(m); err != nil {
		return err
	}

	// Step 2: pull current primary bytes (if any).
	var priorBytes []byte
	cur, err := store.GetObject(ctx, PrimaryKey)
	switch {
	case errors.Is(err, r2.ErrNoSuchKey):
		// First save on this bucket — skip the rotation step.
	case err != nil:
		return fmt.Errorf("manifest: save: read current %s: %w", PrimaryKey, err)
	default:
		priorBytes, err = io.ReadAll(cur)
		_ = cur.Close()
		if err != nil {
			return fmt.Errorf("manifest: save: read current %s body: %w", PrimaryKey, err)
		}
	}

	// Step 3: rotate prior to .bak, if there was a prior.
	if priorBytes != nil {
		if _, err := store.PutObject(ctx, BackupKey,
			bytes.NewReader(priorBytes), int64(len(priorBytes)), contentType); err != nil {
			return fmt.Errorf("manifest: save: rotate to %s: %w", BackupKey, err)
		}
	}

	// Step 4: encode + write new manifest. Encode applies the version
	// bump for older-than-known manifests as a side effect on m.
	var buf bytes.Buffer
	if err := Encode(m, &buf); err != nil {
		return fmt.Errorf("manifest: save: encode: %w", err)
	}
	body := buf.Bytes()
	if _, err := store.PutObject(ctx, PrimaryKey,
		bytes.NewReader(body), int64(len(body)), contentType); err != nil {
		return fmt.Errorf("manifest: save: put %s: %w", PrimaryKey, err)
	}
	return nil
}
