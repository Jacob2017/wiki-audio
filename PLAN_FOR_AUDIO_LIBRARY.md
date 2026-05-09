# Plan: PG Essays → Personal Podcast (v1)

## 1. Executive summary

**Problem.** The wiki holds 53 Paul Graham essays as raw markdown under `raw/processed/paul-graham/`. They are read at desk on a different device than the phone. Long-form prose is ideal listening material — gym, walks, commutes — but copy-pasting essays into TTS apps one at a time is friction.

**Solution.** A standalone CLI, `wiki-audio`, written in **Go** and distributed as a single static binary from its own GitHub repo (separate from the wiki repo). Installable on any Linux/macOS box via a single `curl | bash` line — the script auto-detects platform and pulls a prebuilt binary from GitHub Releases. The CLI runs locally on the wiki box, but its source lives elsewhere — the wiki repo holds only the data, the tool is a tool.

Two commands once installed:

1. **`wiki-audio build`** — walks the configured PG essays directory, strips Readwise-style metadata and markdown formatting, hashes each cleaned essay against an R2-hosted manifest, and (on hash miss) generates an MP3 via the ElevenLabs API.
2. **`wiki-audio publish`** — uploads new/changed MP3s to R2, regenerates an iTunes-namespaced RSS feed `pg.xml`, and writes the manifest back to R2. Both feed and episodes are gated by a Cloudflare Worker requiring a static random token (`?t=<token>`); the bucket itself has no public access.

The phone subscribes to the token-bearing feed URL in Pocket Casts. New episodes auto-download; speed control, queueing, position-sync, and offline playback come for free with the podcast app. Privacy mechanism is detailed in §9.

**Distribution model.**

```
┌─────────────────────────┐         ┌──────────────────────────────────┐
│  dev machine            │         │  GitHub                          │
│  ~/dev/wiki-audio/      │  push   │  Jacob2017/wiki-audio            │
│  go build / goreleaser  ├────────▶│  • main branch (source)          │
│                         │  tag    │  • Releases (binaries by tag)    │
└─────────────────────────┘         │     – wiki-audio_linux_amd64.tar │
                                    │     – wiki-audio_linux_arm64.tar │
                                    │     – wiki-audio_darwin_arm64.tar│
                                    │  • install.sh on main            │
                                    └─────────────┬────────────────────┘
                                                  │ curl -fsSL .../install.sh | bash
                                                  ▼
                              ┌────────────────────────────────────┐
                              │  wiki box (any machine)            │
                              │  • single binary at /usr/local/bin │
                              │  • config at ~/.wiki-audio/        │
                              │  • reads wiki content              │
                              │  • talks to ElevenLabs + R2        │
                              └────────────────────────────────────┘
```

Wiki box only needs the installed binary, a config file at `~/.wiki-audio/config.toml`, and secrets in `~/.wiki-audio/.env`. Manifest state lives in R2 (`pg.manifest.json`), so the wiki box is fully stateless apart from config — re-installs and machine swaps don't lose track of what's been published.

**Cost.** ~$99 one-shot on ElevenLabs Flash v2.5 (Pro tier, 1 month, then cancel). ~$0.01/month R2 storage thereafter. ~$0.50 per new PG-length essay added later.

**Why podcast feed over file sync / Audiobookshelf.** Podcast apps already solve the "long-form audio queue with resume across devices" problem better than any roll-your-own option. R2 + RSS is the cheapest delivery path with no egress fees and no server to maintain.

