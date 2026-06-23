package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/spf13/pflag"
)

// isolateProfiles points the profile store (contexts.json) at a fresh temp file
// so the active-profile API-key resolution is hermetic and never reads the
// developer's real ~/.config/blocks/contexts.json. Returns a cleanup func.
func isolateProfiles(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) {
		return filepath.Join(dir, "contexts.json"), nil
	}
	return func() { profiles.ContextsPathFunc = orig }
}

// resetPublishFlags resets all publish command flag variables to defaults.
// Cobra does not re-apply defaults when Execute() is called again with different args.
func resetPublishFlags() {
	publishApiKey = ""
	publishApiKeyStdin = false
	publishListing = ""
	publishBillingMode = ""
	publishPrice = ""
	publishPricePerTask = ""
	publishPricePerMinute = ""
	publishFreeUnits = 0
	publishFreeTasks = 0
	publishFreeMinutes = 0
	publishAcceptTerms = false
	publishOrgName = ""
	publishCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
	openBrowserFunc = func(string) error { return nil }
}

func TestPublishWithListingPublic(t *testing.T) {
	old := Version
	Version = "1.0.0"
	defer func() { Version = old }()

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
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	cdm.Reset()

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "paid", "--price", "0.15", "--free-units", "10", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if received["listing"] != "public" {
		t.Errorf("listing = %v, want public", received["listing"])
	}
	if received["billingMode"] != "paid" {
		t.Errorf("billingMode = %v, want paid", received["billingMode"])
	}
	if received["pricePerTask"] != "0.150000" {
		t.Errorf("pricePerTask = %v, want 0.150000", received["pricePerTask"])
	}
	if received["freeTasksPerConsumer"] != float64(10) {
		t.Errorf("freeTasksPerConsumer = %v, want 10", received["freeTasksPerConsumer"])
	}
	if _, ok := received["tcAcceptedAt"]; !ok {
		t.Error("expected tcAcceptedAt in payload")
	}
	if !strings.Contains(output, "Visibility: Public") || !strings.Contains(output, "Billing: Paid") {
		t.Errorf("success output missing visibility/billing:\n%s", output)
	}
	if strings.Contains(strings.ToLower(output), "playground") {
		t.Errorf("success output must not contain playground wording:\n%s", output)
	}
	if strings.Contains(output, "View:") {
		t.Errorf("success output should omit View line when backend provides no URL:\n%s", output)
	}
}

func TestPublishRejectsPriceWithoutListing(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", "--price", "0.15"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --price used without --listing")
	}
}

// setupFakeCredentials creates a temporary credentials file and overrides
// CredentialPathFunc so that loadCredentials succeeds during testing.
func setupFakeCredentials(t *testing.T) func() {
	t.Helper()
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, "blocks", "credentials.json")
	origFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) {
		return credPath, nil
	}
	restoreProfiles := isolateProfiles(t)

	// Seed the legacy "blocks" slot so loadCredentials' migration fallback resolves
	// it (the profile store is isolated/empty via isolateProfiles).
	expiry := time.Now().Add(24 * time.Hour)
	if err := auth.SetProviderCredential(credPath, "blocks", &auth.ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     "bk_test_key",
		OrgId:      "org-test",
		ExpiresAt:  &expiry,
	}); err != nil {
		t.Fatalf("saving fake credentials: %v", err)
	}

	return func() {
		auth.CredentialPathFunc = origFunc
		restoreProfiles()
	}
}

// TestEnterpriseForcesFreeBillingMode asserts enterprise forces billing-mode=free
// (so the paid-only pricing/T&C prompts are skipped) while Blocks Network keeps
// its existing prompting behavior (no override).
func TestEnterpriseForcesFreeBillingMode(t *testing.T) {
	if got := enterpriseBillingOverride(true); got == nil || *got != "free" {
		t.Fatalf("enterprise should force billing-mode=free, got %v", got)
	}
	if got := enterpriseBillingOverride(false); got != nil {
		t.Fatalf("non-enterprise should not override billing-mode, got %v", got)
	}
}

