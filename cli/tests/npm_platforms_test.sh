#!/usr/bin/env bash
# Tests that the npm PLATFORMS mapping in postinstall.js, the wrapper
# package.json optionalDependencies, and the cli-npm-publish workflow
# all agree on the set of supported platform packages.
# Run: bash blocks-sdk/cli/tests/npm_platforms_test.sh
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$SCRIPT_DIR/.."
POSTINSTALL="$CLI_DIR/npm/cli/postinstall.js"
WRAPPER_PKG="$CLI_DIR/npm/cli/package.json"
WORKFLOW="$CLI_DIR/../.github/workflows/cli-npm-publish.yaml"

fails=0
passes=0

# Canonical set of platform package names (sorted)
EXPECTED=(
  cli-darwin-arm64
  cli-darwin-x64
  cli-freebsd-arm64
  cli-freebsd-x64
  cli-linux-arm64
  cli-linux-x64
  cli-win32-x64
)

# --- Check postinstall.js PLATFORMS mapping ---
for pkg in "${EXPECTED[@]}"; do
  if grep -q "@blocks-network/${pkg}/" "$POSTINSTALL"; then
    echo "PASS  postinstall.js maps $pkg"
    passes=$((passes + 1))
  else
    echo "FAIL  postinstall.js missing $pkg"
    fails=$((fails + 1))
  fi
done

# --- Check wrapper package.json optionalDependencies ---
for pkg in "${EXPECTED[@]}"; do
  if grep -q "\"@blocks-network/${pkg}\"" "$WRAPPER_PKG"; then
    echo "PASS  wrapper package.json lists $pkg"
    passes=$((passes + 1))
  else
    echo "FAIL  wrapper package.json missing $pkg"
    fails=$((fails + 1))
  fi
done

# --- Check cli-npm-publish workflow publish loop ---
for pkg in "${EXPECTED[@]}"; do
  if grep -q "$pkg" "$WORKFLOW"; then
    echo "PASS  cli-npm-publish.yaml references $pkg"
    passes=$((passes + 1))
  else
    echo "FAIL  cli-npm-publish.yaml missing $pkg"
    fails=$((fails + 1))
  fi
done

# --- Check that each platform package directory has a LICENSE file ---
for pkg in "${EXPECTED[@]}"; do
  if [ -f "$CLI_DIR/npm/${pkg}/LICENSE" ]; then
    echo "PASS  npm/${pkg}/LICENSE exists"
    passes=$((passes + 1))
  else
    echo "FAIL  npm/${pkg}/LICENSE missing"
    fails=$((fails + 1))
  fi
done

echo ""
echo "Passed: $passes  Failed: $fails"
[ "$fails" -eq 0 ]
