package manifest

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// Decode parses a JSON manifest from r. Read-only; the version guard
// does NOT fire here because reading is forgiving — json.Unmarshal
// ignores unknown fields, so an older binary can still inspect a
// newer manifest. Callers that intend to write back MUST call
// CheckCompatible (or just call Encode, which calls it for them)
// before serializing.
//
// An empty Entries map is initialized when the on-disk JSON omits
// the field so callers don't have to nil-check before iteration.
//
// Pure encoding step. The R2-round-trip wrapper that adds
// pg.manifest.json + .bak transport is Load (in storage.go).
func Decode(r io.Reader) (*model.Manifest, error) {
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

// Encode serializes m as JSON to w with the wa-76r.1 version guard:
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
// reason) stay line-readable.
//
// Pure encoding step. The R2-round-trip wrapper that adds the
// .bak rotation + atomic upload is Save (in storage.go).
func Encode(m *model.Manifest, w io.Writer) error {
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