// TestResolvePublishEnterprise covers enterprise classification for publish:
// a saved enterprise profile is authoritative, a pure stock target skips
// discovery, and a custom backend without an enterprise profile is resolved via
// a lenient cli-config discovery (errors default to Network).
func TestResolvePublishEnterprise(t *testing.T) {
	t.Run("enterprise profile is authoritative without discovery", func(t *testing.T) {
		defer isolateProfiles(t)()
		if err := profiles.Upsert("acme", profiles.Profile{Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, true); err != nil {
			t.Fatalf("seed: %v", err)
		}
		t.Setenv("BLOCKS_BACKEND_URL", "")
		// A dead backend would error if discovery ran — the profile short-circuits it.
		if !resolvePublishEnterprise("http://127.0.0.1:1/dead") {
			t.Fatal("enterprise profile should classify as enterprise")
		}
	})

	t.Run("stock target skips discovery and is non-enterprise", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		// Default profile has no BaseURL and no enterprise flag; with no custom
		// backend, discovery is skipped (the dead URL is never contacted).
		if resolvePublishEnterprise("http://127.0.0.1:1/dead") {
			t.Fatal("stock target must not be classified as enterprise")
		}
	})

	t.Run("custom backend discovers enterprise when no profile says so", func(t *testing.T) {
		defer isolateProfiles(t)()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/cli-config" {
				w.WriteHeader(200)
				w.Write([]byte(`{"enterprise":true}`))
				return
			}
			w.WriteHeader(404)
		}))
		defer srv.Close()
		t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
		if !resolvePublishEnterprise(srv.URL) {
			t.Fatal("custom backend with enterprise cli-config should classify as enterprise")
		}
	})

	t.Run("custom backend stays Network when discovery says so", func(t *testing.T) {
		defer isolateProfiles(t)()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte(`{"enterprise":false}`))
		}))
		defer srv.Close()
		t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
		if resolvePublishEnterprise(srv.URL) {
			t.Fatal("non-enterprise cli-config should classify as Network")
		}
	})

	t.Run("unreachable discovery is lenient and non-enterprise", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "http://127.0.0.1:1/dead")
		if resolvePublishEnterprise("http://127.0.0.1:1/dead") {
			t.Fatal("unreachable discovery must not block publish (defaults to Network)")
		}
	})
}

// TestPublishPayloadShape verifies that the publish envelope sent to the
// server includes cliVersion, protocolVersions, preferredProtocolVersion,
// and does NOT include instanceId.
func TestPublishPayloadShape(t *testing.T) {
	old := Version
	Version = "2.3.4-test"
	defer func() { Version = old }()

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli-config" {
			w.WriteHeader(200)
			w.Write([]byte(`{"enterprise":false}`))
			return
		}
		if r.URL.Path == "/api/v1/pricing/limits" {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path == "/api/v1/registry/publish-context" {
			w.WriteHeader(200)
			w.Write([]byte(`{"orgId":"org-test","orgName":"Test Org","agentCount":1}`))
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("unmarshalling request body: %v", err)
		}

		// Verify Blocks-Protocol-Version header
		hdr := r.Header.Get("Blocks-Protocol-Version")
		if hdr != registry.ProtocolVersion {
			t.Errorf("Blocks-Protocol-Version header = %q, want %q", hdr, registry.ProtocolVersion)
		}

		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish command failed: %v", err)
		}
	})

	// Check cliVersion
	cv, ok := received["cliVersion"]
	if !ok {
		t.Fatal("payload missing cliVersion field")
	}
	if cv != "2.3.4-test" {
		t.Errorf("cliVersion = %v, want %q", cv, "2.3.4-test")
	}

	// Check protocolVersions
	pvRaw, ok := received["protocolVersions"]
	if !ok {
		t.Fatal("payload missing protocolVersions field")
	}
	pvSlice, ok := pvRaw.([]interface{})
	if !ok {
		t.Fatalf("protocolVersions is not an array: %T", pvRaw)
	}
	if len(pvSlice) != 1 || pvSlice[0] != registry.ProtocolVersion {
		t.Errorf("protocolVersions = %v, want [%q]", pvSlice, registry.ProtocolVersion)
	}

	// Check preferredProtocolVersion
	ppv, ok := received["preferredProtocolVersion"]
	if !ok {
		t.Fatal("payload missing preferredProtocolVersion field")
	}
	if ppv != registry.ProtocolVersion {
		t.Errorf("preferredProtocolVersion = %v, want %q", ppv, registry.ProtocolVersion)
	}

	// agentName and card must be present
	if _, ok := received["agentName"]; !ok {
		t.Error("payload missing agentName")
	}
	if _, ok := received["card"]; !ok {
		t.Error("payload missing card")
	}

	// instanceId must NOT be present
	if _, ok := received["instanceId"]; ok {
		t.Error("payload must NOT contain instanceId")
	}
}

