#!/usr/bin/env sh
# Blocks CLI installer for macOS / Linux / WSL / FreeBSD / OpenBSD
# POSIX sh-compatible — no bash required.
#
# Usage:
#   curl -fsSL https://config.blocks.ai/install.sh | bash
#
# Or with a GitHub token (for private repo / pre-release builds):
#   export GITHUB_TOKEN="ghp_..."
#   curl -fsSL https://config.blocks.ai/install.sh | bash
#
# Environment variables:
#   GITHUB_TOKEN / GH_TOKEN — GitHub personal access token (only needed for private repo builds)
#   BLOCKS_INSTALL_DIR      — Override install directory (default: ~/.blocks/bin)
#   BLOCKS_RELEASES_URL     — Override download base URL (flat directory with latest.json + archives)
#   BLOCKS_VERSION          — Pin to a specific version (default: latest)

set -eu
# Default to GitHub API download. Set BLOCKS_RELEASES_URL to use a flat-
# directory host (S3, etc.) instead:
#   BLOCKS_RELEASES_URL="https://example.com/releases" bash install.sh
BLOCKS_RELEASES_URL="${BLOCKS_RELEASES_URL:-}"

GITHUB_REPO="pubnub/blocksnetwork"
GITHUB_API="https://api.github.com"
INSTALL_DIR="${BLOCKS_INSTALL_DIR:-$HOME/.blocks/bin}"
AUTH_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-$(gh auth token 2>/dev/null || echo "")}}"

# When no GitHub token and no explicit releases URL, default to the public
# releases endpoint so `curl ... | bash` works without any authentication.
if [ -z "${BLOCKS_RELEASES_URL:-}" ] && [ -z "$AUTH_TOKEN" ]; then
  BLOCKS_RELEASES_URL="https://config.blocks.ai/releases/cli"
fi

# ── Authenticated download helper ─────────────────────────────────────
download() {
  url="$1"
  dest="$2"
  if [ -n "$AUTH_TOKEN" ]; then
    curl -fsSL -H "Authorization: token ${AUTH_TOKEN}" "$url" -o "$dest"
  else
    curl -fsSL "$url" -o "$dest"
  fi
}

# ── Authenticated download via GitHub API (Accept: octet-stream) ──────
download_asset() {
  url="$1"
  dest="$2"
  if [ -n "$AUTH_TOKEN" ]; then
    curl -fsSL -H "Authorization: token ${AUTH_TOKEN}" -H "Accept: application/octet-stream" "$url" -o "$dest"
  else
    curl -fsSL -H "Accept: application/octet-stream" "$url" -o "$dest"
  fi
}

# ── Fetch JSON from GitHub API ────────────────────────────────────────
api_get() {
  url="$1"
  if [ -n "$AUTH_TOKEN" ]; then
    curl -fsSL -H "Authorization: token ${AUTH_TOKEN}" -H "Accept: application/vnd.github+json" "$url"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" "$url"
  fi
}

# ── Detect OS ────────────────────────────────────────────────────────
detect_os() {
  case "$(uname -s)" in
    Darwin)  echo "darwin" ;;
    Linux)   echo "linux" ;;
    FreeBSD) echo "freebsd" ;;
    OpenBSD) echo "openbsd" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *)
      echo "Error: Unsupported operating system: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

# ── Detect architecture ─────────────────────────────────────────────
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "Error: Unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

# ── SHA-256 verification (against checksums.txt) ─────────────────────
# GoReleaser produces a checksums.txt with lines like:
#   abc123  blocks_0.2.0_darwin_arm64.tar.gz
verify_checksum() {
  file="$1"
  filename="$2"
  checksums_file="$3"

  expected=$(grep "  ${filename}$" "$checksums_file" | awk '{print $1}')
  if [ -z "$expected" ]; then
    # Also try single-space separator
    expected=$(grep " ${filename}$" "$checksums_file" | awk '{print $1}')
  fi
  if [ -z "$expected" ]; then
    echo "Warning: No checksum found for ${filename} — skipping verification" >&2
    return 0
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$file" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$file" | awk '{print $1}')
  elif command -v sha256 >/dev/null 2>&1; then
    # FreeBSD and OpenBSD base utility
    actual=$(sha256 -q "$file")
  elif command -v openssl >/dev/null 2>&1; then
    actual=$(openssl dgst -sha256 "$file" | awk '{print $NF}')
  else
    echo "Warning: No SHA-256 tool found — skipping checksum verification" >&2
    return 0
  fi

  if [ "$actual" != "$expected" ]; then
    echo "Error: Checksum mismatch" >&2
    echo "  Expected: $expected" >&2
    echo "  Actual:   $actual" >&2
    exit 1
  fi
}

