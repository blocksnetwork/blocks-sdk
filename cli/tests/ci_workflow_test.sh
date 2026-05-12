#!/usr/bin/env bash
# Tests that the CI cross-compile loop builds the BSD targets.
# Run: bash blocks-sdk/cli/tests/ci_workflow_test.sh
# Note: -e is intentionally omitted — assertion failures must be counted, not abort.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$SCRIPT_DIR/.."
WORKFLOW="$CLI_DIR/../.github/workflows/cli-release.yml"

LOG_FILE=$(mktemp -t cibuild.XXXXXX)
trap 'rm -f "$LOG_FILE"' EXIT

fails=0
passes=0

# --- Static check: workflow for-loop line lists each BSD target ---
loop_line=$(grep -n 'for target in' "$WORKFLOW" | head -1)
if [ -z "$loop_line" ]; then
	echo "FAIL  could not find 'for target in' line in $WORKFLOW"
	fails=$((fails + 1))
else
	for t in freebsd/amd64 freebsd/arm64 openbsd/amd64 openbsd/arm64; do
		if echo "$loop_line" | grep -q "$t"; then
			echo "PASS  workflow loop lists $t"
			passes=$((passes + 1))
		else
			echo "FAIL  workflow loop missing $t"
			fails=$((fails + 1))
		fi
	done
fi

# --- Dynamic check: same loop logic produces the four BSD binaries ---
cd "$CLI_DIR" || exit 1
rm -rf dist-citest
mkdir -p dist-citest
LDFLAGS="-s -w -X github.com/pubnub/blocks-sdk/cli/cmd.Version=0.0.0-citest"

for target in freebsd/amd64 freebsd/arm64 openbsd/amd64 openbsd/arm64; do
	goos="${target%/*}"
	goarch="${target#*/}"
	out="dist-citest/blocks_${goos}_${goarch}"
	if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
		go build -ldflags "$LDFLAGS" -o "$out" . >"$LOG_FILE" 2>&1; then
		if [ -s "$out" ]; then
			echo "PASS  built $out"
			passes=$((passes + 1))
		else
			echo "FAIL  build succeeded but $out is empty"
			fails=$((fails + 1))
		fi
	else
		echo "FAIL  build failed for $target"
		tail -10 "$LOG_FILE"
		fails=$((fails + 1))
	fi
done

rm -rf dist-citest

echo ""
echo "Passed: $passes  Failed: $fails"
[ "$fails" -eq 0 ]
