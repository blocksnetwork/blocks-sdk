package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/spf13/pflag"
)

// resetRegisterFlags resets the register command flag variables to defaults.
// Cobra does not re-apply defaults across Execute() calls in the same process.
func resetRegisterFlags() {
	registerApiKey = ""
	registerApiKeyStdin = false
	registerOrgName = ""
	registerCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
	openBrowserFunc = func(string) error { return nil }
}

// TestRegisterPayloadPrivateFree verifies that `blocks register` always POSTs
// listing=private, billingMode=free, and no pricing / tcAcceptedAt fields.
func TestRegisterPayloadPrivateFree(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"register", cardPath})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("register failed: %v", err)
		}
	})

	if received["listing"] != "private" {
		t.Errorf("listing = %v, want private", received["listing"])
	}
	if received["billingMode"] != "free" {
		t.Errorf("billingMode = %v, want free", received["billingMode"])
	}
	if _, ok := received["pricePerTask"]; ok {
		t.Error("expected no pricePerTask for register")
	}
	if _, ok := received["pricePerMinute"]; ok {
		t.Error("expected no pricePerMinute for register")
	}
	if _, ok := received["tcAcceptedAt"]; ok {
		t.Error("expected no tcAcceptedAt for register (free agents skip T&C)")
	}
	if received["agentName"] == nil || received["card"] == nil {
		t.Error("payload missing agentName/card (shared envelope must still populate them)")
	}
	if !strings.Contains(output, "Visibility: Private") || !strings.Contains(output, "Billing: Free") {
		t.Errorf("success output missing Private/Free:\n%s", output)
	}
	if !strings.Contains(output, "blocks publish") {
		t.Errorf("success output should hint at `blocks publish` to go public:\n%s", output)
	}
}

// TestRegisterHasNoPublicPaidFlags verifies the register command exposes none
// of the public/paid flags, so the suggested flow cannot reach those paths.
func TestRegisterHasNoPublicPaidFlags(t *testing.T) {
	forbidden := []string{
		"listing", "billing-mode", "price", "price-per-task",
		"price-per-minute", "free-units", "free-tasks", "free-minutes",
		"accept-terms",
	}
	for _, name := range forbidden {
		if registerCmd.Flags().Lookup(name) != nil {
			t.Errorf("register must not expose --%s", name)
		}
	}
	// Sanity: the flags it should have.
	for _, name := range []string{"api-key", "api-key-stdin", "org-name"} {
		if registerCmd.Flags().Lookup(name) == nil {
			t.Errorf("register should expose --%s", name)
		}
	}
}

// TestRegisterNonInteractiveNoFlagsRequired verifies that register succeeds on
// a non-TTY stdin with no visibility/billing flags — the key UX win over
// publish, which fails fast without --listing/--billing-mode.
func TestRegisterNonInteractiveNoFlagsRequired(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = origStdin; r.Close() }()

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"register", cardPath})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("register should succeed non-interactively with no flags, got: %v", err)
		}
	})

	if received["listing"] != "private" || received["billingMode"] != "free" {
		t.Errorf("non-interactive register payload = listing:%v billing:%v, want private/free",
			received["listing"], received["billingMode"])
	}
}

// TestRegisterApiKeyFlow verifies register resolves --api-key and sends it as a
// Bearer token, reusing the shared resolver/submit path.
func TestRegisterApiKeyFlow(t *testing.T) {
	var authHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"register", cardPath, "--api-key", "reg-test-key"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("register --api-key failed: %v", err)
		}
	})

	if authHeader != "Bearer reg-test-key" {
		t.Errorf("Authorization = %q, want %q", authHeader, "Bearer reg-test-key")
	}
}

// TestRegisterInvalidCardFails verifies register surfaces card validation
// errors via the same shim path as publish.
func TestRegisterInvalidCardFails(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := t.TempDir()
	cardPath := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(cardPath, []byte(`{"identity":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	var regErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"register", cardPath})
		regErr = rootCmd.Execute()
	})
	if regErr == nil {
		t.Fatal("expected validation error for an invalid card")
	}
}

// TestRegisterErrorPrefixSaysRegister verifies that a registry-side failure
// surfaces as "register failed: ..." (not "publish failed: ...") when the user
// ran `blocks register`, so the error message matches the command they typed.
// Regression test for the case where a paid-only agent rejected a free-register
// attempt and the prefix incorrectly read "publish failed".
func TestRegisterErrorPrefixSaysRegister(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		w.Write([]byte(`{"error":{"code":"BillingModeInvalid","message":"agent is paid"}}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	var regErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"register", cardPath})
		regErr = rootCmd.Execute()
	})

	if regErr == nil {
		t.Fatal("expected error when backend rejects free register of a paid agent")
	}
	if !strings.HasPrefix(regErr.Error(), "register failed:") {
		t.Errorf("error prefix should be 'register failed:', got: %s", regErr.Error())
	}
	if strings.Contains(regErr.Error(), "re-publishing") {
		t.Errorf("error should not say 're-publishing' on the register path, got: %s", regErr.Error())
	}
}

// TestRegisterNoCredentialsFails verifies register without credentials fails
// fast with the same actionable 'not authenticated' guidance as publish.
func TestRegisterNoCredentialsFails(t *testing.T) {
	defer isolateProfiles(t)()

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")
	t.Setenv("BLOCKS_API_KEY", "")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetRegisterFlags()

	rootCmd.SetArgs([]string{"register"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when no credentials exist")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error should mention 'not authenticated', got: %s", err.Error())
	}
}
