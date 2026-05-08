// Package manifest loads and saves the canonical manifest.json from R2
// (§2, §5.6). The manifest is the source of truth for body_hash → R2
// key mappings; episodes whose body_hash matches a stored entry are
// skipped on rebuild.
package manifest
