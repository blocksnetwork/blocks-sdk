#!/usr/bin/env bash
# Tests that the goreleaser snapshot build produces FreeBSD and OpenBSD binaries
# and that the instance domain baked into a release is always a usable DNS suffix
# — never empty, never malformed.
# Run: bash blocks-sdk/cli/tests/goreleaser_test.sh
# Note: -e is intentionally omitted — assertion failures must be counted, not abort.
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$SCRIPT_DIR/.."

cd "$CLI_DIR" || exit 1
rm -rf dist

LOG_FILE=$(mktemp -t goreleaser-test.XXXXXX)
DOMAIN_LOG=$(mktemp -t goreleaser-domain.XXXXXX)
trap 'rm -f "$LOG_FILE" "$DOMAIN_LOG"' EXIT

# env -u keeps a developer's exported BLOCKS_INSTANCE_DOMAIN out of this build so
# the assertion further down really covers the unset case.
echo "Running goreleaser snapshot build..."
env -u BLOCKS_INSTANCE_DOMAIN \
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

FLAG=defaultInstanceDomain

# instance_domain_pair prints the `defaultInstanceDomain=<value>` pair that is
# baked into the binary $1, read from the -ldflags Go records in every binary's
# build info. Prints nothing when the flag was not injected at all; prints the
# pair with an empty value when it was injected empty.
instance_domain_pair() {
	go version -m "$1" 2>/dev/null | grep -o -- "$FLAG=[^ \"]*" | head -1
}

# MAX_SUFFIX_LENGTH is the longest suffix a short name can expand under. The
# expansion is <label>.<suffix>, and a hostname may be at most 253 characters, so
# the shortest label and the dot joining it account for two of them: a suffix of 252
# or more can expand nothing at all, however well-formed it is. It mirrors
# maxInstanceSuffixLength in cmd/login.go.
MAX_SUFFIX_LENGTH=251

# is_plausible_instance_domain reports whether $1 is a usable DNS suffix: one or
# more dot-separated labels of 1 to 63 letters, digits and hyphens, no label
# starting or ending with a hyphen, and short enough that a short name still fits in
# front of it. Those rules are what stop a suffix from relocating the authority it is
# appended to — a value like `corp.example@collector.example` makes
# `blocks login acme` expand to `https://acme.corp.example@collector.example`, whose
# real HOST is collector.example, and the login would send its API key there.
#
# The CLI's own isDNSSuffix (cmd/login.go) is CANONICAL and refuses such a build at
# the point of use; this is a mirror of it, so that a release never gets that far.
# Every boundary below — the length cap, the alphabet, the empty-label cases, the
# 63-character label — must say what that function says. When the two disagree, the
# Go side is right and this one is the bug: a guard that blesses a suffix the runtime
# rejects passes a release whose every short-name login then fails.
is_plausible_instance_domain() {
	local domain="$1" rest label
	[ -n "$domain" ] || return 1
	[ "${#domain}" -le "$MAX_SUFFIX_LENGTH" ] || return 1
	# Anything outside the DNS alphabet — @, /, :, ?, #, whitespace — is out.
	case "$domain" in
	*[!a-zA-Z0-9.-]*) return 1 ;;
	esac
	# A leading, trailing or doubled dot is an empty label.
	case "$domain" in
	.* | *. | *..*) return 1 ;;
	esac
	rest="$domain"
	while [ -n "$rest" ]; do
		label="${rest%%.*}"
		if [ "$rest" = "$label" ]; then rest=""; else rest="${rest#*.}"; fi
		[ "${#label}" -le 63 ] || return 1
		case "$label" in
		-* | *-) return 1 ;;
		esac
	done
	return 0
}

