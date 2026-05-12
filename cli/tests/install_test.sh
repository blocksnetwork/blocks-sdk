#!/usr/bin/env bash
# Tests for blocks-sdk/cli/install.sh detect_os.
# Run: bash blocks-sdk/cli/tests/install_test.sh
# Note: -e is intentionally omitted — assertion failures must be counted, not abort.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALL_SCRIPT="$SCRIPT_DIR/../install.sh"

fails=0
passes=0

assert_detect_os() {
	local uname_output="$1" expected="$2"
	local actual
	actual=$(
		bash -c '
			# install.sh runs `set -eu` on source; detect_os hitting its
			# unsupported-OS branch will exit the subshell, leaving $actual empty.
			uname() {
				if [ "$1" = "-s" ]; then echo "'"$uname_output"'"; else command uname "$@"; fi
			}
			# Sentinel signals install.sh to define functions only, skip main.
			BLOCKS_INSTALL_SH_SOURCED=1
			# shellcheck disable=SC1090
			source "$1" >/dev/null 2>&1
			detect_os
		' _ "$INSTALL_SCRIPT"
	)
	if [ "$actual" = "$expected" ]; then
		echo "PASS  uname -s=$uname_output -> $actual"
		passes=$((passes + 1))
	else
		echo "FAIL  uname -s=$uname_output -> expected '$expected', got '$actual'"
		fails=$((fails + 1))
	fi
}

assert_detect_os "Darwin" "darwin"
assert_detect_os "Linux" "linux"
assert_detect_os "MINGW64_NT-10.0" "windows"
assert_detect_os "MSYS_NT-10.0" "windows"
assert_detect_os "FreeBSD" "freebsd"
assert_detect_os "OpenBSD" "openbsd"

echo ""
echo "Passed: $passes  Failed: $fails"
[ "$fails" -eq 0 ]
