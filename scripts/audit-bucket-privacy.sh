#!/usr/bin/env bash
#
# audit-bucket-privacy.sh — wa-3ia.4 yearly bucket-privacy audit.
#
# Walks the §6 / wa-3ia.4 acceptance steps that can be automated:
#
#   2. bare URL                                 → expect 403
#   3. wrong-token URL                          → expect 403
#   4. correct token + missing key              → expect 404
#   5. correct token + real key                 → expect 200 (or 206 if Range)
#
# Step 1 (Cloudflare R2 dashboard inspection — Public access OFF, custom
# domains tab clean, Public Development URL OFF) is browser-only and cannot
# be automated by curl. It must be done by hand on the same day; the script
# prints the checklist when it finishes so an operator can paste the
# screenshot beside the curl results in the same audit log.
#
# Output: appends to ~/.cache/wiki-audio/audit-YYYY-MM-DD.txt (one file per
# year). On any unexpected status the script exits non-zero so a cron-driven
# harness fails loudly.
#
# Usage:
#
#   scripts/audit-bucket-privacy.sh
#   scripts/audit-bucket-privacy.sh --base-url https://wiki-audio.jabyrne.workers.dev
#   WIKI_AUDIO_ACCESS_TOKEN=$token scripts/audit-bucket-privacy.sh
#
# Sources WIKI_AUDIO_ACCESS_TOKEN from ~/.wiki-audio/.env when not already
# set in the environment. Refuses to run if the token is empty after that.
#
# Schedule from cron / systemd timer / `/schedule` slash command:
#   yearly on May 9 (token-rotation cadence in §6)
#   crontab: 0 9 9 5 * /path/to/scripts/audit-bucket-privacy.sh

set -euo pipefail

BASE_URL="${WIKI_AUDIO_BASE_URL:-https://wiki-audio.jabyrne.workers.dev}"
SAMPLE_KEY="${WIKI_AUDIO_SAMPLE_KEY:-pg/how-to-do-great-work.mp3}"
MISSING_KEY="${WIKI_AUDIO_MISSING_KEY:-does-not-exist.mp3}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url) BASE_URL="$2"; shift 2 ;;
    --sample-key) SAMPLE_KEY="$2"; shift 2 ;;
    --help|-h)
      sed -n '2,/^$/p' "$0" | sed 's/^#//; s/^ //'
      exit 0
      ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

# Source ~/.wiki-audio/.env if WIKI_AUDIO_ACCESS_TOKEN isn't already set.
if [[ -z "${WIKI_AUDIO_ACCESS_TOKEN:-}" ]]; then
  ENV_FILE="${HOME}/.wiki-audio/.env"
  if [[ -r "$ENV_FILE" ]]; then
    # shellcheck disable=SC2046
    export $(grep -E '^WIKI_AUDIO_ACCESS_TOKEN=' "$ENV_FILE" | xargs -d '\n' || true)
  fi
fi

if [[ -z "${WIKI_AUDIO_ACCESS_TOKEN:-}" ]]; then
  echo "audit: WIKI_AUDIO_ACCESS_TOKEN is empty; set it in env or ~/.wiki-audio/.env" >&2
  exit 2
fi

CACHE_DIR="${XDG_CACHE_HOME:-$HOME/.cache}/wiki-audio"
mkdir -p "$CACHE_DIR"
LOG="$CACHE_DIR/audit-$(date -u +%Y-%m-%d).txt"

probe() {
  # probe DESC URL EXPECTED   — appends one line, returns 0 on match.
  local desc="$1" url="$2" expected="$3"
  local got
  got=$(curl -sS -o /dev/null -w '%{http_code}' "$url" || true)
  printf '[%s] %-40s %s   got=%s expected=%s\n' \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$desc" "$url" "$got" "$expected" >> "$LOG"
  if [[ "$got" != "$expected" ]] && [[ ! ( "$expected" == "200" && "$got" == "206" ) ]]; then
    return 1
  fi
}

echo "audit-bucket-privacy: appending to $LOG" >&2
{
  echo "================================================================"
  echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] wa-3ia.4 yearly audit start"
  echo "BASE_URL=$BASE_URL"
  echo "SAMPLE_KEY=$SAMPLE_KEY"
} >> "$LOG"

failures=0

# Step 2: bare URL → 403
probe 'bare-no-token         ' "$BASE_URL/$SAMPLE_KEY" 403 || failures=$((failures+1))

# Step 3: wrong token → 403
probe 'wrong-token           ' "$BASE_URL/$SAMPLE_KEY?t=garbage" 403 || failures=$((failures+1))

# Step 4: correct token + missing key → 404
probe 'right-token+missing   ' "$BASE_URL/$MISSING_KEY?t=$WIKI_AUDIO_ACCESS_TOKEN" 404 || failures=$((failures+1))

# Step 5: correct token + real key → 200 (206 also accepted; the probe()
# helper carves out 200/206 equivalence for the live-bytes case).
probe 'right-token+real-key  ' "$BASE_URL/$SAMPLE_KEY?t=$WIKI_AUDIO_ACCESS_TOKEN" 200 || failures=$((failures+1))

{
  echo
  echo "REMINDER — Step 1 is browser-only. Open the Cloudflare R2 dashboard"
  echo "and confirm for bucket 'wiki-audio':"
  echo "  - Public access:                OFF"
  echo "  - Custom domains tab:           empty (or only the intended Worker route)"
  echo "  - Public Development URL:       OFF"
  echo "Paste the screenshot or text dump beside this audit log file."
  echo
  if [[ "$failures" -eq 0 ]]; then
    echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] wa-3ia.4 audit OK (4/4 curl checks passed)"
  else
    echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] wa-3ia.4 audit FAILED ($failures of 4 curl checks unexpected) — read the bead's fail-path matrix"
  fi
} >> "$LOG"

cat "$LOG" | tail -n 30 >&2

if [[ "$failures" -ne 0 ]]; then
  exit 1
fi
