#!/usr/bin/env bash
# Tests for blocks-sdk/cli/install-from-artifactory.sh OS allowlist.
# Run: bash blocks-sdk/cli/tests/install_artifactory_test.sh
# Note: -e is intentionally omitted — assertion failures must be counted, not abort.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET="$SCRIPT_DIR/../install-from-artifactory.sh"

fails=0
passes=0

assert_os_accepted() {
	local uname_s="$1" expected_lower="$2"
	local shim_dir output
	shim_dir=$(mktemp -d)
	cat > "$shim_dir/uname" <<EOF
#!/usr/bin/env bash
case "\$1" in
  -s) echo "$uname_s" ;;
  -m) echo "x86_64" ;;
  *)  /usr/bin/uname "\$@" ;;
esac
EOF
	chmod +x "$shim_dir/uname"
	output=$(PATH="$shim_dir:$PATH" bash "$TARGET" 2>&1 || true)
	rm -rf "$shim_dir"

	if echo "$output" | grep -q "Unsupported OS:"; then
		echo "FAIL  uname -s=$uname_s rejected by OS allowlist"
		fails=$((fails + 1))
		return
	fi
	if echo "$output" | grep -q "Platform: ${expected_lower}-"; then
		echo "PASS  uname -s=$uname_s -> Platform: ${expected_lower}-..."
		passes=$((passes + 1))
		return
	fi
	echo "FAIL  uname -s=$uname_s did not produce expected 'Platform: ${expected_lower}-' line"
	echo "      output head: $(echo "$output" | head -2 | tr '\n' '|')"
	fails=$((fails + 1))
}

assert_os_accepted "Darwin" "darwin"
assert_os_accepted "Linux" "linux"
assert_os_redirected_to_install_sh() {
	local uname_s="$1"
	local shim_dir output exit_code
	shim_dir=$(mktemp -d)
	cat > "$shim_dir/uname" <<EOF
#!/usr/bin/env bash
case "\$1" in
  -s) echo "$uname_s" ;;
  -m) echo "x86_64" ;;
  *)  /usr/bin/uname "\$@" ;;
esac
EOF
	chmod +x "$shim_dir/uname"
	output=$(PATH="$shim_dir:$PATH" bash "$TARGET" 2>&1)
	exit_code=$?
	rm -rf "$shim_dir"

	if [ "$exit_code" -eq 0 ]; then
		echo "FAIL  uname -s=$uname_s expected non-zero exit, got 0"
		fails=$((fails + 1))
		return
	fi
	if echo "$output" | grep -q "Platform:"; then
		echo "FAIL  uname -s=$uname_s should not progress to Platform: line"
		fails=$((fails + 1))
		return
	fi
	if echo "$output" | grep -q "install.sh"; then
		echo "PASS  uname -s=$uname_s -> redirected to install.sh"
		passes=$((passes + 1))
		return
	fi
	echo "FAIL  uname -s=$uname_s did not produce expected redirect message"
	echo "      output: $(echo "$output" | head -3 | tr '\n' '|')"
	fails=$((fails + 1))
}

assert_os_redirected_to_install_sh "FreeBSD"
assert_os_redirected_to_install_sh "OpenBSD"

echo ""
echo "Passed: $passes  Failed: $fails"
[ "$fails" -eq 0 ]
