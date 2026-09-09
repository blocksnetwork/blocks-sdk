package origin

import (
	"net/url"
	"strings"
	"testing"
)

// Whether url.Parse rejects a malformed bracketed host is not stable across Go releases:
// some accept `https://[x]` (Host "[x]", Hostname "x") and leave the rule to Validate,
// others reject it in the parser. Either refuses, but the reason a caller reads must not
// change with the toolchain — a test asserting the narrower reason passed locally on
// go1.24.0 and failed in CI, which is what this pins.
func TestABracketedHostGivesTheSameReasonWhicheverLayerCatchesIt(t *testing.T) {
	for _, raw := range []string{"https://[x]", "https://[gggg::1]", "https://[not-an-ip]:8443"} {
		err := Validate(raw)
		if err == nil {
			t.Errorf("Validate(%q) = nil, want a refusal", raw)
			continue
		}
		// Holds whichever layer refused it.
		if !strings.Contains(err.Error(), "invalid host") {
			t.Errorf("Validate(%q) = %q, want it to name the host as invalid", raw, err)
		}
		if !strings.Contains(err.Error(), "IP literal") {
			t.Errorf("Validate(%q) = %q, want it to say a bracketed host must be an IP literal", raw, err)
		}
	}
}

// The independent check has to agree with the parser wherever the parser has an opinion,
// or the two would disagree about the same input on different Go versions.
func TestMalformedBracketedHostAgreesWithTheParserWhereItParses(t *testing.T) {
	cases := map[string]bool{
		"https://[x]":                    true,
		"https://[gggg::1]":              true,
		"https://[":                      true,  // unbalanced
		"https://[::1]":                  false, // a real literal
		"https://[::1]:8443":             false,
		"https://[2001:db8::1]/tenant-a": false,
		"https://backend.acme.example":   false, // not bracketed at all
		"https://user@[::1]":             false, // userinfo stripped before the host
		"https://host/[x]":               false, // brackets in the path are not the host
	}
	for raw, want := range cases {
		if got := malformedBracketedHost(raw); got != want {
			t.Errorf("malformedBracketedHost(%q) = %v, want %v", raw, got, want)
		}
		// Where url.Parse accepts the input, its own view of the host must match ours.
		if u, err := url.Parse(raw); err == nil && strings.HasPrefix(u.Host, "[") {
			viaParser := !bracketedHostIsAnIPLiteral(u.Host, u.Hostname())
			if viaParser != want {
				t.Errorf("%q: parser-based check says %v, independent check says %v", raw, viaParser, want)
			}
		}
	}
}

// A legitimate origin is still accepted, including the forms the rules allow.
func TestValidAcceptsTheAllowedForms(t *testing.T) {
	for _, raw := range []string{
		"https://backend.acme.example",
		"https://backend.acme.example:8443/tenant-a",
		"http://localhost:3001",
		"http://127.0.0.1",
		"http://[::1]:3001",
	} {
		if err := Validate(raw); err != nil {
			t.Errorf("Validate(%q) = %v, want accepted", raw, err)
		}
	}
}
