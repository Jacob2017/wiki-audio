#!/usr/bin/env bash
# install.sh — wiki-audio installer (wa-8gt.3).
#
# One-line install:
#   curl -fsSL https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh | bash
#
# Pin a version:
#   curl -fsSL https://raw.githubusercontent.com/Jacob2017/wiki-audio/main/install.sh | bash -s -- v0.3.0
#
# Behaviors that matter (per wa-8gt.3):
#   - Idempotent. Re-running overwrites the binary cleanly.
#   - Fails loudly on unknown platform; points at the Releases page.
#   - SHA-256 verifies before installing — this is what makes the
#     curl|bash line not a supply-chain vulnerability (§3 / wa-8gt.1).
#   - No `set -x` by default. Output is one happy path of
#     detected → fetched → verified → installed → next step.
#
# Override the GitHub endpoints for testing:
#   WA_API=http://localhost:8000  WA_DL=http://localhost:8000  bash install.sh v0.0.0

set -euo pipefail

readonly REPO="Jacob2017/wiki-audio"
readonly BIN="wiki-audio"
readonly API="${WA_API:-https://api.github.com}"
readonly DL="${WA_DL:-https://github.com}"

err() { printf 'install: error: %s\n' "$*" >&2; exit 1; }
say() { printf 'install: %s\n' "$*"; }

# detect_platform — emit "<os>_<arch>" matching goreleaser's archive naming.
detect_platform() {
    local os arch
    os=$(uname -s)
    arch=$(uname -m)
    case "$os" in
        Linux)  os=linux ;;
        Darwin) os=darwin ;;
        *) err "unsupported OS: $os. See https://github.com/${REPO}/releases for manual install." ;;
    esac
    case "$arch" in
        x86_64|amd64)  arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) err "unsupported architecture: $arch. See https://github.com/${REPO}/releases for manual install." ;;
    esac
    printf '%s_%s' "$os" "$arch"
}

# resolve_version — return the user-supplied tag, or fetch the latest from
# GitHub Releases. Empty return is treated as a fatal error by the caller.
resolve_version() {
    local v="${1:-}"
    if [ -n "$v" ]; then
        printf '%s' "$v"
        return 0
    fi
    local resp
    resp=$(curl -fsSL "${API}/repos/${REPO}/releases/latest") \
        || err "could not fetch latest release tag from ${API}"
    # Cheap JSON tag extraction. We avoid a jq dependency because curl is
    # already required and most release-tag responses fit a one-line regex.
    printf '%s' "$resp" \
        | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
        | head -n 1
}

# pick_install_dir — root → /usr/local/bin, else $HOME/.local/bin (created
# with mkdir -p so a fresh user box doesn't fail on the first run).
pick_install_dir() {
    if [ "$(id -u)" = "0" ]; then
        printf '%s' "/usr/local/bin"
        return 0
    fi
    local d="${HOME}/.local/bin"
    mkdir -p "$d" || err "could not create $d"
    printf '%s' "$d"
}

# verify_sha256 — pipe the matching checksums.txt line into sha256sum
# (Linux) or shasum -a 256 (macOS). Both honor `-c` for stdin verification.
verify_sha256() {
    local checksums_file="$1" tarball="$2"
    local line
    # awk over grep so the tarball name is matched as a literal column,
    # not a regex (the `.tar.gz` dots and underscores would otherwise be
    # regex metacharacters).
    line=$(awk -v t="$tarball" '$2 == t { print; found=1 } END { exit !found }' "$checksums_file") \
        || err "no checksum line for $tarball in checksums.txt"

    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s\n' "$line" | (cd "$(dirname "$checksums_file")" && sha256sum -c --quiet) \
            || err "SHA-256 verification failed for $tarball"
    elif command -v shasum >/dev/null 2>&1; then
        printf '%s\n' "$line" | (cd "$(dirname "$checksums_file")" && shasum -a 256 -c --quiet) \
            || err "SHA-256 verification failed for $tarball"
    else
        err "neither sha256sum nor shasum available; cannot verify download"
    fi
}

main() {
    local target version tarball asset_url checksums_url install_dir tmp
    target=$(detect_platform)
    version=$(resolve_version "${1:-}")
    [ -n "$version" ] || err "could not resolve version (latest tag empty)"

    # goreleaser strips a leading `v` from the version in the tarball name:
    # tag v0.3.0 → wiki-audio_0.3.0_linux_amd64.tar.gz.
    tarball="${BIN}_${version#v}_${target}.tar.gz"
    asset_url="${DL}/${REPO}/releases/download/${version}/${tarball}"
    checksums_url="${DL}/${REPO}/releases/download/${version}/checksums.txt"
    install_dir=$(pick_install_dir)

    say "detected: ${target}"
    say "version:  ${version}"
    say "tarball:  ${tarball}"
    say "install:  ${install_dir}/${BIN}"

    tmp=$(mktemp -d -t wiki-audio-install.XXXXXX) \
        || err "could not create temp directory"
    # shellcheck disable=SC2064
    # We deliberately expand $tmp at trap-set time; if it changed later
    # (it doesn't), the trap should still target the original directory.
    trap "rm -rf '$tmp'" EXIT

    say "fetching tarball..."
    curl -fsSL --output "${tmp}/${tarball}" "$asset_url" \
        || err "could not download $asset_url"

    say "fetching checksums..."
    curl -fsSL --output "${tmp}/checksums.txt" "$checksums_url" \
        || err "could not download $checksums_url"

    say "verifying SHA-256..."
    verify_sha256 "${tmp}/checksums.txt" "$tarball"

    say "extracting..."
    tar -xzf "${tmp}/${tarball}" -C "$tmp" "$BIN" \
        || err "could not extract $BIN from $tarball"

    say "installing to ${install_dir}..."
    mv -f "${tmp}/${BIN}" "${install_dir}/${BIN}" \
        || err "could not install to ${install_dir}/${BIN}"
    chmod +x "${install_dir}/${BIN}"

    say "installed: ${install_dir}/${BIN}"
    if ! printf '%s' ":${PATH}:" | grep -qF ":${install_dir}:"; then
        say "note: ${install_dir} is not on \$PATH; add it to your shell rc to use \`${BIN}\` directly."
    fi
    say "next: ${BIN} init"
}

main "$@"
