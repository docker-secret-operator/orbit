#!/usr/bin/env bash
#
# Orbit native installer — installs the docker-orbit binary as a Docker CLI plugin.
#
# This installs a NATIVE binary (no Go toolchain, no source, no container wrapper).
# The previous container-wrapper installer was retired: Orbit's CLI is a host-side
# tool (rollback state in /tmp, history in $XDG_STATE_HOME) and cannot run correctly
# inside an ephemeral container. See docs/installation.md.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/docker-secret-operator/orbit/main/install.sh | bash
#
# Environment overrides:
#   ORBIT_VERSION    Version/tag to install (default: latest published release).
#   ORBIT_DIST_DIR   Install from a local directory of GoReleaser artifacts instead
#                    of downloading — for development / snapshot testing, e.g.:
#                        ORBIT_DIST_DIR=./dist ./install.sh
#   ORBIT_BASE_URL   Base URL for release archives (default: GitHub releases).
#   PLUGIN_DIR       Docker CLI plugins dir (default: system dir, then ~ fallback).
#
set -euo pipefail

REPO="docker-secret-operator/orbit"
BINARY="docker-orbit"
SYSTEM_PLUGIN_DIR="/usr/local/lib/docker/cli-plugins"
USER_PLUGIN_DIR="${HOME}/.docker/cli-plugins"

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; NC=$'\033[0m'
info() { echo "${GREEN}✓${NC} $*"; }
warn() { echo "${YELLOW}⚠${NC} $*"; }
err()  { echo "${RED}✗${NC} $*" >&2; }
die()  { err "$*"; exit 1; }

# ── Detect platform ──────────────────────────────────────────────────────────
detect_os() {
  case "$(uname -s)" in
    Linux)  echo "linux" ;;
    Darwin) echo "darwin" ;;
    *) die "unsupported OS: $(uname -s) (Orbit supports Linux and macOS)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)   echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) die "unsupported architecture: $(uname -m) (Orbit supports amd64 and arm64)" ;;
  esac
}

# ── sha256 (portable across Linux/macOS) ─────────────────────────────────────
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "need sha256sum or shasum for checksum verification"
  fi
}

# verify_checksum <artifact-path> <checksums-file> <artifact-basename>
# Rejects a tampered or truncated artifact by comparing its SHA256 against the
# published checksums.txt line for that filename.
verify_checksum() {
  local artifact="$1" checksums="$2" name="$3"
  local expected actual
  expected="$(awk -v f="$name" '$2 == f {print $1}' "$checksums" | head -n1)"
  [ -n "$expected" ] || die "no checksum entry for $name in $(basename "$checksums")"
  actual="$(sha256_of "$artifact")"
  if [ "$expected" != "$actual" ]; then
    err "checksum verification FAILED for $name"
    err "  expected: $expected"
    err "  actual:   $actual"
    die "refusing to install a binary that does not match its published checksum"
  fi
  info "checksum verified: $name"
}

# ── Locate artifacts (local dir or download) ─────────────────────────────────
# Sets globals ARCHIVE (.tar.gz path) and CHECKSUMS (checksums.txt path).
# Requires WORKDIR, OS, ARCH to be set.
locate_artifacts() {
  if [ -n "${ORBIT_DIST_DIR:-}" ]; then
    [ -d "$ORBIT_DIST_DIR" ] || die "ORBIT_DIST_DIR not a directory: $ORBIT_DIST_DIR"
    local match
    match="$(find "$ORBIT_DIST_DIR" -maxdepth 1 -name "${BINARY}_*_${OS}_${ARCH}.tar.gz" 2>/dev/null | head -n1)"
    [ -n "$match" ] || die "no ${OS}/${ARCH} archive in $ORBIT_DIST_DIR (looked for ${BINARY}_*_${OS}_${ARCH}.tar.gz)"
    ARCHIVE="$match"
    CHECKSUMS="${ORBIT_DIST_DIR}/checksums.txt"
    [ -f "$CHECKSUMS" ] || die "no checksums.txt in $ORBIT_DIST_DIR"
    info "using local artifacts from $ORBIT_DIST_DIR"
  else
    command -v curl >/dev/null 2>&1 || die "curl is required to download release artifacts"
    local version="${ORBIT_VERSION:-latest}"
    if [ "$version" = "latest" ]; then
      version="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | awk -F'"' '/"tag_name"/ {print $4; exit}')"
      [ -n "$version" ] || die "could not resolve latest release tag (is the repo public and released?)"
    fi
    local base="${ORBIT_BASE_URL:-https://github.com/${REPO}/releases/download/${version}}"
    local name="${BINARY}_${version}_${OS}_${ARCH}.tar.gz"
    ARCHIVE="${WORKDIR}/${name}"
    CHECKSUMS="${WORKDIR}/checksums.txt"
    info "downloading ${name} (${version})"
    curl -fsSL -o "$ARCHIVE" "${base}/${name}" || die "download failed: ${base}/${name}"
    curl -fsSL -o "$CHECKSUMS" "${base}/checksums.txt" || die "download failed: ${base}/checksums.txt"
  fi
}

