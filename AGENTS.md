# AGENTS.md

Contributor manual for `Jacob2017/wiki-audio`. Read this once and you have
the conventions; the bead graph at `.beads/` is the implementation plan.

The README (`wa-lz9.2`) is the user-facing manual; this file is the
contributor manual; `PLAN_FOR_AUDIO_LIBRARY.md` (in the wiki repo) is the
design doc. Three documents, three audiences, no overlap.

### Repo layout (cross-link wa-jlb.4)

```
wiki-audio/
├── cmd/wiki-audio/main.go      entrypoint (thin)
├── internal/
│   ├── cli/                    Cobra command tree
│   ├── config/                 TOML + .env loading
│   ├── model/                  data structs (§2)
│   ├── extract/                markdown → CleanedDocument (§5.1)
│   ├── chunk/                  paragraph-bounded chunker (§5.2)
│   ├── tts/                    ElevenLabs client (§5.3)
│   ├── concat/                 ffmpeg subprocess wrapper (§5.4)
│   ├── id3/                    MP3 metadata (§5.5)
│   ├── manifest/               manifest load/save (§5.6)
│   ├── r2/                     S3-compatible (minio-go) client
│   └── feed/                   RSS XML rendering (§5.7)
├── worker/                     Cloudflare Worker (token gate)
├── .github/workflows/          release.yml lands in Phase C
├── .beads/                     bead graph (source of truth)
└── PLAN_FOR_AUDIO_LIBRARY.md   design doc (provenance)
```

Everything except `cmd/wiki-audio/` is `internal/`. The compiler
enforces "no external imports"; bypassing that requires a bead.

## 1. Project orientation

`wiki-audio` is a Go CLI that turns Paul Graham essays into a private
podcast feed: extract markdown → chunk → ElevenLabs TTS → ffmpeg
concat → ID3 tags → Cloudflare R2 → RSS XML behind a token-gated
Worker. The master bead is **wa-hqn**; read its body and the canonical
"reference index" comment for the why-this-exists pitch and the
section-by-section facts map. The full design doc is
`PLAN_FOR_AUDIO_LIBRARY.md` and lives in the wiki content repo — do
not duplicate it here, cite it.

## 2. Workflow with `br` (beads_rust)

Every implementation decision lives in a bead. The bead graph is the
source of truth; this file is just an index above it.

- `br ready` — list unblocked work. Pick the lowest-priority leaf.
- `br ready --json | jq` — same, machine-readable for agents.
- `br dep tree wa-hqn` — full dependency graph from the master.
- `br show <id>` — read a bead before quoting it.
- `br update <id> --status=in_progress` — claim before editing.
- `br comments add <id> --message "..."` — leave verification notes.
- `br close <id>` — only after every acceptance criterion passes.
- `bv --robot-triage` — graph-aware queue if you need a re-rank.

Set `BR_ACTOR=jacob` in your shell so audit trail entries are
attributed correctly. The polish loop pinned this convention; it is
not optional.

**Never edit `.beads/issues.jsonl` directly.** All bead mutations go
through the `br` CLI so the audit trail and JSONL shape stay
consistent.

## 3. Coding conventions

These patterns were pinned across the planning polish loop. Treat
deviations as bugs.

