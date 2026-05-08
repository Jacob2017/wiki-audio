// Package integration hosts opt-in tests that hit real external services
// (Cloudflare Worker, R2, ElevenLabs). Every file in this package carries
// the `//go:build integration` build tag and every test calls
// testutil.RequireIntegration(t) as its first line — see AGENTS.md §4.
//
// CI runs `go test ./...` only; this package compiles to a no-op there
// because all its files are tagged out.
package integration
