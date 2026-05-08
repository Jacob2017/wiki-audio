// Package e2e holds in-process end-to-end test harnesses that wire
// multiple internal/* packages together against fixtures under
// testdata/.
//
// The harnesses run the production code paths — no mocks, no
// stubs — but do NOT call any external service (ElevenLabs, R2, the
// Worker). Live-network integration tests live in dedicated beads
// (wa-4cw.12, wa-i1l.18, wa-3ia.5) and are gated by the
// `WIKI_AUDIO_RUN_INTEGRATION` env var; the e2e harnesses run on
// every `go test ./...` invocation.
package e2e
