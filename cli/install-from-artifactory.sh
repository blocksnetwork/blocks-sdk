#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="$HOME/.blocks/bin"
BINARY_NAME="blocks"

# ── Artifactory credentials (from .npmrc) ────────────────────────────
REGISTRY="https://artifactnub1.jfrog.io/artifactnub1/api/npm/npm-local"
AUTH_TOKEN="cmVmdGtuOjAxOjAwMDAwMDAwMDA6bnhOU1ZUWTNubEJFNHZwSWFoWTMxVnBYcm1B"

# ── Detect OS & architecture ─────────────────────────────────────────
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: ${OS}"; exit 1 ;;
esac

case "$ARCH" in
  x86_64|amd64)  ARCH="x64" ;;
  arm64|aarch64)  ARCH="arm64" ;;
  *) echo "Unsupported architecture: ${ARCH}"; exit 1 ;;
esac

# Platform package: @blocks-network/cli-<os>-<arch>
PLATFORM_PKG="@blocks-network/cli-${OS}-${ARCH}"
echo "Platform: ${OS}-${ARCH}"

# ── Check required tools ─────────────────────────────────────────────
for cmd in curl tar; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "Error: '${cmd}' is required but not installed."
    exit 1
  fi
done

# ── Fetch latest version from the base package ───────────────────────
echo "Fetching latest version..."
META=$(curl -sf -H "Authorization: Bearer ${AUTH_TOKEN}" \
  "${REGISTRY}/@blocks-network%2Fcli/latest" 2>/dev/null) \
|| { echo "Error: could not reach registry at ${REGISTRY}"; exit 1; }

if command -v jq &>/dev/null; then
  VERSION=$(echo "$META" | jq -r '.version')
else
  VERSION=$(echo "$META" | grep -o '"version":"[^"]*"' | head -1 | cut -d'"' -f4)
fi

if [[ -z "${VERSION:-}" ]]; then
  echo "Error: could not determine latest version."
  exit 1
fi

echo "Installing ${PLATFORM_PKG}@${VERSION}..."

# ── Fetch platform package tarball URL ────────────────────────────────
ENCODED_PKG=$(echo "$PLATFORM_PKG" | sed 's/@/%40/' | sed 's/\//%2F/')
PKG_META=$(curl -sf -H "Authorization: Bearer ${AUTH_TOKEN}" \
  "${REGISTRY}/${ENCODED_PKG}/${VERSION}" 2>/dev/null) \
|| PKG_META=$(curl -sf -H "Authorization: Bearer ${AUTH_TOKEN}" \
  "${REGISTRY}/${ENCODED_PKG}/latest" 2>/dev/null) \
|| { echo "Error: platform package ${PLATFORM_PKG} not found."; exit 1; }

if command -v jq &>/dev/null; then
  TARBALL=$(echo "$PKG_META" | jq -r '.dist.tarball')
else
  TARBALL=$(echo "$PKG_META" | grep -o '"tarball":"[^"]*"' | head -1 | cut -d'"' -f4)
fi

if [[ -z "${TARBALL:-}" || "${TARBALL}" == "null" ]]; then
  echo "Error: could not determine tarball URL for ${PLATFORM_PKG}."
  exit 1
fi

# ── Download & extract ────────────────────────────────────────────────
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Downloading..."
curl -sfL -H "Authorization: Bearer ${AUTH_TOKEN}" \
  -o "${TMP_DIR}/package.tgz" "$TARBALL"

tar -xzf "${TMP_DIR}/package.tgz" -C "$TMP_DIR"

# ── Locate the binary ────────────────────────────────────────────────
# Platform packages contain the binary as: package/blocks
BINARY="${TMP_DIR}/package/${BINARY_NAME}"

if [[ ! -f "$BINARY" ]]; then
  echo "Error: binary not found in package."
  echo "Package contents:"
  find "$TMP_DIR/package" -type f
  exit 1
fi

# ── Install binary ────────────────────────────────────────────────────
mkdir -p "$INSTALL_DIR"
cp "$BINARY" "${INSTALL_DIR}/${BINARY_NAME}"
chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
echo "Installed ${BINARY_NAME} → ${INSTALL_DIR}/${BINARY_NAME}"

# ── Ensure ~/.blocks/bin is on PATH ──────────────────────────────────
SHELL_NAME="$(basename "${SHELL:-/bin/sh}")"
case "$SHELL_NAME" in
  zsh)  PROFILE="$HOME/.zshrc" ;;
  bash) PROFILE="$HOME/.bashrc" ;;
  *)    PROFILE="$HOME/.profile" ;;
esac

PATH_LINE='export PATH="$HOME/.blocks/bin:$PATH"'
if ! grep -qF '.blocks/bin' "$PROFILE" 2>/dev/null; then
  printf '\n# Blocks CLI\n%s\n' "$PATH_LINE" >> "$PROFILE"
  echo "Added ${INSTALL_DIR} to PATH in ${PROFILE}"
  echo "Run 'source ${PROFILE}' or open a new terminal to use '${BINARY_NAME}'."
else
  echo "${INSTALL_DIR} already in PATH (${PROFILE})"
fi

echo ""
echo "Done! Run 'blocks --help' to get started."
