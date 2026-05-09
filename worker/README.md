# wiki-audio Cloudflare Worker

This Worker fronts the private R2 bucket `wiki-audio` and gates all reads behind a static query-string token. It is the only network path to read audio enclosures and the RSS feed; the bucket itself has zero public access. See §9 of `PLAN_FOR_AUDIO_LIBRARY.md` for the threat model.

> **Where files live (cross-reference).** The Worker only sees R2 keys (`pg/<slug>.mp3`, `pg.xml`, `pg.manifest.json`, `pg.manifest.json.bak`). The CLI side maintains two local directories — `~/.wiki-audio/` for config + secrets and `~/.cache/wiki-audio/` for regeneratable build output (`tmp/`, `out/`, `skipped.txt`). See the top-level [`README.md`](../README.md#where-things-live) for the complete map. Cache contents never reach the Worker; the publish step uploads `~/.cache/wiki-audio/out/<slug>.mp3` directly to R2.

## Architecture

R2 binding `BUCKET` → `wiki-audio` bucket. Static token compared in constant time to the runtime secret `ACCESS_TOKEN`. Token check happens BEFORE key parsing, so authentication-failure responses cannot leak any information about which keys exist. No public Workers.dev surface beyond the account-default subdomain; no public R2 dev URL; no wide-open routes. See §9.

## The 403/403/404 contract

1. Bare URL (no `?t=`)         → expect HTTP 403
2. Wrong token (`?t=garbage`)  → expect HTTP 403
3. Correct token + missing key → expect HTTP 404

- **403 (forbidden)** for ANY token failure (missing OR wrong). One status, one body. An attacker MUST NOT be able to distinguish "valid-looking but wrong" from "absent" — that's a token format oracle.
- **404 (not found)** ONLY after token validation passes. Tells the authorized user that a key is missing without exposing R2 listing semantics to anyone unauthenticated.
- The Worker returns 403 with body `"forbidden"` and 404 with body `"not found"` — fixed strings, identical between requests. No details about what was wrong.

## Why constant-time compare

A naive `===` short-circuits on the first mismatched character; an attacker measuring response latency could recover the token byte-by-byte. The OR-fold pattern in `src/index.ts` always runs the loop to completion regardless of input, so per-byte timing carries no signal. The length check at the top is fine to short-circuit — token length (43) is a public constant.

If you are tempted to replace the loop with `===`: stop and re-read §9.2. The 403/403/404 contract above is the regression signal but it does not catch a timing leak directly; the loop is the structural defense.

## Deploy

```
cd worker && wrangler deploy
```

Requires `CLOUDFLARE_API_TOKEN` (or `wrangler login`). Deploy updates the existing Worker `wiki-audio` because `wrangler.jsonc` names it explicitly — it does NOT fork a new Worker. After deploy, re-run the three contract checks above to confirm no regression.

## Secret rotation

The full procedure. Run these steps in order — deviation breaks the "old token immediately invalidated" property.

### 1. Generate a fresh token

```
NEW_TOKEN=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=')
echo -n "$NEW_TOKEN" | wc -c   # MUST print 43
```

The shape is `[A-Za-z0-9_-]{43}` (256 bits of entropy, base64url-encoded, no padding). Same shape as `wiki-audio init` produces from `crypto/rand`. See PLAN §9.1.

### 2. Push to Cloudflare

```
cd worker
set -a; . ~/.wiki-audio/.env; set +a    # for CLOUDFLARE_API_TOKEN
echo "$NEW_TOKEN" | wrangler secret put ACCESS_TOKEN
```

Wrangler reports `✨ Success! Uploaded secret ACCESS_TOKEN`. Within seconds the OLD token starts returning 403; the NEW token is live across all edge nodes.

### 3. Update local config

Replace the `WIKI_AUDIO_ACCESS_TOKEN=…` line in `~/.wiki-audio/.env`. Preserve every other secret (ELEVENLABS_API_KEY, R2 keys, etc.) verbatim — surgical edit only:

```
awk -v new="$NEW_TOKEN" '
  /^WIKI_AUDIO_ACCESS_TOKEN=/ { print "WIKI_AUDIO_ACCESS_TOKEN=" new; next }
  { print }
' ~/.wiki-audio/.env > ~/.wiki-audio/.env.new
chmod 600 ~/.wiki-audio/.env.new
mv ~/.wiki-audio/.env.new ~/.wiki-audio/.env
```

### 4. Mirror to 1Password

Update the "wiki-audio access token" entry. Keep the prior version in 1P history for ~30 days as a recovery anchor — if anything in the rotation goes wrong, the prior token is the rollback target.

### 5. Verify (all three MUST hold)

```
OLD=…   # the value before step 1
NEW=$NEW_TOKEN

# OLD token must now 403:
curl -sS -o /dev/null -w '%{http_code}\n' "https://<your-account>.workers.dev/pg.xml?t=$OLD"

# NEW token must work — 404 if pg.xml absent, 200 if present:
curl -sS -o /dev/null -w '%{http_code}\n' "https://<your-account>.workers.dev/pg.xml?t=$NEW"

# Bare URL still 403:
curl -sS -o /dev/null -w '%{http_code}\n' "https://<your-account>.workers.dev/pg.xml"
```

### 6. Republish the feed (only if Phase F is shipped)

`wiki-audio publish --feed-only` regenerates `pg.xml` with the NEW token in every `?t=` URL. Skip this step if no consumers exist yet (pre-Phase-F state — feed file isn't published to R2).

### 7. Re-subscribe in podcast app

Pocket Casts: unsubscribe from the old feed URL, subscribe to the new feed URL. The token is in the query string, so the URL itself changed. Do this AFTER step 6 — otherwise the app pulls a feed whose enclosure URLs don't yet match the new token.

### When to rotate

- After any suspected leak (URL screenshotted, accidental commit, suspicion of compromise).
- After laptop loss or any host compromise.
- Yearly is *unnecessary* — query-string tokens have no built-in expiry; rotate on event, not on calendar. (The yearly check on **wa-3ia.4** is for dashboard drift, not token rotation.)

### What NOT to do

- Don't reorder steps 2 and 3. If you update local config first, the next thing your shell does (e.g. `wiki-audio publish`) signs URLs with the new token while the Worker still accepts only the old one — you'll get 403s and think the deploy broke.
- Don't use `wrangler secret put --secret <value>` with the value on the command line. Pipe via stdin (`echo … | wrangler secret put ACCESS_TOKEN`) so the value never enters shell history.
- Don't reuse a recently-rotated token. 256 bits of fresh entropy each rotation; `openssl rand -base64 32` is cheap.

The full historical runbook with verification matrix lives on bead **wa-76r.4**.

## MUST-stay-private invariants

- No `workers_dev: true` flag. Once a custom route is configured, the workers.dev surface should be turned off entirely.
- R2 bucket `wiki-audio`: public access OFF, public dev URL OFF.
- Routes list MUST NOT contain `*` wildcards beyond the intended hostname.
- `ACCESS_TOKEN` is a runtime secret only — never declared in `wrangler.jsonc`, never committed, never logged.
- The Worker source MUST exactly match the canonical spec in bead wa-3ia.1; the OR-fold loop, length-check-first, token-before-key, and `cache-control: private` are load-bearing and not negotiable without a §9 re-read.

## §9.4 — what this does NOT defend against

- **You sharing the feed URL.** Mitigation: token rotation (wa-76r.4).
- **Cloudflare itself.** Acceptable for personal-use TTS of public essays; if the threat model ever changes, this whole design must be re-examined — don't bolt encryption onto it.
- **Compromised laptop.** Out of scope; defended by full-disk encryption, not by this tool.
- **Pocket Casts logging.** Their servers see enclosure URLs. Switch to AntennaPod / Doughnut if not acceptable.

These are EXPLICIT acceptances. Anyone proposing an upgrade must first show why one of the four has moved from "acceptable" to "unacceptable".

## Yearly bucket-privacy verification

Schedule reminder: bead **wa-3ia.4**. Once a year, walk the R2 dashboard to confirm public access is OFF, the public dev URL is OFF, and the custom-domains tab is empty (or only the Worker route). Cloudflare's dashboard UX has historically made it easy to flip dev URLs on by accident — the yearly check catches dashboard drift even when no one touched the bucket deliberately.
