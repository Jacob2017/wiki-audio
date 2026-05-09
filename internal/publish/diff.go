package publish

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Jacob2017/wiki-audio/internal/model"
	"github.com/Jacob2017/wiki-audio/internal/r2"
)

// EpisodePrefix is the R2 key prefix for podcast episode MP3s.
// Manifest entries that omit ManifestEntry.R2Key default to
// EpisodePrefix + slug + ".mp3".
const EpisodePrefix = "pg/"

// Plan is the pre-upload diff between the local manifest's expected
// R2 state and what's actually on R2. Phase F's publish orchestrator
// (wa-i1l.7) consumes a Plan and routes each bucket through the
// uploader (wa-i1l.4) or skips.
type Plan struct {
	// ToUpload — manifest entries whose R2 object is missing
	// (HeadObject returned r2.ErrNoSuchKey).
	ToUpload []model.ManifestEntry

	// ToOverwrite — manifest entries whose R2 ETag differs from
	// the entry's recorded R2ETag. Cause: a different body was
	// uploaded against the same key by some other path (tampering,
	// out-of-band sync, manual mc cp), or a prior upload failed
	// to update the manifest. Either way, re-upload to converge.
	ToOverwrite []model.ManifestEntry

	// Stale — R2 objects under EpisodePrefix that no manifest
	// entry claims. Default action is to leave them alone (wa-
	// i1l.10 will wire `--prune`); they are reported here so the
	// publish summary can flag them.
	Stale []r2.ObjectInfo

	// Unchanged — manifest entries whose R2 ETag matches and
	// therefore need no upload. Reported so callers can log a
	// "skipping N unchanged" summary line.
	Unchanged []model.ManifestEntry
}

// String returns the §3 publish-output one-line summary:
//
//	"diff: 4 new, 1 changed, 0 stale-on-r2"
//
// Unchanged is intentionally omitted from the headline because the
// listener cares about what changes, not what doesn't.
func (p *Plan) String() string {
	return fmt.Sprintf("diff: %d new, %d changed, %d stale-on-r2",
		len(p.ToUpload), len(p.ToOverwrite), len(p.Stale))
}

// Diff computes the publish plan against R2 (wa-i1l.3).
//
// Algorithm:
//
//  1. ListObjects(EpisodePrefix) — single call, enumerates current
//     R2 state under the prefix. ETags from this call are not used
//     to classify manifest entries (HEAD per known key in step 2 is
//     the canonical comparison) but ARE the source of stale
//     candidates in step 3.
//
//  2. For each manifest entry, sorted by slug for deterministic
//     output: HeadObject(entry's R2 key).
//     - r2.ErrNoSuchKey      → ToUpload
//     - HEAD ETag != entry's → ToOverwrite
//     - HEAD ETag == entry's → Unchanged
//     - any other error      → propagated (publish aborts; nothing
//                              has been uploaded yet, safe to retry).
//
//  3. Any object from step 1 whose key was not visited in step 2 is
//     Stale.
//
// Diff is read-only — no PutObject / DeleteObject calls. A nil
// manifest is a programmer error and returns a non-r2 error so the
// caller can distinguish "missing manifest" from "R2 unavailable".
func Diff(ctx context.Context, store r2.Storage, m *model.Manifest) (*Plan, error) {
	if m == nil {
		return nil, errors.New("publish: nil manifest")
	}

	objects, err := store.ListObjects(ctx, EpisodePrefix)
	if err != nil {
		return nil, fmt.Errorf("publish: list %q: %w", EpisodePrefix, err)
	}

	plan := &Plan{}
	visited := make(map[string]struct{}, len(m.Entries))

	for _, slug := range sortedSlugs(m.Entries) {
		entry := m.Entries[slug]
		key := entryKey(entry, slug)
		visited[key] = struct{}{}

		info, err := store.HeadObject(ctx, key)
		switch {
		case errors.Is(err, r2.ErrNoSuchKey):
			plan.ToUpload = append(plan.ToUpload, entry)
		case err != nil:
			return nil, fmt.Errorf("publish: head %q: %w", key, err)
		case info.ETag != entry.R2ETag:
			plan.ToOverwrite = append(plan.ToOverwrite, entry)
		default:
			plan.Unchanged = append(plan.Unchanged, entry)
		}
	}

	for _, o := range objects {
		if _, seen := visited[o.Key]; seen {
			continue
		}
		plan.Stale = append(plan.Stale, o)
	}

	return plan, nil
}

// entryKey returns the canonical R2 key for a manifest entry. When
// ManifestEntry.R2Key is set (post-publish entries always carry it),
// that value wins; otherwise the EpisodePrefix + slug + ".mp3"
// convention is the fallback so a freshly-built entry whose first
// publish has not happened yet can still be classified.
func entryKey(e model.ManifestEntry, slug string) string {
	if e.R2Key != "" {
		return e.R2Key
	}
	return EpisodePrefix + slug + ".mp3"
}

func sortedSlugs(entries map[string]model.ManifestEntry) []string {
	out := make([]string, 0, len(entries))
	for s := range entries {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