// TestPublishProtocolVersionHeader verifies that the Blocks-Protocol-Version
// header is sent on the publish request.
func TestPublishProtocolVersionHeader(t *testing.T) {
	old := Version
	Version = "1.0.0"
	defer func() { Version = old }()

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	headerSeen := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerSeen = r.Header.Get("Blocks-Protocol-Version")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish command failed: %v", err)
		}
	})

	if headerSeen != registry.ProtocolVersion {
		t.Errorf("Blocks-Protocol-Version header = %q, want %q", headerSeen, registry.ProtocolVersion)
	}
}

// TestPublishNoTTY_ApiKey verifies that "blocks publish --api-key" works
// without a TTY attached to stdin and sends the key in the Authorization header.
func TestPublishNoTTY_ApiKey(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	w.Close()
	defer func() { os.Stdin = origStdin; r.Close() }()

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	var authHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetPublishFlags()

	cmd := rootCmd
	cmd.SetArgs([]string{"publish", "--api-key", "test-key-from-agent", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("publish --api-key should succeed, got: %v", err)
		}
	})

	if authHeader != "Bearer test-key-from-agent" {
		t.Errorf("expected Authorization header %q, got %q", "Bearer test-key-from-agent", authHeader)
	}
}

// TestPublishNoTTY_ApiKeyStdin verifies that "blocks publish --api-key-stdin"
// works when stdin is a pipe (non-TTY) and sends the piped key in the request.
func TestPublishNoTTY_ApiKeyStdin(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	if _, err := w.Write([]byte("piped-agent-key\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer func() { os.Stdin = origStdin; r.Close() }()

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	var authHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetPublishFlags()

	cmd := rootCmd
	cmd.SetArgs([]string{"publish", "--api-key-stdin", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	captureStdout(func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("publish --api-key-stdin should succeed, got: %v", err)
		}
	})

	if authHeader != "Bearer piped-agent-key" {
		t.Errorf("expected Authorization header %q, got %q", "Bearer piped-agent-key", authHeader)
	}
}

// TestPublishNoListingNonInteractive verifies that publishing without --listing
// on a non-TTY stdin fails fast rather than silently publishing to the default
// public listing.
func TestPublishNoListingNonInteractive(t *testing.T) {
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

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", cardPath})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when publishing without --listing on non-TTY stdin")
	}
	if !strings.Contains(err.Error(), "Missing --listing") {
		t.Errorf("error = %q, want missing listing message", err.Error())
	}
}

// TestPublishBillingModeFreeFlag verifies --billing-mode free is accepted and
// the billingMode field appears in the payload.
func TestPublishBillingModeFreeFlag(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok","agentUrl":"https://app.example.com/agents/test_agent"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish --billing-mode free failed: %v", err)
		}
	})

	if received["billingMode"] != "free" {
		t.Errorf("billingMode = %v, want free", received["billingMode"])
	}
	if received["listing"] != "public" {
		t.Errorf("listing = %v, want public", received["listing"])
	}
	if _, ok := received["tcAcceptedAt"]; ok {
		t.Error("expected no tcAcceptedAt for free billing mode")
	}
	if !strings.Contains(output, "View: https://app.example.com/agents/test_agent") {
		t.Errorf("success output missing backend-provided agent URL:\n%s", output)
	}
}

func TestPublishedAgentURLResponseWinsOverAppBase(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "https://app-base.example.com")

	got := publishedAgentURL([]byte(`{"status":"ok","agentUrl":"https://backend.example.com/agents/test_agent"}`), "test_agent", false)
	want := "https://backend.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

func TestPublishedAgentURLUsesAppBaseFallback(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "https://app.example.com/")

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", false)
	want := "https://app.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

func TestPublishedAgentURLRejectsUnsafeBackendURL(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "https://app.example.com")

	got := publishedAgentURL([]byte(`{"status":"ok","agentUrl":"javascript:alert(1)"}`), "test_agent", false)
	want := "https://app.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want safe fallback %q", got, want)
	}
}

func TestSafeHTTPURLStripsControlChars(t *testing.T) {
	got := safeHTTPURL("https://app.example.com/agents/test_agent\x00")
	want := "https://app.example.com/agents/test_agent"
	if got != want {
		t.Errorf("safeHTTPURL = %q, want %q", got, want)
	}
}