# assert_instance_domain checks the instance domain that a build actually baked
# into binary $2. An empty value is release-breaking — it overwrites the compiled
# default, so `blocks login acme` expands to `https://acme.` — hence the flag
# must either carry a non-empty domain or be absent. A non-empty but malformed
# value is worse: it ships a CLI whose short-name login points somewhere the
# operator never intended. $3, when given, is the exact domain the build is
# expected to inject.
assert_instance_domain() {
	local label="$1" binary="$2" want="${3:-}"

	if [ ! -f "$binary" ]; then
		echo "FAIL  ${label}: no binary to inspect at ${binary}"
		fails=$((fails + 1))
		return
	fi

	local pair
	pair=$(instance_domain_pair "$binary")
	if [ -z "$pair" ]; then
		if [ -n "$want" ]; then
			echo "FAIL  ${label}: ${FLAG} not injected, expected ${want}"
			fails=$((fails + 1))
			return
		fi
		echo "PASS  ${label}: ${FLAG} not injected, compiled default kept"
		passes=$((passes + 1))
		return
	fi

	local domain="${pair#*=}"
	if [ -z "$domain" ]; then
		echo "FAIL  ${label}: ${FLAG} is empty — it overwrites the compiled default, so 'blocks login acme' expands to 'https://acme.'"
		fails=$((fails + 1))
		return
	fi
	if [ -n "$want" ] && [ "$domain" != "$want" ]; then
		echo "FAIL  ${label}: ${FLAG}=${domain}, expected ${want}"
		fails=$((fails + 1))
		return
	fi
	if ! is_plausible_instance_domain "$domain"; then
		echo "FAIL  ${label}: ${FLAG}=${domain} is not a DNS suffix — 'blocks login acme' would expand to a URL whose host is not the deployment"
		fails=$((fails + 1))
		return
	fi
	echo "PASS  ${label}: ${FLAG}=${domain}"
	passes=$((passes + 1))
}

# assert_instance_domain_rejected is the complement: binary $2 was deliberately
# built with the malformed domain $3, and the guard above has to catch it. Without
# this the guard could be satisfied by a check that never says no.
assert_instance_domain_rejected() {
	local label="$1" binary="$2" want="$3" pair domain

	if [ ! -f "$binary" ]; then
		echo "FAIL  ${label}: no binary to inspect at ${binary}"
		fails=$((fails + 1))
		return
	fi
	pair=$(instance_domain_pair "$binary")
	domain="${pair#*=}"
	if [ -z "$pair" ] || [ "$domain" != "$want" ]; then
		echo "FAIL  ${label}: expected the build to bake ${FLAG}=${want}, got '${pair}'"
		fails=$((fails + 1))
		return
	fi
	if is_plausible_instance_domain "$domain"; then
		echo "FAIL  ${label}: ${FLAG}=${domain} was accepted, but it is not a DNS suffix"
		fails=$((fails + 1))
		return
	fi
	echo "PASS  ${label}: ${FLAG}=${domain} rejected before release"
	passes=$((passes + 1))
}

# assert_domain_validator exercises the guard directly, with no build behind it, so
# the rules it encodes are pinned rather than only exercised by whatever a release
# happens to inject. $1 is accept|reject, $2 the value, $3 the case description.
assert_domain_validator() {
	local want="$1" value="$2" label="$3" got=accept

	is_plausible_instance_domain "$value" || got=reject
	if [ "$got" = "$want" ]; then
		echo "PASS  validator ${want}s ${label}"
		passes=$((passes + 1))
	else
		echo "FAIL  validator ${got}s ${label} ('${value}'), expected ${want}"
		fails=$((fails + 1))
	fi
}

# any_binary prints the path of one binary from the current dist tree. Every
# target is built from the same ldflags, so one is enough to inspect them.
any_binary() {
	find dist -type f \( -name blocks -o -name blocks.exe \) 2>/dev/null | head -1
}

