# wiki-audio Cloudflare Worker

This Worker fronts the private R2 bucket `wiki-audio` and gates all reads behind a static query-string token. It is the only network path to read audio enclosures and the RSS feed; the bucket itself has zero public access. See §9 of `PLAN_FOR_AUDIO_LIBRARY.md` for the threat model.

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

Rotate the access token via:

```
cd worker && wrangler secret put ACCESS_TOKEN
```

Paste the new value at the prompt. Old token is invalidated within seconds (Worker propagation).

The full rotation runbook (covers `~/.config/wiki-audio/.env`, 1Password, `wiki-audio publish --feed-only`, and re-subscribing in Pocket Casts) lives on bead **wa-76r.4**. Don't rotate without following all five storage locations or you will leave a stale token alive somewhere.

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