**Why a separate repo + curl install over a subdir of the wiki repo.** Keeps the tool's release cadence independent of the wiki's content cadence. Lets the same CLI install on multiple machines (work box, future machines) without dragging the wiki along. Matches the install ergonomics of tools like [beads_rust](https://github.com/Dicklesworthstone/beads_rust): one shell command, no clones, no source tree on the consuming machine.

**Why Go over Python.** Python CLIs install via `pipx`/`uv tool` — clean, but they require a Python toolchain on the target. A Go binary has zero runtime dependencies, cross-compiles cleanly to every target from one machine, and ships at ~10-20 MB. The trade-offs are honest: Python's ecosystem (especially `pysbd` for sentence segmentation) is richer; Go forces simpler heuristics. For this pipeline — straightforward HTTP, file IO, regex extraction, ffmpeg subprocess — Go's stdlib + a few small modules cover it without fighting the language.

**Out of scope for v1.** Insight/analysis pages. Other authors. The `audio: true` opt-in frontmatter flag (deferred — see §8).

---

## 2. Data models

Go structs in `internal/model/`. JSON tags drive the on-the-wire manifest format; TOML tags drive the config file. No external schema library — `encoding/json` and `BurntSushi/toml` handle both.

```go
package model

import "time"

// Parsed from the Readwise-style markdown header of each raw file.
type EssayMeta struct {
    Slug             string `json:"slug"`               // filename stem, kebab-cased
    Title            string `json:"title"`              // first "# " line
    Author           string `json:"author"`             // default "Paul Graham"
    SourcePath       string `json:"source_path"`        // absolute path to the .md
    SourceURL        string `json:"source_url,omitempty"`
    PublishDateText  string `json:"publish_date_text,omitempty"` // e.g. "July 2023"
}

// Output of the extraction stage — what gets sent to TTS.
type CleanedDocument struct {
    Meta             EssayMeta
    Body             string   // plain text, paragraph-segmented
    BodyHash         string   // sha256 of (body + voice_id + model_id + footnote_policy_version)
    CharCount        int
    WordCount        int
    SkippedSegments  []string // code blocks / orphan footnote markers
}

// One TTS request worth of text. Paragraph-bounded, ~4000 chars max.
type AudioChunk struct {
    Index     int
    Text      string
    CharCount int
}

// One row per essay in pg.manifest.json (stored in R2).
type ManifestEntry struct {
    Slug            string     `json:"slug"`
    Title           string     `json:"title"`
    BodyHash        string     `json:"body_hash"`
    VoiceID         string     `json:"voice_id"`
    ModelID         string     `json:"model_id"`
    CharCount       int        `json:"char_count"`
    ChunkCount      int        `json:"chunk_count"`
    DurationSeconds float64    `json:"duration_seconds"`
    FileSizeBytes   int64      `json:"file_size_bytes"`
    R2Key           string     `json:"r2_key,omitempty"`
    R2ETag          string     `json:"r2_etag,omitempty"`
    GeneratedAt     time.Time  `json:"generated_at"`
    PublishedAt     *time.Time `json:"published_at,omitempty"`
}

type Manifest struct {
    Version       int                       `json:"version"` // schema version, default 1
    Entries       map[string]ManifestEntry  `json:"entries"` // keyed by slug
    LastBuildAt   *time.Time                `json:"last_build_at,omitempty"`
    LastPublishAt *time.Time                `json:"last_publish_at,omitempty"`
}

// Loaded from ~/.wiki-audio/config.toml.
type Config struct {
    Wiki  WikiConfig  `toml:"wiki"`
    TTS   TTSConfig   `toml:"tts"`
    R2    R2Config    `toml:"r2"`
    Feed  FeedConfig  `toml:"feed"`
}

type WikiConfig struct {
    SourceDir string `toml:"source_dir"` // absolute path to PG essay folder
}

type TTSConfig struct {
    VoiceID          string  `toml:"voice_id"`
    VoiceLabel       string  `toml:"voice_label"`
    ModelID          string  `toml:"model_id"`         // default "eleven_flash_v2_5"
    ChunkMaxChars    int     `toml:"chunk_max_chars"`  // default 4000
    RequestTimeoutS  float64 `toml:"request_timeout_s"` // default 60
    RetryAttempts    int     `toml:"retry_attempts"`   // default 3
    RetryBackoffBase float64 `toml:"retry_backoff_base_s"` // default 2
    OutputFormat     string  `toml:"output_format"`    // default "mp3_44100_64"
}

type R2Config struct {
    AccountID string `toml:"account_id"`
    Bucket    string `toml:"bucket"`
    // Endpoint derived: https://<AccountID>.r2.cloudflarestorage.com
}

type FeedConfig struct {
    Title         string `toml:"title"`
    Description   string `toml:"description"`
    Author        string `toml:"author"`
    OwnerEmail    string `toml:"owner_email"`
    BaseURL       string `toml:"base_url"`     // e.g. https://wiki-audio.example.workers.dev
    FeedPath      string `toml:"feed_path"`    // default "pg.xml"
    CoverImageURL string `toml:"cover_image_url,omitempty"`
    Language      string `toml:"language"`     // default "en-us"
}

// Secrets loaded separately from ~/.wiki-audio/.env, never in TOML.
// Required env vars:
//   ELEVENLABS_API_KEY
//   R2_ACCESS_KEY_ID
//   R2_SECRET_ACCESS_KEY
//   WIKI_AUDIO_ACCESS_TOKEN  (embedded into feed enclosure URLs)
```

**Why hash includes voice_id + model_id + footnote_policy_version.** If voice or footnote behaviour changes, every essay must regenerate. Bundling these into the hash makes invalidation automatic — no manual cache busting.

**Why config.toml and .env are separate files.** TOML is checked-in-friendly (we'd want to tracking voice ID changes if config were git-tracked); the .env never is. Splitting them prevents accidental secret commits.

---

## 3. CLI / API surface

Cobra-based subcommand structure. Single binary `wiki-audio`. Reads config from `~/.wiki-audio/config.toml` (override with `--config <path>`) and secrets from `~/.wiki-audio/.env` (override with `--env <path>`, or `--env-local` to read `.env` from the current working directory — useful during development from inside the repo). All output to stdout/stderr; structured logging via `log/slog`.

### Install

```bash
# Standard: curl install (auto-detects platform, downloads from GitHub Releases)
$ curl -fsSL https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh | bash
detected: linux/amd64
fetching latest release: v0.3.1
downloaded wiki-audio_0.3.1_linux_amd64.tar.gz (12 MB)
verified sha256 ✓
installed: /usr/local/bin/wiki-audio
run: wiki-audio init  # to scaffold ~/.wiki-audio/

# Pin a version
$ curl -fsSL https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh | bash -s -- v0.3.0

# Manual: download a release tarball from the GitHub Releases page and untar
$ tar -xzf wiki-audio_0.3.1_linux_amd64.tar.gz && sudo mv wiki-audio /usr/local/bin/

# From source (requires Go 1.22+)
$ go install github.com/Jacob2017/wiki-audio/cmd/wiki-audio@latest

# Upgrade (re-runs the install script; idempotent)
$ wiki-audio upgrade
```

### `wiki-audio init`

Scaffolds the config directory on first install.

```bash
$ wiki-audio init
created ~/.wiki-audio/config.toml (with placeholders)
created ~/.wiki-audio/.env       (chmod 600, empty)
next: edit config.toml and populate .env, then run `wiki-audio doctor`
```

### `wiki-audio doctor`

Verifies config + secrets + reachability of dependencies before any real work.

```bash
$ wiki-audio doctor
config.toml         ✓ /home/jacobuntu/.wiki-audio/config.toml
.env                ✓ all 4 required env vars present
ffmpeg              ✓ ffmpeg version 6.1.1
wiki source dir     ✓ 53 .md files at /home/jacobuntu/dev/wiki/Wiki/raw/processed/paul-graham
ElevenLabs API      ✓ authenticated, voice "Christopher" reachable, 487,222 credits remaining on plan
R2 bucket           ✓ wiki-audio (private, 0 objects)
worker access       ✓ https://wiki-audio.example.workers.dev returns 403 without token, 404 with token
all checks passed.
```

### `wiki-audio build`

Walks the configured source dir, extracts and synthesizes any essay whose body hash isn't in the R2-hosted manifest.

```bash
# bulk: build everything that's stale
$ wiki-audio build
[1/53] how-to-do-great-work … extracted (52,481 chars), synthesizing (14 chunks)…
       generated how-to-do-great-work.mp3 (62.4 MB, 1h22m)
[2/53] beating-the-averages … extracted (8,210 chars, dropped 4 code blocks)…
       generated beating-the-averages.mp3 (9.1 MB, 12m04s)
…
build complete: 53 essays, 18h47m audio, 612 MB, 880,344 chars billed

# narrow to one essay (useful for iterating on extraction)
$ wiki-audio build --slug how-to-do-great-work

# dry-run: extract + chunk + estimate cost, but don't call API
$ wiki-audio build --dry-run
estimate: 880,344 chars × 0.5 credits/char (flash_v2_5) = 440,172 credits
estimate: ~$87 on Pro tier overage; fits within Pro monthly quota

# force rebuild even if hash matches
$ wiki-audio build --force --slug how-to-do-great-work

# stop after extraction; print cleaned text to stdout (no API calls)
$ wiki-audio build --slug beating-the-averages --extract-only
```

### `wiki-audio publish`

Diffs local manifest against R2, uploads changed MP3s, regenerates and uploads `pg.xml`.

```bash
$ wiki-audio publish
diff: 4 new, 1 changed, 0 stale-on-r2
uploading how-to-do-great-work.mp3 (62.4 MB) → r2://wiki-audio/pg/how-to-do-great-work.mp3 ✓
uploading beating-the-averages.mp3 (9.1 MB) → r2://wiki-audio/pg/beating-the-averages.mp3 ✓
…
regenerating pg.xml (53 items) → r2://wiki-audio/pg.xml ✓
feed live at https://wiki-audio.example.workers.dev/pg.xml?t=<token>

# regen feed only (e.g. after editing feed.toml metadata)
$ wiki-audio publish --feed-only

# preview: show diff and feed XML without uploading
$ wiki-audio publish --dry-run
```

### `wiki-audio inspect`

Read-only diagnostics.

```bash
$ wiki-audio inspect --slug beating-the-averages
title:        Beating the Averages
chars:        8,210 (cleaned) / 11,402 (raw)
dropped:      4 code blocks (Lisp), 0 footnotes
chunks:       3 × ~2,700 chars
last build:   2026-05-08T14:21:03Z
last publish: 2026-05-08T14:24:11Z
r2 url:       https://wiki-audio.example.workers.dev/pg/beating-the-averages.mp3?t=<token>
duration:     12m04s
```

### `wiki-audio cost`

Cost calculator without invoking the API.

```bash
$ wiki-audio cost --all
cleaned chars across 53 essays: 880,344
flash_v2_5  (0.5 credits/char): 440,172 credits → fits Pro $99/mo
multilingual_v2 (1 credit/char): 880,344 credits → Scale $330/mo (1 month) or Pro $99 × 2
```

### Project layout (the wiki-audio repo)

```
wiki-audio/                            # github.com/Jacob2017/wiki-audio
├── README.md
├── LICENSE
├── go.mod                             # module github.com/Jacob2017/wiki-audio
├── go.sum
├── install.sh                         # the curl|bash installer
├── cmd/
│   └── wiki-audio/
│       └── main.go                    # entrypoint; wires Cobra commands
├── internal/
│   ├── cli/                           # Cobra command definitions
│   ├── config/                        # config.toml + .env loading
│   ├── model/                         # data structs (§2)
│   ├── extract/                       # markdown → CleanedDocument
│   ├── chunk/                         # paragraph-bounded chunking
│   ├── tts/                           # ElevenLabs client
│   ├── concat/                        # ffmpeg subprocess wrapper
│   ├── id3/                           # tag MP3 metadata
│   ├── manifest/                      # load/save manifest from R2
│   ├── r2/                            # S3-compatible client (minio-go)
│   └── feed/                          # RSS XML generation
├── worker/                            # Cloudflare Worker (TypeScript)
│   ├── wrangler.jsonc
│   ├── package.json
│   └── src/index.ts
├── .github/
│   └── workflows/
│       └── release.yml                # goreleaser on tag push
└── .goreleaser.yaml                   # cross-compile + release config
```

---

## 4. Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         build pipeline (local)                           │
│                                                                          │
│   raw/processed/paul-graham/*.md                                         │
│            │                                                             │
│            ▼                                                             │
│   ┌────────────────┐                                                     │
│   │  Extractor     │  drop "# Title", "## Metadata", "## Full Document"  │
│   │                │  strip [[wikilinks]] → display, image refs,         │
│   │                │  fenced code blocks, footnote markers               │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│      CleanedDocument(body, body_hash)                                    │
│           │                                                              │
│           ▼                                                              │
│   ┌────────────────┐  GET pg.manifest.json from R2 once at start of run │
│   │  Manifest      │   hash hit?  ─yes──▶ skip                           │
│   │  cache check   │                                                     │
│   └───────┬────────┘   hash miss                                         │
│           ▼                                                              │
│   ┌────────────────┐                                                     │
│   │  Chunker       │  paragraph-bounded segmentation, ≤4000 chars        │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│   []AudioChunk                                                           │
│           │                                                              │
│           ▼                                                              │
│   ┌────────────────┐    HTTPS    ┌──────────────────┐                    │
│   │  TTS client    │ ──────────▶ │  ElevenLabs API  │                    │
│   │  (per-chunk)   │ ◀────────── │  flash_v2_5      │                    │
│   └───────┬────────┘   mp3 bytes └──────────────────┘                    │
│           ▼                                                              │
│   ┌────────────────┐                                                     │
│   │  Concat (ffm.) │  50ms crossfade between chunks                      │
│   │  + ID3 tagger  │  title, artist, album, date                         │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│   ~/.cache/wiki-audio/out/<slug>.mp3                                     │
│           │                                                              │
│           ▼                                                              │
│   in-memory Manifest updated with ManifestEntry                         │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                       publish pipeline (local → R2)                      │
│                                                                          │
│   in-memory Manifest (built during build phase)                          │
│       │                                                                  │
│       ▼                                                                  │
│   ┌────────────────┐                                                     │
│   │  R2 lister     │  HEAD each pg/<slug>.mp3, compare etag              │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│   set(new ∪ changed)                                                     │
│           │                                                              │
│           ▼                                                              │
│   ┌────────────────┐  minio-go   ┌──────────────────┐                    │
│   │  Uploader      │ ──────────▶ │ Cloudflare R2    │                    │
│   │  (S3 API)      │             │ bucket wiki-audio│                    │
│   └───────┬────────┘             └──────────────────┘                    │
│           ▼                                                              │
│   ┌────────────────┐                                                     │
│   │  RSS builder   │  emit pg.xml with iTunes namespace, one             │
│   │                │  <item> per ManifestEntry                           │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│   upload pg.xml + pg.manifest.json to R2 root                            │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                       access path (phone → audio)                        │
│                                                                          │
│   Pocket Casts                                                           │
│       │                                                                  │
│       │ GET https://audio.<domain>/pg.xml?t=<token>                      │
│       ▼                                                                  │
│   ┌────────────────┐                                                     │
│   │ Cloudflare     │  validates ?t= against TOKEN env var (constant-time)│
│   │ Worker         │  if mismatch → 403; if match → fetch from R2        │
│   │ (R2 binding)   │  binding (no public bucket access required)         │
│   └───────┬────────┘                                                     │
│           ▼                                                              │
│       streamed MP3 / pg.xml bytes                                        │
│                                                                          │
│   episode <enclosure> URLs in the feed all carry the same ?t=<token>,    │
│   so the podcast app passes it through on each download automatically.   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 5. Algorithms

### 5.1 Extraction (`internal/extract/`)

Input: a raw `.md` file. Output: `CleanedDocument`.

```
1. Read file as UTF-8 string.
2. title = first line matching `^# (.+)$`.
3. Drop everything from "## Metadata" to the line before "## Full Document".
4. Drop the "## Full Document" header line itself.
5. The rest is `raw_body`. Split it at the "**Notes**" or "## Notes" heading
   (case-insensitive; whichever appears first, near end of essay):
   - prose_part   = everything before the heading
   - notes_part   = everything after (may be empty if essay has no footnotes)
6. Parse notes_part into a footnote_map: dict[int, str].
   Each note starts with "[N] " on its own line; body continues until the
   next "[M] " line or end of notes_part. Strip leading/trailing whitespace.
   Drop any malformed entries silently.
7. On prose_part, in order:
   a. Remove ![alt](url) image refs entirely.
   b. Replace [text](url) → text.
   c. Replace [[slug|display]] → display; [[slug]] → slug.replace('-', ' ').
   d. Strip fenced code blocks ```...```; record count in skipped_segments.
8. Paragraph-attributed footnote weaving:
   a. Split prose_part into paragraphs by `\n\s*\n`.
   b. For each paragraph:
      i.   Find all `[N]` markers (where N matches a key in footnote_map).
           Use a regex that does NOT match markdown link openers (those
           were already replaced in step 7b, so any remaining [N] is a
           footnote marker).
      ii.  Strip the markers from the paragraph prose.
      iii. If markers found, append after the paragraph (separated by a
           blank line):
             "Footnote {N}: {footnote_map[N]}"
           — one line per marker, in order of appearance.
           Markers referenced multiple times in one paragraph emit once.
9. Reassemble paragraphs with blank-line separators. Collapse runs of
   blank lines to a single blank line.
10. Leave paragraph boundaries intact — the chunker reflows on `\n\n`,
    not sentences. (Go has no `pysbd`-grade sentence segmenter; paragraph-
    bounded chunking avoids the need and is sufficient for PG-length prose.)
11. body_hash = sha256(body + voice_id + model_id + footnote_policy_version).
12. Return CleanedDocument.
```

**Why paragraph-end (not essay-end) footnotes.** User preference: hearing the footnote within the same listening "beat" as the text it annotates preserves context, but interrupting mid-sentence breaks flow. Paragraph boundaries are the natural seam — the listener has just finished a thought, the footnote elaborates, then the next paragraph begins.

**Why include `footnote_policy_version` in the hash.** If the algorithm changes (e.g. switch to inline asides), we want every essay to regenerate automatically rather than serve stale audio mixed with the new style.

**Why drop code blocks rather than describe.** PG's code is often the *point* of the paragraph (*Beating the Averages*, *Revenge of the Nerds*) but reads atrociously aloud. Better to skip cleanly than mangle. The user can read those essays at desk.

**Edge cases the extractor handles:**

| Input pattern | Handling |
|---|---|
| Essay with no Notes section | `footnote_map = {}`; step 8 is a no-op. |
| Marker `[N]` in prose with no matching note in `footnote_map` | Strip marker silently; don't emit a "Footnote N:" line. Log at debug level. |
| Note defined but never referenced in prose | Dropped (won't be read aloud). Log at info level. |
| Paragraph references the same `[N]` twice | Emit the footnote once at paragraph end. |
| Markdown link that survived step 7b due to malformed syntax | If a `[N]` is preceded immediately by `]` or followed by `(`, treat as link debris and skip. |
| `**Notes**` heading absent but footnote markers exist | Treat whole body as prose; markers stripped silently (no defs to weave). |

### 5.2 Chunking (`internal/chunk/`)

```
Input:  body (string), maxChars (int = 4000)
Output: []AudioChunk

1. paragraphs = strings.Split(body, "\n\n")
2. var chunks []string; var cur strings.Builder
3. For each paragraph p in paragraphs:
   if cur.Len() + len(p) + 2 > maxChars and cur.Len() > 0:
       chunks = append(chunks, strings.TrimSpace(cur.String()))
       cur.Reset()
   cur.WriteString(p); cur.WriteString("\n\n")
4. if cur.Len() > 0: chunks = append(chunks, strings.TrimSpace(cur.String()))
5. Edge case: if any paragraph itself > maxChars, fall back to sentence-style
   split on regex `[.!?]\s+(?=[A-Z])` for that paragraph only. Log a warning;
   PG essays rarely trip this.
6. Return []AudioChunk{Index:i, Text:c, CharCount:len(c)} for i,c.
```

**Why paragraph-bounded (not sentence-bounded).** Mid-sentence breaks would produce audible glitches in concatenated audio. Paragraph boundaries are natural pause points that the listener already expects, and avoid needing a Go equivalent of `pysbd` (no library at that quality exists). The fallback in step 5 handles the rare overlong paragraph.

### 5.3 TTS dispatch (`internal/tts/`)

```go
// pseudocode; real implementation in internal/tts/elevenlabs.go
for _, chunk := range chunks {
    for attempt := 0; attempt < cfg.RetryAttempts; attempt++ {
        resp, err := http.Post(
            fmt.Sprintf("%s/v1/text-to-speech/%s", baseURL, voiceID),
            jsonBody{Text: chunk.Text, ModelID: cfg.ModelID, OutputFormat: cfg.OutputFormat},
            withHeader("xi-api-key", apiKey),
            withTimeout(cfg.RequestTimeoutS),
        )
        switch {
        case err != nil && isTimeout(err):
            time.Sleep(backoff(attempt)); continue
        case resp.StatusCode == 200:
            return resp.Body, nil  // streamed mp3
        case resp.StatusCode == 429:
            time.Sleep(parseRetryAfter(resp) | backoff(attempt)); continue
        case resp.StatusCode == 402 || resp.StatusCode == 403:
            return nil, FatalTTSError{...}  // out of credits / auth
        case resp.StatusCode >= 500:
            time.Sleep(backoff(attempt)); continue
        }
    }
    return nil, ChunkSynthesisError{Index: chunk.Index}
}

func backoff(attempt int) time.Duration {
    base := cfg.RetryBackoffBase * math.Pow(2, float64(attempt))
    jitter := rand.Float64()  // 0..1
    return time.Duration((base + jitter) * float64(time.Second))
}
```

### 5.4 Concatenation (`internal/concat/`)

`ffmpeg -i in0.mp3 -i in1.mp3 ... -filter_complex "acrossfade=d=0.05" out.mp3`

In practice, run pairwise `acrossfade` in a loop because ffmpeg's filter syntax doesn't chain crossfades cleanly across N inputs.

### 5.5 RSS generation (`internal/feed/`)

Use Go stdlib `encoding/xml` with custom struct tags. The iTunes namespace requires hand-crafted `xml:"itunes:author"` tags — a small one-time investment. No external podcast library; the surface is small enough to own.

iTunes-namespaced RSS 2.0. Per `ManifestEntry`, emit:

```xml
<item>
  <title>How to Do Great Work</title>
  <enclosure url="https://wiki-audio.example.workers.dev/pg/how-to-do-great-work.mp3?t=<token>"
             length="65437184" type="audio/mpeg" />
  <guid isPermaLink="false">pg-how-to-do-great-work</guid>
  <pubDate>Thu, 08 May 2026 14:21:03 GMT</pubDate>
  <itunes:author>Paul Graham</itunes:author>
  <itunes:duration>4924</itunes:duration>
  <itunes:explicit>false</itunes:explicit>
  <description>Originally published July 2023.</description>
</item>
```

**`pubDate` policy.** Use `published_at` from the manifest, not the essay's original publish date — this controls episode ordering in podcast apps. Newest publish at the top of the inbox = "just generated" appears first, which matches the listening flow.

**`guid` policy.** Stable across regenerations (`pg-<slug>`). Changing the guid causes podcast apps to re-download.

---

## 6. Error handling

| Failure mode | Detection | Recovery |
|---|---|---|
| ElevenLabs request timeout (60s) | `context.DeadlineExceeded` from http.Client with timeout | Exponential backoff, 3 retries. After final failure, abort that essay only; partial chunks discarded; R2 manifest unchanged so next run retries cleanly. |
| Rate limit (HTTP 429) | response status | Honor `Retry-After` header if present, else backoff and retry. |
| Out of credits / auth failure (HTTP 402, 403) | response status | `FatalTTSError` aborts the whole run. R2 manifest preserves successful entries from earlier essays — resumable. Surface the API error body to the user. |
| Server error (HTTP 5xx) | response status | Retry with backoff. |
| Malformed essay (cleaned body < 200 chars) | post-extraction length check | Skip with warning, append slug to local `~/.cache/wiki-audio/skipped.txt`. Don't fail the run. |
| Extractor regression (e.g. accidentally drops body) | dry-run diff: char_count drops >50% from previous manifest entry | Build aborts with diagnostic. User reviews extraction output (`--extract-only`) before allowing run. `--force-regression` overrides. |
| ffmpeg concatenation failure | non-zero exit code from `os/exec` | Preserve raw chunk MP3s under `~/.cache/wiki-audio/tmp/<slug>/` for debugging. Skip that essay; manifest unchanged. |
| Manifest JSON corruption | `json.Unmarshal` error | R2 keeps the previous manifest at `pg.manifest.json.bak` (rotated before each save). If both corrupt, abort with instruction to manually inspect. Never silently overwrite. |
| Disk full during write | `os.WriteFile` returns `syscall.ENOSPC` | Atomic local writes via `os.CreateTemp` + `os.Rename` ensure partial files don't replace good ones. |
| R2 upload failure | minio-go error | Retry 3x. If still failing, leave `R2Key=""` on the manifest entry, surfaces in next `publish` run. Local cache MP3 retained — no rebuild needed. |
| R2 listing failure during diff | error from ListObjects | Abort publish; nothing uploaded; safe to retry. |
| Stale R2 objects (essay deleted from raw/) | slug present on R2 but absent in manifest | `wiki-audio publish --prune` removes them. Default off — silent prune is dangerous. |
| Network outage mid-bulk-run | repeated timeouts | Manifest is uploaded after each essay completion. Ctrl-C → re-run resumes from where it left off. |
| API key in shell history | n/a | `~/.wiki-audio/.env` (chmod 600), loaded via `godotenv`. Never logged. Tool refuses to start if .env is world-readable. |
| API key checked into git | pre-commit hook | `.env` lives outside any git repo by default (`~/.wiki-audio/.env`). The wiki-audio repo's `.gitignore` excludes `.env` defensively; `gitleaks` config in CI catches accidental commits. |
| Access token leak (e.g. screenshot of feed URL) | manual / suspicion | Rotate token: regen, `wrangler secret put ACCESS_TOKEN`, update local .env, run `wiki-audio publish --feed-only`, re-paste new feed URL into Pocket Casts. Old links 403 immediately. No object renames needed because token is in query string, not path. |
| Cloudflare Worker outage | Worker error / 5xx from podcast app | R2 + Worker high availability is good but not 100%. Acceptable — podcast apps retry. No escalation path needed for v1. |
| Bucket accidentally made public | manual review at Phase A gate | Phase A explicitly checks bare URL returns 403. Add yearly reminder via `/schedule` to re-verify. |
| Config file missing or invalid | `wiki-audio doctor` | Tool refuses to run any command other than `init` or `doctor` if config invalid. Clear error pointing at the offending field. |
| Tool version mismatch with manifest schema | `Version` field on Manifest | Older tool refuses to overwrite a newer-schema manifest. User instructed to upgrade via the install script. |
| Install script fails on unknown platform | `uname -s`/`-m` not in supported list | Print supported list, instruct user to download manually from Releases. |
| ElevenLabs ToS change | manual review | One-time check before subscription; revisit annually. |
| Chunk audio length anomaly | duration < 1s for non-empty chunk | Log warning, but accept (some chunks are pure metadata-stripped fragments). |

---

## 7. Implementation roadmap

**Phase A — already done (Worker + R2 infra).**
- ✅ R2 bucket `wiki-audio` created (no public access, no dev URL).
- ✅ Access token generated, stored as Worker secret `ACCESS_TOKEN` and (locally) `WIKI_AUDIO_ACCESS_TOKEN`.
- ✅ Cloudflare Worker deployed at `https://wiki-audio.example.workers.dev`; constant-time token gate verified (bare URL → 403, wrong token → 403, correct token + missing key → 404).

> The Worker code currently lives at `tools/wiki-audio/worker/`. **Migration step:** when the new repo is created (Phase B), move `tools/wiki-audio/worker/` into the new repo and delete the directory from the wiki repo.

**Phase B — scaffold the wiki-audio Go repo (half-day).**
- `gh repo create Jacob2017/wiki-audio --private --clone` on dev machine.
- `go mod init github.com/Jacob2017/wiki-audio`.
- Add deps: `cobra`, `BurntSushi/toml`, `minio-go/v7`, `bogem/id3v2/v2`, `joho/godotenv`.
- Write skeleton: `cmd/wiki-audio/main.go` wiring Cobra, empty `internal/` packages.
- Move Worker code from `tools/wiki-audio/worker/` into `worker/` of the new repo.
- **Gate:** `go build ./...` succeeds; `wiki-audio --help` prints all subcommands; `cd worker && wrangler deploy` still deploys the Worker.

**Phase C — release plumbing (2-3 hrs).** Depends on B.
- `.goreleaser.yaml`: cross-compile for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. Output tarballs + sha256sums.
- `.github/workflows/release.yml`: trigger goreleaser on `v*` tag push.
- `install.sh` in repo root: detects `uname -s` / `uname -m`, fetches latest (or pinned) tarball from GitHub Releases, verifies sha256, installs to `/usr/local/bin` (or `~/.local/bin` if non-root).
- Cut `v0.0.1` (empty stub binary). Verify install on the wiki box: `curl -fsSL .../install.sh | bash` → `wiki-audio --help` works.
- **Gate:** install script exits cleanly on a fresh shell; `wiki-audio --version` prints `v0.0.1`.

**Phase D — extractor + dry-run (half-day).** Depends on B.
- `internal/extract/`: implement §5.1 step-by-step.
- `internal/chunk/`: implement §5.2.
- `internal/config/`: load TOML + .env.
- CLI: `wiki-audio init`, `wiki-audio doctor`, `wiki-audio build --dry-run`, `wiki-audio build --extract-only`.
- Cut `v0.1.0`, install on wiki box.
- **Gate:** `wiki-audio build --extract-only --slug beating-the-averages` prints clean text with code blocks dropped, footnotes appended at paragraph ends, no formatting debris. User-reviewed.

**Phase E — TTS + spike (1 hr after D).** Depends on D, Phase A.
- `internal/tts/` + `internal/concat/` + `internal/id3/`.
- Subscribe to ElevenLabs Pro for one month.
- Run `wiki-audio build --slug how-to-do-great-work` end to end. Upload manually to R2 (via aws-cli or rclone) for now.
- Listen on phone via the Worker URL with token query.
- **Gate:** voice quality acceptable across an hour-long essay; ID3 tags display correctly in Pocket Casts.

**Phase F — R2 publish + RSS (half-day).** Depends on E.
- `internal/r2/` (minio-go): manifest get/put, MP3 put, list-and-diff.
- `internal/feed/`: RSS XML generation.
- CLI: `wiki-audio publish`, `wiki-audio publish --feed-only`, `wiki-audio publish --dry-run`.
- Cut `v1.0.0`. Re-install on wiki box.
- **Gate:** `wiki-audio publish` after the spike yields a feed validating in `castfeedvalidator.com`; Pocket Casts subscribes successfully.

**Phase G — bulk run.** Depends on F.
- `wiki-audio build && wiki-audio publish` against all 53 essays.
- Spot-listen 3-5 random essays end-to-end on phone.
- **Gate:** zero malformed extractions; total cost within estimate; all 53 episodes appear in Pocket Casts inbox.

**Phase H — operationalize.** Depends on G.
- Cancel ElevenLabs Pro, switch to pay-as-you-go top-ups.
- Document the workflow in `wiki-audio/README.md`: install line, `init` + `doctor`, daily ergonomics for adding new essays.
- **Gate:** new essay added to PG raw folder → `wiki-audio build && wiki-audio publish` produces a new podcast episode end-to-end without manual steps.

**Phase I (deferred — v2 expansion).**
- `audio: true` opt-in flag on summary/insight pages.
- Source→raw resolution for sources with raw originals.
- Optional second feed `insights.xml`.
- Per-page voice override (`audio_voice: hq`).

---

## 8. Comparison tables

### 8.1 TTS engine

| Engine | Quality | Cost (880k chars) | Setup | Verdict for v1 |
|---|---|---|---|---|
| **ElevenLabs Flash v2.5** | Very natural; minor prosody issues on technical prose | ~$99 (Pro 1 month) | API key only | **Chosen** — best quality/cost for long-form essay listening |
| ElevenLabs Multilingual v2 | Best-in-class | ~$198–$330 | API key only | Reserve for v2 high-priority pages via override |
| OpenAI tts-1 | Natural, slightly flat | ~$13 | API key only | Cheaper, but user explicitly chose ElevenLabs for voice quality |
| OpenAI tts-1-hd | Natural, marginally better than tts-1 | ~$26 | API key only | Same as above |
| Google Neural2 / Azure Neural | Comparable to OpenAI tts-1 | ~$14 | GCP/Azure billing | Skip — extra setup, no quality win |
| Kokoro (local) | Surprisingly good, slight robotic edge | $0 | Local model + GPU recommended | Skip for v1 — quality gap matters at hours of listening |
| Piper (local) | Good for short clips, fatiguing over hours | $0 | Local model | Skip |

### 8.2 Delivery / hosting

| Approach | Phone UX | Setup | Cost | Verdict |
|---|---|---|---|---|
| **Cloudflare R2 + RSS feed** | Native podcast app: queue, speed, resume, offline | One bucket + DNS | ~$0.01/mo | **Chosen** — best UX, no egress fees |
| AWS S3 + RSS feed | Same as R2 | One bucket + DNS | ~$0.05/mo + egress fees per listen | Egress fees scale with listening; avoid |
| Backblaze B2 + RSS | Same as R2 | Bucket + Cloudflare in front for free egress | ~$0.01/mo | Equivalent to R2; pick whichever is more familiar |
| Audiobookshelf self-hosted | Excellent dedicated audiobook UI | VPS + reverse proxy + auth | $5/mo VPS + maintenance time | Overkill for a one-author feed; revisit if library grows past ~500 hours |
| Syncthing folder → phone | Plays in VLC; no podcast UX | Syncthing on both ends | $0 | Loses queueing/streaming; only good if R2 is unavailable |
| Plex / Jellyfin | OK with Prologue iOS app | Server + library config | $5/mo VPS or local NAS | Heavy infrastructure for the use case |

### 8.3 Opt-in mechanism (for v2)

| Mechanism | Discoverability | Survives renames | Per-file granularity | Verdict |
|---|---|---|---|---|
| **`audio: true` in YAML frontmatter** | grep-able | Yes | Yes | **Chosen for v2** |
| Tag-based (`#audio`) | grep-able | Yes | Yes | Equivalent; user already uses `tags:` array in frontmatter, so YAML key is more idiomatic |
| Folder-based (`audio/` subfolder) | Visual | No (moves break links) | Coarse | Worse — Obsidian links rot on move |
| Central registry file | Single source of truth | Manual | Yes | Drifts from reality; rejected |
| Path glob in config | Easy bulk include | No | Coarse | Used implicitly for v1 (whole PG folder); not opt-in |

### 8.4 Voice model trade-off within ElevenLabs

| Model | Credits / char | Quality | Latency | When to use |
|---|---|---|---|---|
| `eleven_flash_v2_5` | 0.5 | Very good | Low (~150ms TTFB) | **Default for v1** — bulk corpus |
| `eleven_turbo_v2_5` | 0.5 | Good, slightly less expressive than Flash | Lowest | Real-time use cases; not needed here |
| `eleven_multilingual_v2` | 1.0 | Best | Higher | v2 override for ~10 most-relistened essays |
| `eleven_monolingual_v1` | 1.0 | Good but older | Higher | Skip — superseded |

### 8.5 Implementation language

| Option | Binary distribution | Runtime deps on target | Cost to ship | Verdict |
|---|---|---|---|---|
| **Go** | Single static binary, ~12 MB; cross-compile with `go build` or `goreleaser` | None (just ffmpeg for concat, which is independent of language choice) | Moderate; some Go-flavoured lib churn (ID3, RSS) but stdlib carries most | **Chosen** — matches the beads_rust install ergonomics |
| Python (pipx / uv tool install) | Source distribution; user needs Python 3.11+ on target | Python toolchain + virtualenv per CLI | Lowest — richest libs (`pysbd`, `pydantic`, `feedgen`) | Skipped — fights "single curl line" goal; install requires `uv` or `pipx` bootstrap |
| Python (PyInstaller) | "Single binary" but really a bundled interpreter, ~30-50 MB; slow startup | None | Moderate; PyInstaller is fragile with C extensions | Skipped — worst of both worlds: heavy binary, brittle build |
| Rust | Single static binary, ~5-8 MB; cross-compile via cross/cargo-zigbuild | None | Highest — async ecosystem, audio/markdown libs less mature than Python | Skipped — quality bar of beads_rust at the cost of doubling project effort; reconsider if Go runs into limits |
| Node.js + pkg/SEA | Bundled binary, ~40-50 MB; slow startup | None | Moderate; ecosystem strong but binary story messy | Skipped — distribution worse than Go for similar effort |

### 8.6 Go-specific dependencies

| Need | Library | Reason |
|---|---|---|
| **CLI framework** | `github.com/spf13/cobra` | De facto Go standard for nested subcommands; rich help output; mature |
| **TOML config** | `github.com/BurntSushi/toml` | Stable, struct-tag-driven, no surprises |
| **`.env` loading** | `github.com/joho/godotenv` | Tiny, does exactly one thing |
| **R2 / S3 client** | `github.com/minio/minio-go/v7` | ~3 MB compiled vs ~30 MB for `aws-sdk-go-v2`; full S3 compatibility, works with R2 |
| **ID3 tagging** | `github.com/bogem/id3v2/v2` | Stable, simple API for ID3v2.3/v2.4 frames |
| **HTTP** | `net/http` (stdlib) | Sufficient; no need for resty/req for one external API |
| **RSS XML** | `encoding/xml` (stdlib) + custom struct tags | iTunes namespace is small enough to own; avoids external podcast lib |
| **Hashing** | `crypto/sha256` (stdlib) | — |
| **Logging** | `log/slog` (stdlib, Go 1.21+) | Structured, leveled; good enough |
| **Cross-compile + release** | `goreleaser` (CI tool) | Industry-standard for Go binary releases on GitHub |

### 8.7 Audio format

| Format | Bitrate | Size for 880k chars (~19h) | Quality for spoken voice | Verdict |
|---|---|---|---|---|
| **MP3 64 kbps mono** | 64 kbps | ~520 MB | Indistinguishable from higher bitrates for voice | **Chosen** — universal podcast support |
| MP3 128 kbps mono | 128 kbps | ~1.0 GB | Same | Wasted bytes |
| Opus 32 kbps | 32 kbps | ~260 MB | Excellent | Apple Podcasts has historically poor Opus support; avoid |
| AAC 64 kbps | 64 kbps | ~520 MB | Slightly better than MP3 | Universal but no real win over MP3 |

---

## 9. Privacy & access control

**Threat model.** The feed and episodes carry personal-use TTS audio of third-party copyrighted essays. Acceptable risk: someone who already knows you and can guess a domain might find the feed. Unacceptable risk: the feed appears in Cloudflare's public dev URL listings, gets indexed by search engines, or is enumerable from R2 bucket listings.

**Mechanism.** Cloudflare Worker fronting a non-public R2 bucket; Worker validates a static random token passed as `?t=<token>`. Bucket has no public access enabled and no public dev URL.

### 9.1 Token policy

- Generated once: 43 chars of `[A-Za-z0-9_-]`. Either via `wiki-audio init` (Go binary uses `crypto/rand` to produce 32 random bytes, base64-url-encoded), or out-of-band: `openssl rand -base64 32 | tr '+/' '-_' | tr -d '='`.
- Stored in Cloudflare Worker as a Secret (env var `ACCESS_TOKEN`), in `~/.wiki-audio/.env` as `WIKI_AUDIO_ACCESS_TOKEN`, and in 1Password.
- Embedded into every URL in the feed at generation time (`internal/feed` reads the env var and appends `?t=<token>` to the feed self-link and every `<enclosure>` URL).
- Compared in the Worker with a constant-time string compare to defeat timing oracles.
- Rotation: regenerate, update Worker secret + `~/.wiki-audio/.env`, run `wiki-audio publish --feed-only`, re-paste the new feed URL into Pocket Casts. Old token immediately invalidated.

### 9.2 Worker (sketch)

`worker/src/index.ts` in the wiki-audio repo (~30 lines):

```typescript
export interface Env {
  BUCKET: R2Bucket;
  ACCESS_TOKEN: string;
}

function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let out = 0;
  for (let i = 0; i < a.length; i++) out |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return out === 0;
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    const token = url.searchParams.get("t") ?? "";
    if (!timingSafeEqual(token, env.ACCESS_TOKEN)) {
      return new Response("forbidden", { status: 403 });
    }
    const key = url.pathname.replace(/^\/+/, "");
    if (!key) return new Response("not found", { status: 404 });

    const obj = await env.BUCKET.get(key);
    if (!obj) return new Response("not found", { status: 404 });

    const headers = new Headers();
    obj.writeHttpMetadata(headers);
    headers.set("etag", obj.httpEtag);
    headers.set("cache-control", "private, max-age=3600");
    return new Response(obj.body, { headers });
  },
};
```

### 9.3 Why this and not alternatives

| Mechanism | Privacy strength | Podcast app compat | Setup | Verdict |
|---|---|---|---|---|
| **Worker + static query-string token** | Strong; revocable; URLs unguessable; bucket fully private | All major apps pass `?t=` through unchanged on enclosure fetches | ~30 lines TS + 1 secret | **Chosen** |
| Long random URL path (obscurity only, public bucket) | Weak; URL leaks via referer / app logs / screenshots are unrecoverable without renaming objects | All apps | Zero code | Skipped — rotation requires re-keying every object |
| HTTP Basic Auth via Worker | Strong; revocable | Pocket Casts ✓, Apple Podcasts ✓, Overcast ✓ historically; Spotify ✗ | ~20 lines TS | Equivalent to chosen; query-string token slightly easier to test in browser |
| S3-style pre-signed URLs (time-limited) | Strong while live; expires | Apps cache feed XML for hours; expired enclosure URLs cause download failures and "episode unavailable" errors | Moderate | Skipped — operationally fragile |
| Cloudflare Access (zero-trust SSO) | Strongest | Most podcast apps cannot complete OAuth | High | Skipped — incompatible with podcast apps |
| Public bucket + RSS in private location | Episodes still enumerable if URL guessed | All apps | Zero code | Skipped — false sense of privacy |

### 9.4 What the plan does NOT defend against

- **You sharing the feed URL.** Anyone with the token can listen until you rotate.
- **Cloudflare itself.** Cloudflare can read the bucket. Acceptable for personal-use TTS of public essays; not acceptable for genuinely sensitive material.
- **Targeted attacker who has compromised your laptop.** Out of scope; if they have the env file they have the token, the API key, and your wiki.
- **Pocket Casts logging.** Pocket Casts servers see enclosure URLs (token included) when they download episodes for you. Acceptable risk; if not, switch to a strictly-local podcast app like Doughnut or AntennaPod that fetches directly from the device.

---

## 10. Decisions

| Question | Decision | Locus |
|---|---|---|
| Voice | **Christopher** (`G17SuINrv2H9FC6nvetn`) — British male narrator from ElevenLabs voice library. Baked into `feed.toml`. | §2 `TTSConfig`, `feed.toml` |
| Footnote handling | Read at end of containing paragraph as "Footnote N: …" lines. Defs parsed from `**Notes**` section; markers stripped from prose; orphan markers dropped silently. | §5.1 step 8 |
| Cover art | Skipped for v1; default placeholder is acceptable. Add later without regenerating audio (feed-only republish). | — |
| Multiple voices | Single voice across all 53 essays. Revisit only if listening fatigue is felt. | §2 `TTSConfig`, single `voice_id` |
| ElevenLabs ToS / PG copyright | Risk accepted by user; personal-use, no redistribution. Not revisited. | — |
| Implementation language | **Go** — single static binary, cross-compile, zero runtime deps on target. Trade-off vs Python (richer libs, esp. `pysbd`) accepted; PG prose is uniform enough that paragraph-bounded chunking is sufficient. | §8.5, §8.6, all `internal/` packages |
| Tool repo location | New private GitHub repo `Jacob2017/wiki-audio` — separate from the wiki repo. Worker code migrates from `tools/wiki-audio/worker/` into `wiki-audio/worker/`. Wiki repo retains nothing about the tool's source. | §1, §7 Phase B |
| Distribution / install | `curl -fsSL https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh \| bash` — auto-detects platform, downloads tarball from GitHub Releases (built by goreleaser on tag push), verifies sha256, drops binary at `/usr/local/bin/wiki-audio`. Pin version with `bash -s -- v0.3.0`. | §3, §7 Phase C |
| Config location | `~/.wiki-audio/config.toml` (non-secret) and `~/.wiki-audio/.env` (secrets, chmod 600). Outside any git repo by default. | §2, §6 |
| State (manifest) location | R2 object `pg.manifest.json` — wiki box is stateless. Re-installs and machine swaps don't lose track of what's published. | §1, §6 |
