package manifest

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Load decodes a JSON manifest from r. Read-only; the version guard
// does NOT fire here because reading is forgiving — json.Unmarshal
// ignores unknown fields, so an older binary can still inspect a
// newer manifest. Callers that intend to write back MUST call
// CheckCompatible (or just call Save, which calls it for them)
// before serializing.
//
// An empty Entries map is initialized when the on-disk JSON omits
// the field so callers don't have to nil-check before iteration.
func Load(r io.Reader) (*model.Manifest, error) {
	var m model.Manifest
	dec := json.NewDecoder(r)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	if m.Entries == nil {
		m.Entries = make(map[string]model.ManifestEntry)
	}
	return &m, nil
}

// Save serializes m as JSON to w with the wa-76r.1 version guard:
//
//   - m.Version > KnownManifestVersion → ErrSchemaTooNew (wrapped);
//     w is NOT written to (the guard is a write-time refuse-and-
//     upgrade, per §6).
//   - m.Version < KnownManifestVersion → silently bumped to
//     KnownManifestVersion before serialization (write-time
//     upgrade).
//   - m.Version == KnownManifestVersion → as-is.
//
// JSON output is indent=2 with "\n" line endings and a trailing
// newline so PR diffs (when the manifest is committed for any
// reason) stay line-readable. wa-i1l.16 (Phase F) wraps Save with
// the atomic-on-disk + R2-upload transport; this function is the
// pure encoding step.
func Save(m *model.Manifest, w io.Writer) error {
	if err := CheckCompatible(m); err != nil {
		return err
	}
	if m.Version < KnownManifestVersion {
		m.Version = KnownManifestVersion
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("manifest: encode: %w", err)
	}
	return nil
}