func TestPublishedAgentURLOmitsWithoutResolution(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	cdm.Reset()

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", false)
	if got != "" {
		t.Errorf("publishedAgentURL = %q, want empty string", got)
	}
}

func TestPublishErrorCodeReadsStructuredData(t *testing.T) {
	payload := map[string]interface{}{
		"error": map[string]interface{}{
			"data": map[string]interface{}{
				"code": "BillingModeInvalid",
			},
		},
	}

	if got := publishErrorCode(payload); got != "BillingModeInvalid" {
		t.Errorf("publishErrorCode = %q, want BillingModeInvalid", got)
	}
}

func TestCenterTextUsesVisibleRuneWidth(t *testing.T) {
	got := centerText(boldText("猫"), 5)
	if !strings.HasPrefix(got, "  "+ansiBold) {
		t.Errorf("centerText prefix = %q, want two visible padding spaces before ANSI bold", got)
	}
}

// TestPublishBillingModePaidFlag verifies --billing-mode paid is accepted and
// tcAcceptedAt is included in the payload.
func TestPublishBillingModePaidFlag(t *testing.T) {
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

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "paid", "--price", "0.15", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish --billing-mode paid failed: %v", err)
		}
	})

	if received["billingMode"] != "paid" {
		t.Errorf("billingMode = %v, want paid", received["billingMode"])
	}
	if _, ok := received["tcAcceptedAt"]; !ok {
		t.Error("expected tcAcceptedAt for paid billing mode")
	}
}

// TestPublishPaidMissingAcceptTermsNonInteractive verifies that paid agents
// require --accept-terms in non-interactive mode, while free agents do not.
func TestPublishPaidMissingAcceptTermsNonInteractive(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	// Replace stdin with a pipe so isInteractive() returns false.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "paid", "--price", "1.00"})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --accept-terms omitted for paid agent in non-interactive mode")
	}
	wantSubstr := "--accept-terms"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want substring %q", err.Error(), wantSubstr)
	}
}

// TestPublishMissingBillingModeNonTTY verifies that a non-TTY publish
// (CI, scripted invocation) without --billing-mode fails fast even when
// --accept-terms is not set. The TTY signal alone must trigger
// non-interactive semantics. Regression test for the case where the
// previous code only treated --accept-terms as the non-interactive
// signal and silently fell into promptBillingMode → EOF in CI.
func TestPublishMissingBillingModeNonTTY(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	// Replace stdin with a pipe so isInteractive() returns false reliably
	// regardless of how this test is launched.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	resetPublishFlags()

	// No --accept-terms; --listing provided so we get past the listing gate.
	// With stdin redirected to a non-TTY pipe, the new
	// NonInteractive plumbing must fail fast on missing --billing-mode.
	rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public"})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --billing-mode omitted in non-TTY mode")
	}
	wantSubstr := "Missing --billing-mode"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want substring %q", err.Error(), wantSubstr)
	}
}

// TestPublishInvalidBillingModeNonInteractive verifies that an invalid
// --billing-mode value fails fast.
func TestPublishInvalidBillingModeNonInteractive(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "tier", "--accept-terms"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --billing-mode value")
	}
}

// TestPublishFreePrivatePayload verifies free+private emits a valid payload
// with no pricing and no tcAcceptedAt.
func TestPublishFreePrivatePayload(t *testing.T) {
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

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish private+free failed: %v", err)
		}
	})

	if received["listing"] != "private" {
		t.Errorf("listing = %v, want private", received["listing"])
	}
	if received["billingMode"] != "free" {
		t.Errorf("billingMode = %v, want free", received["billingMode"])
	}
	if _, ok := received["pricePerTask"]; ok {
		t.Error("expected no pricePerTask for free billing")
	}
	if _, ok := received["tcAcceptedAt"]; ok {
		t.Error("expected no tcAcceptedAt for free billing")
	}
}

// TestPublishPaidPrivatePayload verifies paid+private emits tcAcceptedAt
// (D3 paid-any-listing T&C regression).
func TestPublishPaidPrivatePayload(t *testing.T) {
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

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "paid", "--price", "0.15", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish private+paid failed: %v", err)
		}
	})

	if received["listing"] != "private" {
		t.Errorf("listing = %v, want private", received["listing"])
	}
	if received["billingMode"] != "paid" {
		t.Errorf("billingMode = %v, want paid", received["billingMode"])
	}
	if _, ok := received["tcAcceptedAt"]; !ok {
		t.Error("expected tcAcceptedAt for paid+private (D3 paid-any-listing)")
	}
}