- **Structured logging** — `log/slog` everywhere. The CLI root wires
  a text handler by default, JSON under `--json`. Verbose flips to
  DEBUG, quiet flips to WARN; `--verbose` and `--quiet` are mutually
  exclusive (enforced in the root's `PersistentPreRunE`). Never log
  the access token, ElevenLabs API key, or any value loaded from
  `.env`. Cross-link **wa-76r.3**.
- **Atomic local writes** — `os.CreateTemp` + `os.Rename` for every
  state file (manifest snapshots, .env writes, config writes). A
  helper in `internal/atomic` (or wherever it lands) is the only
  sanctioned writer; non-atomic `os.WriteFile` for state is a
  regression. Cross-link **wa-76r.2**.
- **Body-hash invariants** — `body_hash = sha256(body || voice_id ||
  model_id || footnote_policy_version)`. Concatenation is byte-level.
  Bitrate is **not** in the hash; a format change requires `--force`
  rebuild. Changing any input field invalidates every essay; that is
  intentional. Cross-link **wa-kyn.1**, **wa-kyn.9**.
- **Manifest schema** — every manifest carries a `schema_version`.
  Refuse to load newer schemas (forward-compat guard); refuse to
  write degraded manifests. Cross-link **wa-76r.1**.
- **Cobra subcommand structure** — stub-and-fill pattern:
  `cmd/wiki-audio/main.go` is the entrypoint; every subcommand lives
  in `internal/cli/<name>.go`; new subcommands are registered up-front
  even when stubbed so `--help` shows the full surface from day one.
  Cross-link **wa-jlb.5**.
- **`internal/`-first** — every package except `cmd/wiki-audio/` is
  under `internal/` so the compiler enforces no external imports.
  Adding a public package needs a bead.

## 4. Test conventions

The polish loop pinned three test tiers and the gating story.

- **Unit tier** — always run, hermetic, no network. `go test ./...`
  must finish in seconds with no env vars set.
- **Orchestration tier** — uses an in-memory R2 fake plus
  `httptest.NewServer` ElevenLabs stub. No network, no credits.
  Runs on every PR.
- **Integration tier** — opt-in via `WIKI_AUDIO_RUN_INTEGRATION=1`
  and the `integration` build tag. CI never burns ElevenLabs credits
  by accident. Cross-link **wa-76r.10**.

Patterns:

- **`t.Helper()` `RequireIntegration(t)`** — every integration test's
  first line. Skips with a reason string when the env var is unset.
- **Golden files** — every test that has a golden artifact regenerates
  via `go test ./... -update` (single flag, not per-package). Cross-link
  **wa-kyn.22**.
- **Structured-logging assertion** — tests use `slog.With("test",
  t.Name(), ...)` so a CI failure log is greppable to the producing
  test.
- **No `t.Skip()` without a reason string.** Every skip names the
  missing precondition (env var, binary on PATH, network, etc.).

## 5. Security posture

The §9 threat model boundaries are explicit and argued in **wa-hqn**.
Anyone proposing OAuth / signed URLs / Cloudflare Access must explain
which of the §9.4 acceptances moved from acceptable to unacceptable
before re-debating the design.

- **Access token** — 32 bytes from `crypto/rand`, base64-url no
  padding, exactly 43 chars. Constant-time compare in the Worker
  (`timingSafeEqual` — OR-fold XOR loop, length check first).
  Replacing the loop with `===` is a regression. Cross-link
  **wa-3ia.1**.
- **Token rotation runbook** — **wa-76r.4**. Rotate when you suspect
  exposure or once a year, whichever sooner. Yearly bucket-privacy
  audit is **wa-3ia.4**.
- **`.env` permissions** — chmod 600 on write; a fail-closed startup
  check refuses to run if `mode & 0077 != 0`. Cross-link **wa-kyn.3**.
- **Worker contract** — bare URL ⇒ 403; wrong token ⇒ 403; correct
  token + missing key ⇒ 404. The 403/403/404 ordering is the
  regression signal; cross-link **wa-3ia.2**.
- **Cache-control** — Worker responses set `cache-control: private`
  so Cloudflare's edge does not cache across token values. Removing
  this is a leak path.
- **Pre-commit guard** — gitleaks via the agent-mail pre-commit hook
  defends against accidental `.env` commits. Cross-link **wa-76r.5**.
- **Never log secrets** — token, ElevenLabs API key, every key/value
  loaded from `.env`. Slog handlers must redact; tests assert no
  secret substring appears in captured logs.

## 6. Release & distribution

Phase C beads own the release surface. Cross-link **wa-8gt.1** through
**wa-8gt.9**.

- **goreleaser matrix** — linux/amd64, linux/arm64, darwin/amd64,
  darwin/arm64. Cross-link **wa-8gt.1**.
- **GitHub Actions release** — `.github/workflows/release.yml`
  triggers on tag push and runs goreleaser. Cross-link **wa-8gt.2**.
- **`install.sh`** — `uname` detect, sha256 verify, install to
  `~/.local/bin` (or `$WIKI_AUDIO_INSTALL_DIR`). The install URL is
  hard-coded `raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh`.
  Cross-link **wa-8gt.3**.
- **`--version`** — ldflags-injected via `-X
  github.com/Jacob2017/wiki-audio/internal/cli.Version=...`. Cross-link
  **wa-8gt.4**.
- **Three release tags** — `v0.0.1` distribution smoke (**wa-8gt.6**),
  `v0.1.0` Phase D milestone (**wa-kyn.20**), `v1.0.0` Phase F gate
  (**wa-i1l.14**). Each tag has a post-release verification checklist
  living on the owning bead.

## 7. What this project will NOT do

The polish loop pinned the v1 scope explicitly. Every line below is a
deferred bead with its own deferral rationale; do not "just add" any
of them without reading the deferral.

- Insight / analysis pages — separate authoring vector. Cross-link
  **wa-o2f.1**.
- Other authors — per-author voice/branding is post-v2.
- The `audio: true` opt-in frontmatter — Phase I deferred. Cross-link
  **wa-o2f.1**, **wa-o2f.2**.
- Multi-feed (`insights.xml`) — Phase I deferred. Cross-link
  **wa-o2f.3**.
- Per-page voice override (`audio_voice: hq`) — Phase I deferred.
  Cross-link **wa-o2f.4**.

If a feature is here, the right move is to read the deferred bead and
either (a) accept the deferral or (b) open a new bead arguing why v1
should change scope. Skipping straight to implementation rewrites the
plan.

## 8. Quick-start for a new contributor

1. Clone `Jacob2017/wiki-audio` (this repo).
2. Read **wa-hqn** end-to-end — the master bead. Skim its "canonical
   reference index" comment to see where every fact lives.
3. `BR_ACTOR=jacob br ready` — work the unblocked leaves. Lowest
   priority first.
4. `br show <id>` BEFORE re-reading `PLAN_FOR_AUDIO_LIBRARY.md`. The
   bead graph is the source of truth; the plan is provenance.
5. Reserve files via MCP Agent Mail (`file_reservation_paths` with
   narrow patterns) before editing — the swarm coordinates through
   the reservation table, not just commits.
6. `go build ./...` and `go vet ./...` must stay clean. `go test
   ./...` must pass. Add a bead for any deviation.
7. Commit messages reference the bead id you're closing (e.g.,
   `Closes wa-kyn.4.`). Push to `main`; this is a single-author repo
   with a fast-forward-only history.

Welcome aboard. The bead graph and this file together are enough to
land any change end-to-end.
