package manifest

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

var manifestUnknownFields sync.Map // map[*model.Manifest]map[string]json.RawMessage

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
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("manifest: decode: read: %w", err)
	}

	var m model.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	if m.Entries == nil {
		m.Entries = make(map[string]model.ManifestEntry)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("manifest: decode raw: %w", err)
	}
	delete(raw, "version")
	delete(raw, "entries")
	delete(raw, "last_build_at")
	delete(raw, "last_publish_at")
	rememberUnknownFields(&m, raw)
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

	if m.Entries == nil {
		m.Entries = make(map[string]model.ManifestEntry)
	}

	wire := cloneUnknownFields(m)
	if wire == nil {
		wire = make(map[string]json.RawMessage)
	}
	if err := marshalField(wire, "version", m.Version); err != nil {
		return fmt.Errorf("manifest: encode version: %w", err)
	}
	if err := marshalField(wire, "entries", m.Entries); err != nil {
		return fmt.Errorf("manifest: encode entries: %w", err)
	}
	if m.LastBuildAt != nil {
		if err := marshalField(wire, "last_build_at", m.LastBuildAt); err != nil {
			return fmt.Errorf("manifest: encode last_build_at: %w", err)
		}
	} else {
		delete(wire, "last_build_at")
	}
	if m.LastPublishAt != nil {
		if err := marshalField(wire, "last_publish_at", m.LastPublishAt); err != nil {
			return fmt.Errorf("manifest: encode last_publish_at: %w", err)
		}
	} else {
		delete(wire, "last_publish_at")
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(wire); err != nil {
		return fmt.Errorf("manifest: encode: %w", err)
	}
	return nil
}

func rememberUnknownFields(m *model.Manifest, extras map[string]json.RawMessage) {
	if len(extras) == 0 {
		manifestUnknownFields.Delete(m)
		return
	}
	cloned := make(map[string]json.RawMessage, len(extras))
	for key, value := range extras {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	manifestUnknownFields.Store(m, cloned)
}

func cloneUnknownFields(m *model.Manifest) map[string]json.RawMessage {
	raw, ok := manifestUnknownFields.Load(m)
	if !ok {
		return nil
	}
	extras := raw.(map[string]json.RawMessage)
	cloned := make(map[string]json.RawMessage, len(extras))
	for key, value := range extras {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func marshalField(out map[string]json.RawMessage, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	out[key] = raw
	return nil
}
