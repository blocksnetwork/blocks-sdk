// Package origin validates deployment origins — the values the CLI appends endpoint
// paths to and sends credentials at.
//
// It exists so there is one answer to "is this a deployment origin". The check began as
// validation of the `blocks login` argument, where the value is typed and obviously
// attacker-influenceable. But the same value reaches the same requests from tiers nobody
// types: an exported BLOCKS_BACKEND_URL, a stored profile written before this check
// existed, a build-time linker default, and the api.baseUrl a CDM payload carries. A
// check that lives at one entry point is a check the other tiers do not get.
package origin

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// maxTCPPort is the highest port number a TCP address can name.
const maxTCPPort = 65535

// hostReservedChars are the characters that stop a string meaning a bare host once a
// scheme is put in front of it.
const hostReservedChars = "/@?#\\"

// Validate reports whether raw is usable as a deployment origin, and why not when it is
// not. The error is phrased to be shown to a user, and names the rule rather than the
// input, so a caller can prefix it with wherever the value came from.
//
// The rules, and why each is here rather than left to fail later:
//
//   - https, or http only for a loopback host. This is what keeps a credential from
//     going out in clear text to anywhere but the machine it was typed on.
//   - No userinfo. `https://trusted.example@collector.example` reaches
//     collector.example while reading as trusted.example, so an origin carrying it is
//     not the one it appears to name.
//   - No query and no fragment. An origin has endpoint paths appended to it, so
//     `https://host?x=1` would send every request to `https://host?x=1/api/v1/...`, a
//     path no deployment serves. A fragment additionally comments the path out.
//   - A port inside 1–65535. url.Parse only requires digits, so `https://host:99999`
//     is accepted as a URL and fails much later, inside the first request.
//   - A bracketed authority must really contain an IP literal. Brackets are the IPv6
//     syntax and url.Parse checks only that they are balanced, so `https://[x]` parses
//     with Host `[x]` and Hostname `x` — a hostname that resolves nowhere near what it
//     appears to say.
func Validate(raw string) error {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil {
		// Whether url.Parse rejects a malformed bracketed host is not stable across Go
		// releases, so the reason is established here rather than left to the parser. The
		// bracketed rule below is the one that fires when the parser accepts it; this is the
		// same rule stated for the case where the parser does not, so the message a caller
		// sees is the same either way. Without this the reason depended on the toolchain,
		// which is how a test passed locally and failed in CI.
		if malformedBracketedHost(trimmed) {
			return fmt.Errorf("has an invalid host: a bracketed host must be an IP literal")
		}
		// Otherwise the parse error is the only thing that says why, so it is wrapped
		// rather than discarded.
		return fmt.Errorf("is not a valid URL: %w", err)
	}
	if u.User != nil {
		return fmt.Errorf("must not carry a username or password before the host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("must be an origin with no query string and no fragment")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("must be an absolute URL with a host")
	}
	if !plausibleHost(u.Host) {
		return fmt.Errorf("has a host that is not a host")
	}
	if !deploymentPort(u.Port()) {
		return fmt.Errorf("has a port outside 1-%d", maxTCPPort)
	}
	if !bracketedHostIsAnIPLiteral(u.Host, host) {
		// Deliberately worded to share "invalid host" with the parse error above, because
		// which of the two fires depends on the Go patch level: 1.24.0 parses
		// `https://[x]` cleanly (Host "[x]", Hostname "x") and this rule catches it, while
		// later 1.24.x reject it inside url.Parse and never reach here. Both refuse; a
		// caller or test that wants to assert the reason needs one phrase that holds on
		// either, or it passes locally and fails in CI — which is exactly what happened.
		return fmt.Errorf("has an invalid host: a bracketed host must be an IP literal")
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && IsLoopbackHost(strings.ToLower(host)):
		return nil
	case u.Scheme == "http":
		return fmt.Errorf("must use https: (http: is only allowed for a loopback host)")
	default:
		return fmt.Errorf("must use https:, or http: with a loopback host; got scheme %q", u.Scheme)
	}
}

// malformedBracketedHost reports whether raw carries a bracketed authority that does not
// contain an IP literal, decided without url.Parse.
//
// It exists because url.Parse's treatment of `https://[x]` is not stable across Go
// releases: some accept it (Host "[x]", Hostname "x") and leave the rule to us, others
// reject it themselves. Either is fine as a refusal, but the *reason* a caller reads
// should not change with the toolchain, so this establishes it independently.
//
// The authority is taken by hand: strip the scheme, drop any userinfo, and stop at the
// first path, query or fragment delimiter.
func malformedBracketedHost(raw string) bool {
	authority := raw
	if i := strings.Index(authority, "://"); i >= 0 {
		authority = authority[i+3:]
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	if !strings.HasPrefix(authority, "[") {
		return false
	}
	end := strings.Index(authority, "]")
	if end < 0 {
		// An unbalanced bracket is not a host under either reading.
		return true
	}
	return net.ParseIP(authority[1:end]) == nil
}

// Valid is Validate as a predicate, for callers that only branch on the answer.
func Valid(raw string) bool { return Validate(raw) == nil }

// IsLoopbackHost reports whether host is one of the three spellings of the local
// machine — the only hosts a plain-http origin may name. Callers pass url.Hostname(),
// which strips IPv6 brackets, so "::1" rather than "[::1]" is the form seen here.
func IsLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func plausibleHost(s string) bool {
	if strings.ContainsAny(s, hostReservedChars) {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func deploymentPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= maxTCPPort
}

func bracketedHostIsAnIPLiteral(authority, hostname string) bool {
	if !strings.HasPrefix(authority, "[") {
		return true
	}
	return net.ParseIP(hostname) != nil
}
