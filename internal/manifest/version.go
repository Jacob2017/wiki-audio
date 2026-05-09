package manifest

import (
	"errors"
	"fmt"

	"github.com/Jacob2017/wiki-audio/internal/model"
)

// KnownManifestVersion is the schema version this binary was built
// to read AND write. It mirrors model.ManifestSchemaVersion (the
// on-the-wire constant used by the wire format) so a single bump in
// the model package ratchets every binary forward in lockstep
// (wa-76r.1).
//
// Bumping policy (§6 "Tool version mismatch"): when a PR introduces
// a new field on model.Manifest or model.ManifestEntry, that PR
// MUST also bump model.ManifestSchemaVersion by one. Old binaries
// loading the bumped manifest then fail closed at Save (refuse-and-
// upgrade) instead of silently dropping the new field on write-back.
const KnownManifestVersion = model.ManifestSchemaVersion

// ErrSchemaTooNew is returned when a loaded manifest declares a
// version this binary does not know how to write back. The message
// is part of §6's recovery story — wa-76r.1 pins it as user-facing
// copy that wiki-audio upgrade resolves.
var ErrSchemaTooNew = errors.New(
	"manifest schema is newer than this binary; run `wiki-audio upgrade` and try again")

// CheckCompatible verifies a loaded *model.Manifest can be safely
// written back by this binary.
//
//	loaded.Version > KnownManifestVersion  → ErrSchemaTooNew (wrapped
//	                                          with the version numbers)
//	loaded.Version <= KnownManifestVersion → nil
//
// Reading is forgiving (json.Unmarshal ignores unknown fields), so
// the guard fires at WRITE-time. Older versions are upgraded to
// KnownManifestVersion by Save before serialization (silent ratchet
// — the read happens, the user keeps working, and the next write
// publishes the canonical shape).
//
// A nil manifest is a programmer error and returns a distinct
// non-wrapped error so tests cannot accidentally observe
// ErrSchemaTooNew on a nil pointer.
func CheckCompatible(loaded *model.Manifest) error {
	if loaded == nil {
		return errors.New("manifest: nil manifest")
	}
	if loaded.Version > KnownManifestVersion {
		return fmt.Errorf("%w (loaded version=%d, known version=%d)",
			ErrSchemaTooNew, loaded.Version, KnownManifestVersion)
	}
	return nil
}
