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
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/spf13/pflag"
)

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
	publishCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
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

	creds := &auth.Credentials{
		ApiKey:    "bk_test_key",
		OrgId:     "org-test",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := auth.Save(creds); err != nil {
		t.Fatalf("saving fake credentials: %v", err)
	}

	return func() { auth.CredentialPathFunc = origFunc }
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
// without a TTY attached to stdin.
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

	// Create a valid project and mock backend
	dir := writeValidProject(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

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

	creds, err := auth.Load()
	if err != nil {
		t.Fatalf("expected credentials to be saved, got: %v", err)
	}
	if creds.ApiKey != "test-key-from-agent" {
		t.Errorf("expected ApiKey %q, got %q", "test-key-from-agent", creds.ApiKey)
	}
}

// TestPublishNoTTY_ApiKeyStdin verifies that "blocks publish --api-key-stdin"
// works when stdin is a pipe (non-TTY).
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

	dir := writeValidProject(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

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

	creds, err := auth.Load()
	if err != nil {
		t.Fatalf("expected credentials to be saved, got: %v", err)
	}
	if creds.ApiKey != "piped-agent-key" {
		t.Errorf("expected ApiKey %q, got %q", "piped-agent-key", creds.ApiKey)
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

	got := publishedAgentURL([]byte(`{"status":"ok","agentUrl":"https://backend.example.com/agents/test_agent"}`), "test_agent")
	want := "https://backend.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

func TestPublishedAgentURLUsesAppBaseFallback(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "https://app.example.com/")

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent")
	want := "https://app.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

func TestPublishedAgentURLRejectsUnsafeBackendURL(t *testing.T) {
	t.Setenv("BLOCKS_APP_BASE_URL", "https://app.example.com")

	got := publishedAgentURL([]byte(`{"status":"ok","agentUrl":"javascript:alert(1)"}`), "test_agent")
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

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent")
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

// TestPublishMissingBillingModeNonInteractive verifies that --billing-mode is
// required in non-interactive mode (--accept-terms) and fails with the
// actionable error message.
func TestPublishMissingBillingModeNonInteractive(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()

	rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--accept-terms"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --billing-mode omitted in non-interactive mode")
	}
	wantSubstr := "Missing --billing-mode"
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
