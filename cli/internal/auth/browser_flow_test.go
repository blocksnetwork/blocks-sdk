package auth

import (
	"crypto/sha256"
	"encoding/base64"
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