# build_with_instance_domain runs a fast single-target snapshot build with
# BLOCKS_INSTANCE_DOMAIN set to $1 — which may be the empty string — and prints
# the resulting binary path.
build_with_instance_domain() {
	BLOCKS_BACKEND_URL=https://example.invalid \
	BLOCKS_CLI_CLIENT_ID=test \
	BLOCKS_INSTANCE_DOMAIN="$1" \
		goreleaser build --snapshot --clean --single-target >"$DOMAIN_LOG" 2>&1
	local status=$?
	if [ "$status" -ne 0 ]; then
		echo "FAIL  single-target build with BLOCKS_INSTANCE_DOMAIN='$1' exited $status" >&2
		tail -20 "$DOMAIN_LOG" >&2
		return 1
	fi
	any_binary
}

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

# BLOCKS_INSTANCE_DOMAIN was unset for the build above, as it is for any release
# that does not override the domain.
assert_instance_domain "unset BLOCKS_INSTANCE_DOMAIN" "$(any_binary)"

# Set but empty: an undefined CI variable expands to "", which a lookup that only
# tests for "unset" happily bakes in as an empty domain.
empty_case_binary=$(build_with_instance_domain "")
if [ -n "$empty_case_binary" ]; then
	assert_instance_domain "empty BLOCKS_INSTANCE_DOMAIN" "$empty_case_binary"
else
	fails=$((fails + 1))
fi

# An explicit domain must still reach the binary, so the two assertions above
# cannot be satisfied by dropping the ldflag altogether.
explicit_case_binary=$(build_with_instance_domain blocks.example)
if [ -n "$explicit_case_binary" ]; then
	assert_instance_domain "explicit BLOCKS_INSTANCE_DOMAIN" "$explicit_case_binary" blocks.example
else
	fails=$((fails + 1))
fi

# Set but malformed: a value that builds cleanly and passes a non-empty check, yet
# makes every short-name login resolve to a host nobody chose.
MALFORMED_DOMAIN='corp.example@collector.example'
malformed_case_binary=$(build_with_instance_domain "$MALFORMED_DOMAIN")
if [ -n "$malformed_case_binary" ]; then
	assert_instance_domain_rejected "malformed BLOCKS_INSTANCE_DOMAIN" "$malformed_case_binary" "$MALFORMED_DOMAIN"
else
	fails=$((fails + 1))
fi

# The rules the guard encodes, pinned directly. MAX_DOMAIN is 63+1+63+1+63+1+59 =
# 251 characters, the longest suffix that still leaves room for a one-character short
# name in front of it — the same boundary TestIsDNSSuffix pins on the Go side.
MAX_LABEL=$(printf '%063d' 0 | tr 0 a)
MAX_DOMAIN="${MAX_LABEL}.${MAX_LABEL}.${MAX_LABEL}.$(printf '%059d' 0 | tr 0 b)"

assert_domain_validator accept blocks.ai "the shipped default"
assert_domain_validator accept blocks.example.com "a three-label suffix"
assert_domain_validator accept example "a single label"
assert_domain_validator accept acme-2.example.test "hyphens and digits inside a label"
assert_domain_validator accept "$MAX_DOMAIN" "251 characters, which still leaves room for a label"
assert_domain_validator reject "" "an empty domain"
assert_domain_validator reject "$MALFORMED_DOMAIN" "userinfo before the host"
assert_domain_validator reject example.com/path "a path after the host"
assert_domain_validator reject https://example.com "a scheme"
assert_domain_validator reject "exam ple.com" "whitespace inside a label"
assert_domain_validator reject .example.com "a leading dot"
assert_domain_validator reject example.com. "a trailing dot"
assert_domain_validator reject example.com:8443 "a port"
assert_domain_validator reject "${MAX_LABEL}a.example.com" "a 64-character label"
assert_domain_validator reject -example.com "a leading hyphen"
assert_domain_validator reject "${MAX_DOMAIN}c" "252 characters, which leaves room for the dot but not the label"
assert_domain_validator reject "${MAX_DOMAIN}cc" "253 characters, a suffix that can expand nothing"
assert_domain_validator reject "${MAX_DOMAIN}ccc" "254 characters, past the hostname limit itself"

echo ""
echo "Passed: $passes  Failed: $fails"
rm -rf dist
[ "$fails" -eq 0 ]