// ─── shared-helper migration regression ──────────────────────────────────────

// TestPublishSharedHelperAttachesProtocolVersionHeader verifies that the
// publish command attaches the Blocks-Protocol-Version header on every
// outbound request through the shared blocksapi.Client (migration regression).
// Simulates a backend that rejects requests missing the header (HTTP 412).
func TestPublishSharedHelperAttachesProtocolVersionHeader(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	headerSeen := ""
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headerSeen = r.Header.Get("Blocks-Protocol-Version")
		if headerSeen == "" {
			// Simulate a backend that rejects missing header.
			w.WriteHeader(412)
			w.Write([]byte(`{"error":"Blocks-Protocol-Version required"}`))
			return
		}
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

	resetPublishFlags()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if headerSeen != registry.ProtocolVersion {
		t.Errorf("Blocks-Protocol-Version = %q, want %q (shared helper must auto-attach it)", headerSeen, registry.ProtocolVersion)
	}
}

// TestPublishNoCredentialsFails verifies that publish without stored credentials
// (and no --api-key flag) fails fast with an actionable error.
func TestPublishNoCredentialsFails(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")
	t.Setenv("BLOCKS_API_KEY", "")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when no credentials exist")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error should mention 'not authenticated', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "blocks login") {
		t.Errorf("error should mention 'blocks login', got: %s", err.Error())
	}
}

// TestPublishExpiredCredentialsFails verifies that publish with an expired
// active-profile org key fails fast with an actionable error. The expired key
// is filtered out by activeProfileAPIKey() (IsExpired), and the legacy
// fallback's expiry check yields the same "not authenticated — run blocks
// login" guidance, so the command never reaches the registry.
func TestPublishExpiredCredentialsFails(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "blocks", "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()

	// Seed the active profile's default org with an expired key.
	expired := profiles.Profile{
		DefaultOrgID: "org-test",
		Orgs: map[string]profiles.OrgKey{
			"org-test": {OrgName: "Test Org", ApiKey: "bk_expired_key", ExpiresAt: time.Now().Add(-1 * time.Hour)},
		},
	}
	if err := profiles.Upsert(profiles.DefaultProfile, expired, true); err != nil {
		t.Fatalf("seeding expired profile: %v", err)
	}

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")
	t.Setenv("BLOCKS_API_KEY", "")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when credentials are expired")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("error should mention 'not authenticated', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "blocks login") {
		t.Errorf("error should mention 'blocks login', got: %s", err.Error())
	}
}

// TestPublish401ReturnsActionableError verifies that a 401 from the registry
// results in a clear error message directing the user to 'blocks login'.
func TestPublish401ReturnsActionableError(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	var publishErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		publishErr = rootCmd.Execute()
	})

	if publishErr == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(publishErr.Error(), "blocks login") {
		t.Errorf("error should mention 'blocks login', got: %s", publishErr.Error())
	}
}

// TestPublish401WithApiKeyFlag verifies that a 401 when using --api-key
// tells the user to replace the key, not to run 'blocks login'.
func TestPublish401WithApiKeyFlag(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	var publishErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--api-key", "bad-key", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		publishErr = rootCmd.Execute()
	})

	if publishErr == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(publishErr.Error(), "--api-key was rejected") {
		t.Errorf("error should mention '--api-key was rejected', got: %s", publishErr.Error())
	}
	if strings.Contains(publishErr.Error(), "blocks login") {
		t.Errorf("error should NOT mention 'blocks login' for direct-key path, got: %s", publishErr.Error())
	}
}

// TestPublish401WithApiKeyStdin verifies that a 401 when using --api-key-stdin
// tells the user to replace the stdin key, not to run 'blocks login'.
func TestPublish401WithApiKeyStdin(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	// Provide a key on stdin
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString("bad-key-from-stdin\n")
	w.Close()
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	var publishErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--api-key-stdin", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		publishErr = rootCmd.Execute()
	})

	if publishErr == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(publishErr.Error(), "--api-key-stdin was rejected") {
		t.Errorf("error should mention '--api-key-stdin was rejected', got: %s", publishErr.Error())
	}
	if strings.Contains(publishErr.Error(), "blocks login") {
		t.Errorf("error should NOT mention 'blocks login' for stdin-key path, got: %s", publishErr.Error())
	}
}
