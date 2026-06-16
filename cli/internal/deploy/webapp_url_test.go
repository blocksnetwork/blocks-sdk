package deploy

import (
	"strings"
	"testing"
)

// TestValidateWebAppURL_Corpus mirrors the cross-layer corpus enforced by
// the JSON Schema pattern + Zod refine on the backend + new URL() on the
// frontend. Every entry below is shared so a future change to the rule
// must update all five places in lock-step.
func TestValidateWebAppURL_Corpus(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		// Production-shape https URLs.
		{"https://my-app.pages.dev", true},
		{"https://example.com/path?q=1", true},
		{"https://example.com:8443/path", true},
		{"https://example.com:65535", true},
		// Loopback http.
		{"http://localhost", true},
		{"http://localhost:5173", true},
		{"http://127.0.0.1:8080", true},
		{"http://[::1]", true},
		{"http://[::1]:1234/x", true},

		// Wrong scheme.
		{"http://example.com", false},
		{"http://attacker.example.com", false},
		{"ftp://example.com", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},

		// Bad shape / missing host.
		{"not-a-url", false},
		{"", false},
		{"https://", false},
		{"https:///path", false},
		{"https://?x", false},
		{"https://#frag", false},
		{"http:///localhost", false},

		// Port out of range — semantic, not caught by the pattern alone.
		{"https://example.com:0", false},
		{"https://example.com:65536", false},
		{"https://example.com:99999", false},
		{"http://localhost:65536", false},

		// Parser-rejected (semantic).
		{"https://%zz", false},
		{"https://[x]", false},
		{"https://[gggg::1]", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			err := ValidateWebAppURL(tc.url)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateWebAppURL(%q) err=%v, want ok=%v", tc.url, err, tc.ok)
			}
		})
	}
}

// TestValidateWebAppURL_ErrorMessages exercises the error-message branches
// so future churn (e.g. message rewording) doesn't accidentally drop the
// "must be 1-65535" hint or the loopback enumeration.
func TestValidateWebAppURL_ErrorMessages(t *testing.T) {
	cases := []struct {
		url      string
		contains string
	}{
		{"https://example.com:99999", "1-65535"},
		{"https://%zz", "invalid percent-encoding"},
		// Malformed IPv6 literals are caught by Go's url.Parse itself
		// (netip rejects them with "invalid host"). No custom validator
		// runs at this layer; the parser is sufficient.
		{"https://[gggg::1]", "invalid host"},
		{"https://[x]", "invalid host"},
		{"http://example.com", "loopback"},
		{"ftp://example.com", "scheme"},
		{"https://", "no host"},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			err := ValidateWebAppURL(tc.url)
			if err == nil {
				t.Fatalf("expected error for %q", tc.url)
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q should contain %q", err.Error(), tc.contains)
			}
		})
	}
}
