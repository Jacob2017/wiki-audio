// Package model defines the data structures from §2 of
// PLAN_FOR_AUDIO_LIBRARY.md and the constants pinned by §5.
//
// Pure data + the body_hash function — no I/O, no logging, no config
// loading. Other packages depend on these types but model imports only
// stdlib.
//
// JSON tags drive the wire format of the R2-hosted manifest
// (pg.manifest.json). TOML tags drive the on-disk config file
// (~/.wiki-audio/config.toml). Both encoders ship in the stdlib +
// BurntSushi/toml.
package model