# ── Add to PATH ──────────────────────────────────────────────────────
add_to_path() {
  install_dir="$1"

  path_line="export PATH=\"$install_dir:\$PATH\""

  if [ -n "${ZSH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "zsh" ]; then
    shell_profile="$HOME/.zshrc"
  elif [ -n "${BASH_VERSION:-}" ] || [ "$(basename "${SHELL:-}")" = "bash" ]; then
    if [ -f "$HOME/.bashrc" ]; then
      shell_profile="$HOME/.bashrc"
    else
      shell_profile="$HOME/.profile"
    fi
  elif [ -f "$HOME/.config/fish/config.fish" ]; then
    shell_profile="$HOME/.config/fish/config.fish"
    path_line="fish_add_path $install_dir"
  else
    shell_profile="$HOME/.profile"
  fi

  if [ -f "$shell_profile" ] && grep -qF "$install_dir" "$shell_profile" 2>/dev/null; then
    return 0
  fi

  echo "" >> "$shell_profile"
  echo "# Blocks CLI" >> "$shell_profile"
  echo "$path_line" >> "$shell_profile"

  echo "  Added $install_dir to PATH in $shell_profile"
}

# ── Get installed version ────────────────────────────────────────────
get_installed_version() {
  if [ -x "${INSTALL_DIR}/blocks" ]; then
    "${INSTALL_DIR}/blocks" version 2>/dev/null || echo ""
  else
    echo ""
  fi
}

# ── Parse JSON value (portable, no jq dependency) ────────────────────
json_value() {
  echo "$1" | grep -o "\"$2\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -1 | sed "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/"
}

# ── Find asset download URL from GitHub API release JSON ─────────────
find_asset_api_url() {
  release_json="$1"
  asset_name="$2"
  echo "$release_json" | grep -B5 "\"name\":.*\"${asset_name}\"" | grep '"url"' | head -1 | sed 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/'
}

# ── Main ─────────────────────────────────────────────────────────────
main() {
  os=$(detect_os)
  arch=$(detect_arch)

  echo "Blocks CLI installer"
  echo "  Platform: ${os}/${arch}"

  installed_version=$(get_installed_version)

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  # ── Flat-directory download (S3 or custom URL) ────────────────────
  if [ -n "${BLOCKS_RELEASES_URL:-}" ]; then
    download_base="${BLOCKS_RELEASES_URL}"

    if [ -n "${BLOCKS_VERSION:-}" ]; then
      version="${BLOCKS_VERSION}"
    else
      echo "  Fetching version manifest..."
      download "${download_base}/latest.json" "${tmp_dir}/latest.json"
      manifest_content=$(cat "${tmp_dir}/latest.json")
      version=$(json_value "$manifest_content" "version")
    fi

    if [ -z "$version" ]; then
      echo "Error: Could not determine version from manifest" >&2
      exit 1
    fi

    if [ -n "$installed_version" ]; then
      if [ "$installed_version" = "$version" ]; then
        echo "  Installed: v${installed_version}"
        echo ""
        echo "Blocks CLI v${version} is already up to date."
        exit 0
      else
        echo "  Installed: v${installed_version}"
        echo "  Available: v${version}"
        echo "  Upgrading..."
      fi
    else
      echo "  Version: v${version}"
    fi

    # GoReleaser archive naming
    archive_name="blocks_${version}_${os}_${arch}.tar.gz"

    echo "  Downloading checksums..."
    download "${download_base}/checksums.txt" "${tmp_dir}/checksums.txt"

    echo "  Downloading ${archive_name}..."
    download "${download_base}/${archive_name}" "${tmp_dir}/${archive_name}"

  # ── GitHub API download (default) ─────────────────────────────────
  else
    if [ -z "$AUTH_TOKEN" ]; then
      echo "Error: GITHUB_TOKEN or GH_TOKEN is required for private repo downloads." >&2
      echo "  Set it with: export GITHUB_TOKEN=\"ghp_...\"" >&2
      echo "  Or authenticate via: gh auth login" >&2
      exit 1
    fi

    if [ -n "${BLOCKS_VERSION:-}" ]; then
      release_url="${GITHUB_API}/repos/${GITHUB_REPO}/releases/tags/cli-v${BLOCKS_VERSION}"
      echo "  Fetching release info from GitHub API..."
      release_json=$(api_get "$release_url")
    else
      echo "  Fetching latest CLI release from GitHub API..."

      # Try /releases/latest first (single API call). If the repo's "latest"
      # release is already a CLI tag we're done; otherwise fall back to scanning.
      release_json=$(api_get "${GITHUB_API}/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null) || release_json=""
      cli_tag=$(echo "$release_json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"cli-v[^"]*"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')

      if [ -z "$cli_tag" ]; then
        # Latest release isn't a CLI release — scan recent releases and pick
        # the most recently published cli-v* tag.
        releases_json=$(api_get "${GITHUB_API}/repos/${GITHUB_REPO}/releases?per_page=30")
        cli_tag=$(echo "$releases_json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"cli-v[^"]*"\|"published_at"[[:space:]]*:[[:space:]]*"[^"]*"' | awk '
          /tag_name.*cli-v/ { tag=$0; sub(/.*"tag_name"[[:space:]]*:[[:space:]]*"/, "", tag); sub(/".*/, "", tag); pending=tag; next }
          /published_at/ && pending { pub=$0; sub(/.*"published_at"[[:space:]]*:[[:space:]]*"/, "", pub); sub(/".*/, "", pub); print pub, pending; pending="" }
        ' | sort -r | head -1 | awk '{print $2}')
      fi

      if [ -z "$cli_tag" ]; then
        echo "Error: No CLI release (cli-v*) found" >&2
        exit 1
      fi

      echo "  Found release: ${cli_tag}"
      release_url="${GITHUB_API}/repos/${GITHUB_REPO}/releases/tags/${cli_tag}"
      release_json=$(api_get "$release_url")
    fi

    # Extract version from tag_name (strip "cli-v" prefix)
    version=$(echo "$release_json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"cli-v\{0,1\}\([^"]*\)".*/\1/')
    if [ -z "$version" ]; then
      # Fallback: try without cli- prefix
      version=$(echo "$release_json" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/')
    fi

    if [ -z "$version" ]; then
      echo "Error: Could not determine version from release" >&2
      exit 1
    fi

    if [ -n "$installed_version" ]; then
      if [ "$installed_version" = "$version" ]; then
        echo "  Installed: v${installed_version}"
        echo ""
        echo "Blocks CLI v${version} is already up to date."
        exit 0
      else
        echo "  Installed: v${installed_version}"
        echo "  Available: v${version}"
        echo "  Upgrading..."
      fi
    else
      echo "  Version: v${version}"
    fi

    # Download checksums.txt
    checksums_api_url=$(find_asset_api_url "$release_json" "checksums.txt")
    if [ -n "$checksums_api_url" ]; then
      echo "  Downloading checksums..."
      download_asset "$checksums_api_url" "${tmp_dir}/checksums.txt"
    fi

    # Download the archive
    archive_name="blocks_${version}_${os}_${arch}.tar.gz"
    archive_api_url=$(find_asset_api_url "$release_json" "$archive_name")
    if [ -z "$archive_api_url" ]; then
      echo "Error: Could not find ${archive_name} asset in the release" >&2
      exit 1
    fi

    echo "  Downloading ${archive_name}..."
    download_asset "$archive_api_url" "${tmp_dir}/${archive_name}"
  fi

  # ── Common: verify, extract, install ─────────────────────────────
  if [ -f "${tmp_dir}/checksums.txt" ]; then
    echo "  Verifying checksum..."
    verify_checksum "${tmp_dir}/${archive_name}" "$archive_name" "${tmp_dir}/checksums.txt"
  fi

  echo "  Installing to ${INSTALL_DIR}..."
  mkdir -p "$INSTALL_DIR"
  tar -xzf "${tmp_dir}/${archive_name}" -C "$INSTALL_DIR"

  chmod +x "${INSTALL_DIR}/blocks"

  # macOS: remove quarantine flag and ad-hoc sign
  if [ "$(uname -s)" = "Darwin" ]; then
    xattr -d com.apple.quarantine "${INSTALL_DIR}/blocks" 2>/dev/null || true
    codesign --sign - "${INSTALL_DIR}/blocks" 2>/dev/null || true
  fi

  add_to_path "$INSTALL_DIR"

  echo ""
  if [ -n "$installed_version" ]; then
    echo "Blocks CLI upgraded from v${installed_version} to v${version}!"
  else
    echo "Blocks CLI v${version} installed successfully!"
  fi
  echo ""
  echo "  Binary: ${INSTALL_DIR}/blocks"
  echo ""
  echo "  Restart your shell or run:"
  echo "    export PATH=\"${INSTALL_DIR}:\$PATH\""
  echo ""
  echo "  Then verify with:"
  echo "    blocks version"
}

# Only run main when executed directly, not when sourced for testing.
# The test harness sets BLOCKS_INSTALL_SH_SOURCED=1 before sourcing.
# This sentinel avoids the bash-only ${BASH_SOURCE[0]} array syntax so
# the script also parses under POSIX /bin/sh.
if [ -z "${BLOCKS_INSTALL_SH_SOURCED:-}" ]; then
  main "$@"
fi
