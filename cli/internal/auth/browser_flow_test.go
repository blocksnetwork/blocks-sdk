package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestGenerateCodeVerifier(t *testing.T) {
	v, err := generateCodeVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Errorf("verifier length %d not in [43,128]", len(v))
	}
}

func TestCodeChallengeS256(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := codeChallengeS256(verifier)

	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])

	if challenge != expected {
		t.Errorf("challenge = %q, want %q", challenge, expected)
	}
}

func TestGenerateState(t *testing.T) {
	s, err := generateState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) < 16 {
		t.Errorf("state too short: %d chars", len(s))
	}
}

func TestErrorPageEscapesHTML(t *testing.T) {
	payload := `</div><script>alert(1)</script>`
	page := errorPage(payload)
	if strings.Contains(page, payload) {
		t.Fatalf("errorPage did not escape raw HTML payload; injection possible:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("errorPage did not HTML-escape <script> tag; output:\n%s", page)
	}
}

func TestErrorPageEmbedsSharedAssets(t *testing.T) {
	page := errorPage("boom")
	if !strings.Contains(page, ".bg-grid") {
		t.Error("errorPage missing shared CSS (.bg-grid)")
	}
	if !strings.Contains(page, "requestAnimationFrame") {
		t.Error("errorPage missing shared JS (requestAnimationFrame)")
	}
	if !strings.Contains(page, "boom") {
		t.Error("errorPage dropped the error detail")
	}
}

func TestSuccessPageEmbedsSharedAssets(t *testing.T) {
	if !strings.Contains(callbackSuccessRendered, ".bg-grid") {
		t.Error("success page missing shared CSS (.bg-grid)")
	}
	if !strings.Contains(callbackSuccessRendered, "requestAnimationFrame") {
		t.Error("success page missing shared JS (requestAnimationFrame)")
	}
	if strings.Contains(callbackSuccessRendered, "{{.Styles}}") || strings.Contains(callbackSuccessRendered, "{{.Script}}") {
		t.Error("success page still contains unresolved template placeholders")
	}
}

func TestBrowserCommand(t *testing.T) {
	cases := []struct {
		goos     string
		wantCmd  string
		wantArgs []string
	}{
		{"darwin", "open", []string{"https://example.com"}},
		{"linux", "xdg-open", []string{"https://example.com"}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", "https://example.com"}},
		{"freebsd", "xdg-open", []string{"https://example.com"}},
		{"openbsd", "xdg-open", []string{"https://example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			cmd, args, err := browserCommand(tc.goos, "https://example.com")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, tc.wantCmd)
			}
			if !slices.Equal(args, tc.wantArgs) {
				t.Errorf("args = %v, want %v", args, tc.wantArgs)
			}
		})
	}
}

func TestBrowserCommandUnsupported(t *testing.T) {
	_, _, err := browserCommand("plan9", "https://example.com")
	if err == nil {
		t.Fatal("expected error for plan9, got nil")
	}
	if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("error %q should mention the unsupported platform name", err.Error())
	}
}

func TestMissingOpenerError(t *testing.T) {
	cases := []struct {
		goos     string
		wantHint string // substring expected in the error message; empty = no install hint
	}{
		{"freebsd", "pkg install xdg-utils"},
		{"openbsd", "pkg_add xdg-utils"},
		{"linux", "xdg-utils"},
		{"darwin", ""},
		{"windows", ""},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			err := missingOpenerError(tc.goos, "xdg-open")
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(err.Error(), "xdg-open") {
				t.Errorf("error %q should mention the opener name", err.Error())
			}
			if tc.wantHint == "" {
				if strings.Contains(err.Error(), "install") {
					t.Errorf("error %q should have no install hint for %s", err.Error(), tc.goos)
				}
				return
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("error %q missing expected hint %q for %s", err.Error(), tc.wantHint, tc.goos)
			}
		})
	}
}
