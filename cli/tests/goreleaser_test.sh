#!/usr/bin/env bash
# Tests that goreleaser snapshot build produces FreeBSD and OpenBSD binaries.
# Run: bash blocks-sdk/cli/tests/goreleaser_test.sh
# Note: -e is intentionally omitted — assertion failures must be counted, not abort.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$SCRIPT_DIR/.."

cd "$CLI_DIR" || exit 1
rm -rf dist

LOG_FILE=$(mktemp -t goreleaser-test.XXXXXX)
trap 'rm -f "$LOG_FILE"' EXIT

echo "Running goreleaser snapshot build..."
BLOCKS_BACKEND_URL=https://example.invalid \
BLOCKS_CLI_CLIENT_ID=test \
	goreleaser build --snapshot --clean >"$LOG_FILE" 2>&1
build_status=$?

if [ "$build_status" -ne 0 ]; then
	echo "FAIL  goreleaser build exited $build_status"
	tail -20 "$LOG_FILE"
	exit 1
fi

fails=0
passes=0

assert_binary() {
	local goos="$1" goarch="$2"
	# GoReleaser places binaries in dist/<id>_<goos>_<goarch>[_<variant>]/blocks
	# Windows binaries use blocks.exe extension.
	local match
	match=$(find dist -type f \( -path "*${goos}_${goarch}*/blocks" -o -path "*${goos}_${goarch}*/blocks.exe" \) 2>/dev/null | head -1)
	if [ -n "$match" ] && [ -x "$match" ]; then
		echo "PASS  ${goos}/${goarch} -> $match"
		passes=$((passes + 1))
	else
		echo "FAIL  no binary found for ${goos}/${goarch}"
		fails=$((fails + 1))
	fi
}

assert_binary linux   amd64
assert_binary linux   arm64
assert_binary darwin  amd64
assert_binary darwin  arm64
assert_binary windows amd64
assert_binary freebsd amd64
assert_binary freebsd arm64
assert_binary openbsd amd64
assert_binary openbsd arm64

echo ""
echo "Passed: $passes  Failed: $fails"
rm -rf dist
[ "$fails" -eq 0 ]