# ── Choose install dir (preserves an existing choice on upgrade) ─────────────
choose_plugin_dir() {
  if [ -n "${PLUGIN_DIR:-}" ]; then echo "$PLUGIN_DIR"; return; fi
  if [ -e "${SYSTEM_PLUGIN_DIR}/${BINARY}" ]; then echo "$SYSTEM_PLUGIN_DIR"; return; fi
  if [ -e "${USER_PLUGIN_DIR}/${BINARY}" ]; then echo "$USER_PLUGIN_DIR"; return; fi
  if [ -w "$(dirname "$SYSTEM_PLUGIN_DIR")" ] 2>/dev/null || [ -w "$SYSTEM_PLUGIN_DIR" ] 2>/dev/null; then
    echo "$SYSTEM_PLUGIN_DIR"
  elif command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    echo "$SYSTEM_PLUGIN_DIR"
  else
    echo "$USER_PLUGIN_DIR"
  fi
}

# install_file <src> <dst> — writes with 0755, elevating with sudo only if needed.
install_file() {
  local src="$1" dst="$2" dir
  dir="$(dirname "$dst")"
  if { [ -d "$dir" ] && [ -w "$dir" ]; } || { [ ! -e "$dir" ] && [ -w "$(dirname "$dir")" ] 2>/dev/null; }; then
    mkdir -p "$dir"; install -m 0755 "$src" "$dst"
  else
    warn "elevating with sudo to write $dir"
    sudo mkdir -p "$dir"; sudo install -m 0755 "$src" "$dst"
  fi
}

main() {
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "  Orbit installer (native Docker CLI plugin)"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  OS="$(detect_os)"; ARCH="$(detect_arch)"
  info "platform: ${OS}/${ARCH}"

  WORKDIR="$(mktemp -d)"; trap 'rm -rf "$WORKDIR"' EXIT

  locate_artifacts
  verify_checksum "$ARCHIVE" "$CHECKSUMS" "$(basename "$ARCHIVE")"

  tar -xzf "$ARCHIVE" -C "$WORKDIR"
  [ -f "${WORKDIR}/${BINARY}" ] || die "archive did not contain ${BINARY}"

  local dir dst; dir="$(choose_plugin_dir)"; dst="${dir}/${BINARY}"

  # Upgrade replaces only the binary. Orbit keeps no package-managed config;
  # runtime state (/tmp/orbit-*, $XDG_STATE_HOME/orbit) is never touched.
  [ -e "$dst" ] && info "upgrading existing install at $dst"
  install_file "${WORKDIR}/${BINARY}" "$dst"
  info "installed: $dst"

  if command -v docker >/dev/null 2>&1 && { [ "$dir" = "$SYSTEM_PLUGIN_DIR" ] || [ "$dir" = "$USER_PLUGIN_DIR" ]; }; then
    local dver
    if dver="$(docker orbit version 2>/dev/null)"; then
      info "plugin discovered by Docker: docker orbit → ${dver}"
    else
      warn "installed, but 'docker orbit' not discovered yet (restart shell or check Docker CLI version)"
    fi
  fi
  info "docker-orbit version: $("$dst" version 2>/dev/null || echo unknown)"

  echo ""
  info "Done. Try:  docker orbit doctor"
  if [ "$dir" = "$USER_PLUGIN_DIR" ]; then
    warn "Installed per-user. For all users, re-run with sudo or PLUGIN_DIR=${SYSTEM_PLUGIN_DIR}."
  fi
}

main "$@"
