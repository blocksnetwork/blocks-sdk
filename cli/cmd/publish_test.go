package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
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

// validProjectAgentName is the agent writeValidProject's card declares, and so the name
// a registration mock echoes back: a deployment reports the agent it registered.
const validProjectAgentName = "test_agent"

// agentRegisterResponse is the canonical body of POST /api/v1/registry/agents. The three
// fields without an omitempty are required by the response contract — status is only
// ever "ok", and ts is the millisecond time the registration was recorded — so a body
// missing any of them is not one a deployment produces. The other two are the only
// further properties the contract permits; nothing else may appear.
type agentRegisterResponse struct {
	AgentName        string `json:"agentName"`
	Status           string `json:"status"`
	Ts               int64  `json:"ts"`
	EffectiveListing string `json:"effectiveListing,omitempty"`
	BillingMode      string `json:"billingMode,omitempty"`
}

// registeredResponseBody encodes that contract once rather than spelling a JSON literal
// at each mock, so no single mock can drift from it.
func registeredResponseBody(agentName string) []byte {
	body, err := json.Marshal(agentRegisterResponse{
		AgentName: agentName,
		Status:    "ok",
		Ts:        time.Now().UnixMilli(),
	})
	if err != nil {
		// Two strings and an int: unreachable, and a mock that could not describe the
		// contract must not quietly answer something else.
		panic(err)
	}
	return body
}

// writeRegistered answers a registration the way a deployment does. Every success mock in
// this file goes through it.
//
// They used to answer `{"status":"ok"}` — a body carrying neither the agent name nor the
// timestamp the response contract requires. That passed only because the command decodes
// into a map it reads no required field of, which is exactly why it was worth fixing: the
// mocks are the only statement of the response contract these tests make, and ones that
// omit required fields let a decoder start depending on them with no test noticing. The
// correction changes no behaviour today.
func writeRegistered(w http.ResponseWriter) {
	w.WriteHeader(http.StatusCreated)
	w.Write(registeredResponseBody(validProjectAgentName))
}

// The fixture is only worth correcting if something holds it to the contract, so the
// shape is asserted directly against both halves of it: every required property present,
// status the one permitted value, and no property the contract does not list. Without
// this the fields are inert — nothing in the publish path reads them — and `{}` could
// come back unnoticed.
func TestRegisteredResponseFixtureMatchesTheRegistrationContract(t *testing.T) {
	var got map[string]interface{}
	if err := json.Unmarshal(registeredResponseBody("my_agent"), &got); err != nil {
		t.Fatalf("the registration fixture must be a JSON object: %v", err)
	}
	for _, required := range []string{"agentName", "status", "ts"} {
		if _, ok := got[required]; !ok {
			t.Errorf("the fixture omits %q, which the response contract requires", required)
		}
	}
	permitted := map[string]bool{
		"agentName": true, "status": true, "ts": true,
		"effectiveListing": true, "billingMode": true,
	}
	for key := range got {
		if !permitted[key] {
			t.Errorf("the fixture carries %q, which the response contract does not permit", key)
		}
	}
	if got["agentName"] != "my_agent" {
		t.Errorf("agentName = %v, want the agent the registration named", got["agentName"])
	}
	if got["status"] != "ok" {
		t.Errorf("status = %v, want %q — the contract's only permitted value", got["status"], "ok")
	}
	ts, ok := got["ts"].(float64)
	if !ok || ts <= 0 {
		t.Errorf("ts = %v, want the millisecond time the registration was recorded", got["ts"])
	}
}

// resetPublishFlags resets all publish command flag variables to defaults.
// Cobra does not re-apply defaults when Execute() is called again with different args.
//
// Call it on entry AND from t.Cleanup, at every site. The two do different jobs: the
// entry call protects this test from whatever ran before it, and only the cleanup
// protects everything after it from this one. A test with the entry call alone is correct
// on its own and still leaks — `go test -shuffle=on` then reorders it in front of a test
// that does not reset, and the failure lands on the innocent one. The cleanup form is
// used rather than defer because it also runs when the test fails, which is exactly when
// a leaked flag is most likely.
//
// The state is package-level and read through cobra's own flag values, so it is not
// confined to a command run: publishApiKeyStdin is never read as a variable at all, it is
// read back off the flag it is bound to, which means a leaked `true` makes the next
// publish believe it was asked to consume stdin.
//
// One resetter per set, and this is the publish set's. A second helper covering some of
// the same variables is how a partial reset becomes indistinguishable from none.
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
		writeRegistered(w)
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
	t.Cleanup(resetPublishFlags)

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
	// When the backend response carries no agentUrl, the View link
	// falls back to the active deployment origin (BLOCKS_BACKEND_URL here), not
	// stock app.blocks.ai and not an omitted line.
	wantView := "View: " + ts.URL + "/agents/test_agent"
	if !strings.Contains(output, wantView) {
		t.Errorf("success output should show View line at the deployment origin (%q):\n%s", wantView, output)
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
	t.Cleanup(resetPublishFlags)

	rootCmd.SetArgs([]string{"publish", "--price", "0.15"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --price used without --listing")
	}
}

// setupFakeCredentials authenticates the invocation under test against whatever
// backend the test points BLOCKS_BACKEND_URL at. It seeds a temporary legacy
// credentials file (overriding CredentialPathFunc) and isolates the profile store,
// and it exports the same key as BLOCKS_API_KEY.
//
// The ambient key is what makes the credential belong to the deployment under
// test. Tests that use this helper point BLOCKS_BACKEND_URL at a local httptest
// server that no saved profile describes, and a key held only in local storage
// belongs to whatever deployment the profile that holds it names — so the resolver
// declines to send it elsewhere. `BLOCKS_BACKEND_URL` plus `BLOCKS_API_KEY` is the
// supported way to authenticate against a deployment you have not logged in to,
// which is exactly the situation these tests set up.
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
	t.Setenv("BLOCKS_API_KEY", "bk_test_key")

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

// TestPublishEnterpriseClassification covers enterprise classification for publish:
// a saved enterprise profile is authoritative, a pure stock target skips
// discovery, and a custom backend without an enterprise profile is resolved via
// a lenient cli-config discovery (errors default to Network).
func TestPublishEnterpriseClassification(t *testing.T) {
	t.Run("enterprise profile is authoritative without discovery", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Cleanup(clictx.Reset)
		// The profile records the deployment it describes. It used to be seeded with
		// Enterprise:true and no BaseURL, which is not a state the CLI produces —
		// mergeDiscovery sets a BaseURL for every enterprise login and clears the flag
		// for an explicit Network choice — and it only classified as enterprise because
		// a profile was then assumed to describe whatever the local tiers resolved,
		// including this build's linker default. That assumption is the credential
		// crossing profileDescribesLocalTarget closes.
		if err := profiles.Upsert("acme", profiles.Profile{
			Enterprise: true,
			BaseURL:    "http://127.0.0.1:1/dead",
			Orgs:       map[string]profiles.OrgKey{},
		}, true); err != nil {
			t.Fatalf("seed: %v", err)
		}
		t.Setenv("BLOCKS_BACKEND_URL", "")
		// The profile's own deployment is dead, so discovery would error if it ran — the
		// saved verdict short-circuits it, which is what this asserts.
		clictx.Resolve(&clictx.Overrides{})
		if !clictx.Enterprise() {
			t.Fatal("enterprise profile should classify as enterprise")
		}
	})

	t.Run("stock target skips discovery and is non-enterprise", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Cleanup(clictx.Reset)
		t.Setenv("BLOCKS_BACKEND_URL", "")
		// Default profile has no BaseURL and no enterprise flag; with no custom
		// backend, discovery is skipped (the dead URL is never contacted).
		clictx.Resolve(&clictx.Overrides{DefaultBackendURL: "http://127.0.0.1:1/dead"})
		if clictx.Enterprise() {
			t.Fatal("stock target must not be classified as enterprise")
		}
	})

	t.Run("custom backend discovers enterprise when no profile says so", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Cleanup(clictx.Reset)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/cli-config" {
				w.WriteHeader(200)
				w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
				return
			}
			w.WriteHeader(404)
		}))
		defer srv.Close()
		t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
		clictx.Resolve(&clictx.Overrides{BackendURL: srv.URL})
		if !clictx.Enterprise() {
			t.Fatal("custom backend with enterprise cli-config should classify as enterprise")
		}
	})

	t.Run("custom backend stays Network when discovery says so", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Cleanup(clictx.Reset)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte(`{"enterprise":false,"productName":"Blocks Network"}`))
		}))
		defer srv.Close()
		t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
		clictx.Resolve(&clictx.Overrides{BackendURL: srv.URL})
		if clictx.Enterprise() {
			t.Fatal("non-enterprise cli-config should classify as Network")
		}
	})

	t.Run("unreachable discovery is lenient and non-enterprise", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Cleanup(clictx.Reset)
		t.Setenv("BLOCKS_BACKEND_URL", "http://127.0.0.1:1/dead")
		clictx.Resolve(&clictx.Overrides{BackendURL: "http://127.0.0.1:1/dead"})
		if clictx.Enterprise() {
			t.Fatal("unreachable discovery must not block publish (defaults to Network)")
		}
	})
}

// TestPublishPayloadShape verifies that the publish envelope sent to the
// server includes cliVersion, protocolVersions, preferredProtocolVersion,
// and does NOT include the instance id.
//
// It also pins the organization id the publish context reports. The mock used to
// answer with `orgID` while the schema, the backend and the decoder all say `orgId`,
// so the decode yielded an empty id — and every step that addresses the organization
// by id ran against "" without the test noticing. --org-name is passed for that
// reason: it makes publish address the organization it was told about, so the
// spelling is load-bearing rather than decoration.
func TestPublishPayloadShape(t *testing.T) {
	old := Version
	Version = "2.3.4-test"
	defer func() { Version = old }()

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	var orgsAddressed []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli-config" {
			w.WriteHeader(200)
			w.Write([]byte(`{"enterprise":false,"productName":"Blocks Network"}`))
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
		if strings.HasPrefix(r.URL.Path, "/api/v1/orgs/") {
			orgsAddressed = append(orgsAddressed, r.Method+" "+r.URL.Path)
			w.WriteHeader(200)
			// The update response wraps the organization it changed, and that object
			// carries its slug and types alongside the id and name. The command reads
			// none of it — only the status code — so the shape is the contract's alone
			// to state.
			w.Write([]byte(`{"org":{"id":"org-test","name":"Renamed Org","slug":"renamed-org","types":["provider"]}}`))
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

		writeRegistered(w)
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
	// Also on the way out: --org-name is sticky, and a later publish that inherited it
	// would try to rename an organization it was never asked to.
	t.Cleanup(resetPublishFlags)

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms", "--org-name", "Renamed Org"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish command failed: %v", err)
		}
	})

	// The organization the publish context named must be the one addressed. An empty
	// id here means the response was decoded under the wrong field name.
	if want := []string{"PATCH /api/v1/orgs/org-test"}; len(orgsAddressed) != 1 || orgsAddressed[0] != want[0] {
		t.Errorf("organization requests = %v, want %v", orgsAddressed, want)
	}

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

	// The instance id must NOT be present. `instanceId` is the canonical wire spelling —
	// the same lowerCamelCase rule as `orgId` above — and the Go-style `instanceID` is
	// checked too, so a reintroduction under either spelling fails here rather than
	// slipping past an assertion naming a key the server can never send.
	for _, forbidden := range []string{"instanceId", "instanceID"} {
		if _, ok := received[forbidden]; ok {
			t.Errorf("payload must NOT contain %s", forbidden)
		}
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
		writeRegistered(w)
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
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
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
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
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
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
	// Nothing else may name a dashboard origin, so the View line below can only have
	// come from the deployment this publish targeted.
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

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
	// The registration response has no field for an agent URL, so the View line is built
	// from the deployment origin — the same fallback TestPublishWithListingPublic asserts.
	// This mock used to answer with an `agentUrl`, a property the response contract does
	// not carry; that the CLI would prefer one if a deployment ever sent it stays covered
	// by TestPublishedAgentURLResponseWinsOverAppBase, which exercises the decoder
	// directly rather than claiming a deployment produces such a body.
	wantView := "View: " + ts.URL + "/agents/test_agent"
	if !strings.Contains(output, wantView) {
		t.Errorf("success output should show the View line at the deployment origin (%q):\n%s", wantView, output)
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

// safeHTTPURL guards the one value this package hands to the operating system's URL
// handler, so it is held to the deployment-origin rule `blocks login` and
// `blocks dashboard` already apply — but to the ORIGIN only. What it guards is a page
// link rather than an origin endpoint paths get appended to, so a query and a fragment
// are ordinary here and must survive; the authority and the scheme are what must not be
// taken on trust.
func TestSafeHTTPURLHoldsTheOriginToTheDeploymentRule(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"userinfo moves the authority past the apparent host", "https://app.acme.example@evil.example/x", ""},
		{"userinfo with a password", "https://app.acme.example:tok@evil.example/x", ""},
		{"plaintext http to a remote host", "http://app.acme.example/agents/a", ""},
		{"a port outside the dialable range", "https://app.acme.example:99999/agents/a", ""},
		{"a scheme the operating system dispatches elsewhere", "javascript:alert(1)", ""},
		{"no authority at all", "/agents/a", ""},
		{"https is kept whole", "https://app.acme.example/agents/a", "https://app.acme.example/agents/a"},
		{"a query addresses a tab and must survive", "https://app.acme.example/agents/a?tab=tasks", "https://app.acme.example/agents/a?tab=tasks"},
		{"a fragment addresses an anchor and must survive", "https://app.acme.example/agents/a#io", "https://app.acme.example/agents/a#io"},
		{"a custom port is what a deployment is reached at", "https://app.acme.example:8443/agents/a", "https://app.acme.example:8443/agents/a"},
		{"loopback may be plaintext", "http://localhost:3000/agents/a", "http://localhost:3000/agents/a"},
		{"and so may its other two spellings", "http://127.0.0.1:3000", "http://127.0.0.1:3000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := safeHTTPURL(c.in); got != c.want {
				t.Errorf("safeHTTPURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// The "View" link a completed publish prints is one the deployment's own registration
// response can name, and the browser opener does not fetch it — it asks the operating
// system to dispatch it. So the authority has to be the host the link appears to name:
// everything before the `@` in https://app.acme.example@evil.example/x is userinfo, and
// the site that opens is evil.example.
//
// A refusal must not fail the command. The agent is registered on the deployment by the
// time this response is read, so the publish still succeeds; the link is printed
// inertly, with the reason it was not opened, and the user judges it.
func TestPublishOpensOnlyAnAgentURLWhoseAuthorityIsItsApparentHost(t *testing.T) {
	cases := []struct {
		name     string
		agentURL string
		open     bool
	}{
		{"userinfo moves the authority past the apparent host", "https://app.acme.example@evil.example/x", false},
		{"plaintext http to a remote host", "http://app.acme.example/agents/test_agent", false},
		{"an ordinary page link carrying a query", "https://app.acme.example/agents/test_agent?tab=tasks", true},
		{"loopback may be plaintext", "http://localhost:3000/agents/test_agent", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			restoreCLIState(t)
			t.Cleanup(setupFakeCredentials(t))

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/registry/agents" {
					// Only the registration is answered. A mock that answered every path
					// would also answer the publish-context fetch, and this flow is
					// interactive: a context reporting no agents starts the org-name
					// prompt, which has nothing to read from.
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				// Deliberately off-contract. The registration response carries no URL
				// property at all — registeredResponseBody states the real shape — so
				// answering one is the only way to reach the decoder that reads one.
				fmt.Fprintf(w, `{"agentName":%q,"status":"ok","ts":1,"agentUrl":%q}`,
					validProjectAgentName, c.agentURL)
			}))
			defer ts.Close()

			dir := writeValidProject(t)
			cardPath := filepath.Join(dir, "agent-card.json")
			t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
			// Nothing else may name a dashboard origin, so a link that is opened can
			// only be the one the response named.
			t.Setenv("BLOCKS_APP_BASE_URL", "")
			t.Setenv("BLOCKS_DASHBOARD_URL", "")
			t.Chdir(dir)

			resetPublishFlags()
			t.Cleanup(resetPublishFlags)
			// The opener is consulted only in an interactive session, so the property
			// under test is unreachable without this.
			isInteractive = func() bool { return true }
			var opened []string
			openBrowserFunc = func(u string) error { opened = append(opened, u); return nil }

			output := captureStdout(func() {
				rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
				if err := rootCmd.Execute(); err != nil {
					t.Fatalf("the agent is registered, so the publish must report success: %v", err)
				}
			})

			if want := "View: " + c.agentURL; !strings.Contains(output, want) {
				t.Errorf("the link the deployment named must be shown as %q:\n%s", want, output)
			}
			if c.open {
				if len(opened) != 1 || opened[0] != c.agentURL {
					t.Fatalf("opened = %q, want exactly [%q]", opened, c.agentURL)
				}
				if strings.Contains(output, declinedAgentURLNote) {
					t.Errorf("an accepted link must not be explained as declined:\n%s", output)
				}
				return
			}
			if len(opened) != 0 {
				t.Fatalf("opened = %q, want nothing reaching the opener at all", opened)
			}
			if !strings.Contains(output, declinedAgentURLNote) {
				t.Errorf("a declined link must state why it was not opened:\n%s", output)
			}
		})
	}
}

func TestPublishedAgentURLOmitsWithoutResolution(t *testing.T) {
	// Nothing may name a deployment: not the environment, and not a profile store
	// this test inherited. Both tiers have to be neutralised explicitly, or the
	// assertion only holds on machines with no local login.
	defer isolateProfiles(t)()
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	cdm.Reset()
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", false)
	if got != "" {
		t.Errorf("publishedAgentURL = %q, want empty string", got)
	}
}

// TestPublishedAgentURLUsesBackendURLEnvFallback covers the case where, with no
// dashboard override set, the "View" link must fall back to the deployment
// origin (BLOCKS_BACKEND_URL here) rather than stock CDM / app.blocks.ai.
func TestPublishedAgentURLUsesBackendURLEnvFallback(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://blocks.acme.com")
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true)
	want := "https://blocks.acme.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

// TestPublishedAgentURLUsesActiveProfileFallback covers the case where the active
// profile's BaseURL (an enterprise/custom deployment) must drive the "View"
// link when no dashboard override and no BLOCKS_BACKEND_URL are set.
func TestPublishedAgentURLUsesActiveProfileFallback(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true)
	want := "https://blocks.acme.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

// TestPublishedAgentURLDashboardOverrideWinsOverBackend ensures an explicit
// dashboard origin (profile DashboardBaseURL) still takes precedence over the
// deployment BaseURL fallback — the split-dashboard deployment case.
func TestPublishedAgentURLDashboardOverrideWinsOverBackend(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:          "https://api.acme.com",
		DashboardBaseURL: "https://dashboard.acme.com",
		Orgs:             map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true)
	want := "https://dashboard.acme.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q", got, want)
	}
}

// TestPublishedAgentURLOfflineTierResolvesWhenNonInteractive verifies the
// offline deployment tiers (here the active profile BaseURL) still resolve a
// View link in non-interactive mode — computing it needs no network, so gating
// them off would needlessly drop the link in CI.
func TestPublishedAgentURLOfflineTierResolvesWhenNonInteractive(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", false)
	want := "https://blocks.acme.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q (offline tier must resolve non-interactively)", got, want)
	}
}

// TestPublishedAgentURLSkipsCDMFetchWhenNonInteractive verifies only the
// network-dependent CDM tier is gated behind allowNetworkFallback: with no
// offline tier set (no env, no profile BaseURL, no ldflag), a non-interactive
// call must NOT fall through to a CDM fetch (which could stall in CI/offline).
func TestPublishedAgentURLSkipsCDMFetchWhenNonInteractive(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	cdm.Reset()
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", false)
	if got != "" {
		t.Errorf("publishedAgentURL = %q, want empty string (CDM fetch must stay gated off)", got)
	}
}

// TestPublishedAgentURLBackendOverrideBeatsStaleProfileDashboard covers the
// Precedence hole: when BLOCKS_BACKEND_URL targets a different
// backend than the active profile was logged into, the profile's cached
// DashboardBaseURL is stale and must be skipped so the View link follows the
// backend actually being published to.
func TestPublishedAgentURLBackendOverrideBeatsStaleProfileDashboard(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:          "https://a.example.com",
		DashboardBaseURL: "https://dashboard.a.example.com",
		Orgs:             map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://b.example.com")
	resolveCLIContext(t, rootCmd)

	got := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true)
	want := "https://b.example.com/agents/test_agent"
	if got != want {
		t.Errorf("publishedAgentURL = %q, want %q (must follow BLOCKS_BACKEND_URL, not stale profile dashboard)", got, want)
	}
}

// TestPublishedAgentURLProfileDashboardKeptWhenBackendMatches ensures the
// split-dashboard case still works: a profile whose dashboard origin differs
// from its backend keeps that dashboard origin when BLOCKS_BACKEND_URL is unset
// or points at the profile's own backend (not a divergence).
func TestPublishedAgentURLProfileDashboardKeptWhenBackendMatches(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:          "https://a.example.com",
		DashboardBaseURL: "https://dashboard.a.example.com",
		Orgs:             map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")

	// Unset override: split-dashboard origin is honored.
	t.Setenv("BLOCKS_BACKEND_URL", "")
	resolveCLIContext(t, rootCmd)
	if got, want := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true), "https://dashboard.a.example.com/agents/test_agent"; got != want {
		t.Errorf("unset override: publishedAgentURL = %q, want %q", got, want)
	}

	// Override equals the profile backend (trailing slash aside): still honored.
	t.Setenv("BLOCKS_BACKEND_URL", "https://a.example.com/")
	resolveCLIContext(t, rootCmd)
	if got, want := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true), "https://dashboard.a.example.com/agents/test_agent"; got != want {
		t.Errorf("matching override: publishedAgentURL = %q, want %q", got, want)
	}

	// Override differs only by an explicit default port / case: origin-equivalent,
	// so the dashboard origin is still honored (not a divergence).
	t.Setenv("BLOCKS_BACKEND_URL", "https://A.example.com:443")
	resolveCLIContext(t, rootCmd)
	if got, want := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true), "https://dashboard.a.example.com/agents/test_agent"; got != want {
		t.Errorf("default-port override: publishedAgentURL = %q, want %q", got, want)
	}
}

// TestPublishedAgentURLBackendOverrideDivergesByPath covers same-host,
// path-prefixed multi-tenant deployments: BaseURL is a full request prefix, so
// a BLOCKS_BACKEND_URL that differs only by path is a different deployment and
// must drop the profile's cached (tenant-a) dashboard origin.
func TestPublishedAgentURLBackendOverrideDivergesByPath(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:          "https://host.example.com/tenant-a",
		DashboardBaseURL: "https://host.example.com/tenant-a/dashboard",
		Orgs:             map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")

	// Different path prefix → divergence → follow BLOCKS_BACKEND_URL.
	t.Setenv("BLOCKS_BACKEND_URL", "https://host.example.com/tenant-b")
	resolveCLIContext(t, rootCmd)
	if got, want := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true), "https://host.example.com/tenant-b/agents/test_agent"; got != want {
		t.Errorf("path divergence: publishedAgentURL = %q, want %q (must not open tenant-a dashboard)", got, want)
	}

	// Same path prefix (trailing slash aside) → not a divergence → keep dashboard.
	t.Setenv("BLOCKS_BACKEND_URL", "https://host.example.com/tenant-a/")
	resolveCLIContext(t, rootCmd)
	if got, want := publishedAgentURL([]byte(`{"status":"ok"}`), "test_agent", true), "https://host.example.com/tenant-a/dashboard/agents/test_agent"; got != want {
		t.Errorf("same path: publishedAgentURL = %q, want %q", got, want)
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
		writeRegistered(w)
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

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
		writeRegistered(w)
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
// active-profile org key fails fast with an actionable error. The resolver skips an
// expired profile key, and the legacy tier's expiry check yields the same "not
// authenticated — run blocks login" guidance, so the command never reaches the
// registry.
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
	t.Cleanup(resetPublishFlags)

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

// A key saved for one deployment must never be sent to another. The saved profile
// names deployment A and holds A's key; BLOCKS_BACKEND_URL points at deployment B
// and nothing supplies a key for B. Publish must stop, name B, and reach B with no
// credential at all rather than authenticating as A somewhere A does not exist.
func TestPublishRefusesToSendASavedKeyToAnotherDeployment(t *testing.T) {
	defer isolateProfiles(t)()
	t.Cleanup(clictx.Reset)

	var leaked []string
	deploymentB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "bk_deployment_a") ||
			strings.Contains(r.Header.Get("X-Api-Key"), "bk_deployment_a") {
			leaked = append(leaked, r.URL.Path)
		}
		w.WriteHeader(404)
	}))
	defer deploymentB.Close()

	saved := profiles.Profile{
		BaseURL:      "https://a.blocks.example",
		DefaultOrgID: "org-a",
		Orgs: map[string]profiles.OrgKey{
			"org-a": {OrgName: "Org A", ApiKey: "bk_deployment_a", ExpiresAt: time.Now().Add(24 * time.Hour)},
		},
	}
	if err := profiles.Upsert("a.blocks.example", saved, true); err != nil {
		t.Fatalf("seeding profile: %v", err)
	}

	dir := writeValidProject(t)
	t.Setenv("BLOCKS_BACKEND_URL", deploymentB.URL)
	t.Setenv("BLOCKS_API_KEY", "")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

	rootCmd.SetArgs([]string{"publish", "--listing", "public", "--billing-mode", "free", "--accept-terms"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected publish to stop rather than send the saved deployment's key elsewhere")
	}
	host := strings.TrimPrefix(deploymentB.URL, "http://")
	if !strings.Contains(err.Error(), host) {
		t.Errorf("error should name the deployment being called (%s), got: %s", host, err.Error())
	}
	if !strings.Contains(err.Error(), "BLOCKS_API_KEY") || !strings.Contains(err.Error(), "--api-key") {
		t.Errorf("error should say how to supply a credential, got: %s", err.Error())
	}
	if len(leaked) > 0 {
		t.Errorf("the saved deployment's key was sent to %v", leaked)
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
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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
	t.Cleanup(resetPublishFlags)

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

func TestPreparePublishSourcesEnterpriseFromClictx(t *testing.T) {
	// An enterprise profile is active; preparePublish must report enterprise
	// without any cli-config round-trip, proving it reads clictx rather than
	// re-deriving the answer.
	dir := t.TempDir()
	path := filepath.Join(dir, "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3,
      "active": "umbrella.blocks.ai",
      "profiles": {
        "umbrella.blocks.ai": {
          "base_url": "https://umbrella.blocks.ai",
          "enterprise": true,
          "product_name": "Umbrella Corporation",
          "default_org_id": "org-1",
          "orgs": {"org-1": {"org_name": "Engineering", "api_key": "bk_test"}}
        }
      }
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig; clictx.Reset() })

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	clictx.Resolve(nil)

	projectDir := writeValidProject(t)
	t.Chdir(projectDir)
	t.Setenv("BLOCKS_API_KEY", "bk_test")

	prep, err := preparePublish("blocks publish", nil)
	if err != nil {
		t.Fatalf("preparePublish: %v", err)
	}
	if !prep.enterprise {
		t.Fatal("prep.enterprise should be true from the active profile")
	}
	if calls != 0 {
		t.Fatalf("no discovery call expected, got %d", calls)
	}
}

func TestPublishCallsBannerFunction(t *testing.T) {
	// This test proves that the publish command path calls clictx.PrintBanner().
	// Seeds enterprise context so the banner is non-empty, then verifies the
	// banner appears in the command output.

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	// Routed by path rather than answering every request with a registration body.
	// The enterprise profile this test seeds takes publish through the organization
	// lookup, and `GET /api/v1/orgs` requires exactly 200 — a GET answering the 201
	// a registration returns is not a response any deployment produces, so a
	// catch-all mock would be asserting a contract that does not exist.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/orgs":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"orgs":[{"id":"org-1","name":"Engineering"}]}`))
		default:
			writeRegistered(w)
		}
	}))
	defer ts.Close()

	dir := writeValidProject(t)
	cardPath := filepath.Join(dir, "agent-card.json")
	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

	// Set up enterprise profile context and use --profile flag to ensure it's used
	// The profile must record the deployment the command is pointed at, so the
	// context banner describes the request rather than refusing to.
	seedEnterpriseProfileForTest(t, ts.URL)

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	// Verify both that the banner appears (proving PrintBanner() is called)
	// and that the command succeeds
	if !strings.Contains(output, "[umbrella.blocks.ai / Engineering]") {
		t.Fatalf("publish command should print banner when enterprise context is available, got:\n%s", output)
	}
	if !strings.Contains(output, "Congratulations!") {
		t.Fatalf("publish command should succeed, got:\n%s", output)
	}
}

func TestPublishSummarySuppressesBillingOnEnterprise(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	out := captureStdout(func() {
		printPublishSummary("my_agent", registry.PromotionInput{Listing: "private", BillingMode: "free"})
	})
	if strings.Contains(out, "Billing:") {
		t.Errorf("enterprise must not print a Billing line:\n%s", out)
	}
	if !strings.Contains(out, "Visibility:") {
		t.Errorf("visibility should still print:\n%s", out)
	}
}

func TestPublishSummaryKeepsBillingOnNetwork(t *testing.T) {
	defer isolateProfiles(t)()
	t.Cleanup(clictx.Reset)
	clictx.Reset()
	clictx.Resolve(nil) // no profile → not enterprise
	out := captureStdout(func() {
		printPublishSummary("my_agent", registry.PromotionInput{Listing: "private", BillingMode: "free"})
	})
	if !strings.Contains(out, "Billing: Free") {
		t.Errorf("network must still print Billing: Free:\n%s", out)
	}
}

func TestRegisterSuccessSaysRegisteredNotPublished(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)
	out := captureStdout(func() {
		printPublishSuccess("my_agent", registry.PromotionInput{Listing: "private", BillingMode: "free"}, "", "blocks register")
	})
	if strings.Contains(out, "published to") {
		t.Errorf("register must not say published:\n%s", out)
	}
	if !strings.Contains(out, "registered on Umbrella Corporation") {
		t.Errorf("expected enterprise-branded register verb:\n%s", out)
	}
}

func TestPublishSuccessStillSaysPublished(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)
	out := captureStdout(func() {
		printPublishSuccess("my_agent", registry.PromotionInput{Listing: "public", BillingMode: "free"}, "", "blocks publish")
	})
	if !strings.Contains(out, "published to Umbrella Corporation") {
		t.Errorf("publish should still say published:\n%s", out)
	}
}

// An ambient backend URL sends the publish somewhere the active profile does not
// describe, so the success line must name that deployment instead of claiming the
// agent landed on the profile's product.
func TestPublishSuccessNamesTheEffectiveDeploymentWhenTheProfileIsDisplaced(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://127.0.0.1:8899")
	t.Setenv("BLOCKS_API_KEY", "bk_elsewhere")
	resolveCLIContext(t, publishCmd)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)

	out := captureStdout(func() {
		printPublishSuccess("my_agent", registry.PromotionInput{Listing: "public", BillingMode: "free"}, "", "blocks publish")
	})
	if strings.Contains(out, "Umbrella Corporation") {
		t.Errorf("must not name a product the publish never reached:\n%s", out)
	}
	if !strings.Contains(out, "published to 127.0.0.1:8899.") {
		t.Errorf("expected the effective deployment in the success line:\n%s", out)
	}
}

// Tests for --no-input functionality in publish commands

func TestPromptOrgChoiceNoInputMode(t *testing.T) {
	// Test that --no-input with enterprise multi-org selection errors with zero HTTP requests
	setNoInputMode(true)
	t.Cleanup(func() { setNoInputMode(false) })

	orgs := []orgChoice{
		{Id: "org1", Name: "Organization One"},
		{Id: "org2", Name: "Organization Two"},
	}

	_, err := promptOrgChoice(orgs)

	if err == nil {
		t.Error("promptOrgChoice should return error with --no-input")
	}
	if !strings.Contains(err.Error(), "cannot ask organization selection with --no-input") {
		t.Errorf("error should mention organization selection, got: %v", err)
	}

	// Note: This test verifies no HTTP requests are made because promptOrgChoice
	// fails before any network operations. The actual publish command would need
	// to be tested separately to verify zero HTTP requests to the publish endpoint.
}

func TestPromptOrgChoiceNormalBehavior(t *testing.T) {
	// Test that without --no-input, promptOrgChoice keeps current behavior
	setNoInputMode(false)
	t.Cleanup(func() { setNoInputMode(false) })

	orgs := []orgChoice{
		{Id: "org1", Name: "Organization One"},
		{Id: "org2", Name: "Organization Two"},
	}

	// Simulate user input "1" (select first org)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	go func() {
		defer w.Close()
		w.Write([]byte("1\n"))
	}()

	// Temporarily replace stdin to simulate user input
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	origScanner := stdinScanner
	stdinScanner = nil
	t.Cleanup(func() { stdinScanner = origScanner })

	choice, err := promptOrgChoice(orgs)
	if err != nil {
		t.Errorf("promptOrgChoice should not error without --no-input: %v", err)
	}
	if choice.Id != "org1" {
		t.Errorf("should select first org, got %q", choice.Id)
	}
}

func TestRetryOrgNamePromptNoInputMode(t *testing.T) {
	// Test that retryOrgNamePrompt returns empty string (cannot retry) with --no-input
	setNoInputMode(true)
	t.Cleanup(func() { setNoInputMode(false) })

	// In --no-input mode, retry should not be possible, so it returns empty to signal failure
	result := retryOrgNamePrompt("default_name")
	if result != "" {
		t.Errorf("retryOrgNamePrompt should return empty string with --no-input, got %q", result)
	}
}

func TestRetryOrgNamePromptNormalBehavior(t *testing.T) {
	// Test that retryOrgNamePrompt works normally without --no-input
	setNoInputMode(false)
	t.Cleanup(func() { setNoInputMode(false) })

	// Simulate user providing a new name
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	go func() {
		defer w.Close()
		w.Write([]byte("new_org_name\n"))
	}()

	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	// The prompt primitives share one scanner across prompts, so an earlier test
	// can leave one bound to a pipe that is already closed. Drop it so this
	// prompt reads the stdin this test just installed.
	origScanner := stdinScanner
	stdinScanner = nil
	t.Cleanup(func() { stdinScanner = origScanner })

	result := retryOrgNamePrompt("default_name")
	if result != "new_org_name" {
		t.Errorf("retryOrgNamePrompt should return user input, got %q", result)
	}
}

// marketplacelessBackend stands in for a deployment with billing globally off: it
// serves the registry and the organization list, and answers nothing else — a
// deployment with no marketplace need not route /api/v1/pricing/limits at all. It
// records every path asked for, and the envelope the registry received, so a test can
// assert both what was sent and what was never requested.
type marketplacelessBackend struct {
	paths         []string
	registryCalls int
	envelope      map[string]interface{}
}

func (b *marketplacelessBackend) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.paths = append(b.paths, r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs":
			w.Write([]byte(`{"orgs":[{"id":"org-eng","name":"Engineering"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/registry/agents":
			b.registryCalls++
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &b.envelope)
			writeRegistered(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (b *marketplacelessBackend) asked(path string) bool {
	for _, p := range b.paths {
		if p == path {
			return true
		}
	}
	return false
}

// Suppressing pricing and marketplace vocabulary on a deployment with billing off is
// not a wording choice, it decides what the registry is told. Forcing free where
// --billing-mode is absent left the flag as a way straight past it: the envelope went
// out paid, and the success text promised paid tasks to a deployment that can never
// bill for one.
//
// An explicit paid mode is therefore refused rather than reinterpreted, and refused
// before anything is written: the registry must see no request at all, because a
// publish that lands and then reports a flag problem is a publish the operator has to
// undo.
func TestPublishRefusesExplicitPaidBillingWhereThereIsNoMarketplace(t *testing.T) {
	backend := &marketplacelessBackend{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)

	var err error
	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public",
			"--billing-mode", "paid", "--price", "0.15", "--accept-terms"})
		err = rootCmd.Execute()
	})

	if err == nil {
		t.Fatal("--billing-mode paid must be refused where billing is off and there is no marketplace")
	}
	for _, want := range []string{"--billing-mode paid", "no marketplace", "--billing-mode free"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must contain %q so the fix is one edit: %v", want, err)
		}
	}
	if backend.registryCalls != 0 {
		t.Errorf("registry calls = %d, want 0 — the refusal must land before anything is published", backend.registryCalls)
	}
	// The next-steps text is the other half of the leak: "accept paid tasks" is a
	// promise about a marketplace this deployment does not have.
	for _, forbidden := range []string{"paid", "Paid", "Price per task", "Price per minute"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("output names %q on a deployment with no marketplace:\n%s", forbidden, out)
		}
	}
}

// The absent-flag case is the one the override was written for and must keep working:
// billing is free on the wire, the success text says nothing about billing or pricing,
// and the next step names no marketplace.
func TestPublishWithoutBillingModeFlagStaysFreeWhereThereIsNoMarketplace(t *testing.T) {
	backend := &marketplacelessBackend{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)

	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.registryCalls != 1 {
		t.Fatalf("registry calls = %d, want 1", backend.registryCalls)
	}
	if got := backend.envelope["billingMode"]; got != "free" {
		t.Errorf("billingMode = %v, want free", got)
	}
	if _, ok := backend.envelope["tcAcceptedAt"]; ok {
		t.Error("a deployment with no marketplace has no paid-agent terms to accept")
	}
	if want := "Next: invite organizations before they can use this agent."; !strings.Contains(out, want) {
		t.Errorf("next step should read %q:\n%s", want, out)
	}
	for _, forbidden := range []string{"Billing:", "paid tasks"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("output names %q on a deployment with no marketplace:\n%s", forbidden, out)
		}
	}
}

// An explicit --billing-mode free is the same request the override already makes, so
// it is accepted, and Blocks Network is untouched by the refusal.
func TestRefusePaidWithoutMarketplaceScope(t *testing.T) {
	for _, c := range []struct {
		name       string
		enterprise bool
		mode       string
		refuse     bool
	}{
		{"paid without a marketplace", true, "paid", true},
		{"free without a marketplace", true, "free", false},
		{"paid on Blocks Network", false, "paid", false},
		{"free on Blocks Network", false, "free", false},
		// An unrecognised value stays CollectPromotionInput's to reject, so the two
		// checks cannot disagree about what a valid billing mode is.
		{"an invalid mode without a marketplace", true, "tier", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := refusePaidWithoutMarketplace(c.enterprise, c.mode)
			if c.refuse && err == nil {
				t.Fatalf("refusePaidWithoutMarketplace(%v, %q) = nil, want a refusal", c.enterprise, c.mode)
			}
			if !c.refuse && err != nil {
				t.Fatalf("refusePaidWithoutMarketplace(%v, %q) = %v, want nil", c.enterprise, c.mode, err)
			}
		})
	}
}

// The pricing bounds cannot change anything a marketplace-less publish sends —
// billing is free, and every use of them sits behind a paid billing mode — so asking
// for them was pure cost, paid on every publish, and up to the pricing client's full
// five-second timeout on a deployment that does not answer that route.
func TestPublishAsksForNoPricingLimitsWhereThereIsNoMarketplace(t *testing.T) {
	backend := &marketplacelessBackend{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.registryCalls != 1 {
		t.Fatalf("registry calls = %d, want 1 — the publish must have run for this to prove anything", backend.registryCalls)
	}
	if backend.asked("/api/v1/pricing/limits") {
		t.Errorf("the publish asked for pricing limits it cannot use; paths = %v", backend.paths)
	}
}

// backendMaxPricePerTask is the per-task ceiling pricingLimitsBackend answers with. It
// sits far below registry.MaxPricePerTask, the CLI's compiled-in default, and that gap is
// what makes "the limits were fetched" checkable by their effect: a price the defaults
// permit and this ceiling does not can only be refused by a value that came off the wire.
const backendMaxPricePerTask = "0.50"

// pricingLimitsBackend stands in for a Blocks Network deployment that does route
// /api/v1/pricing/limits. It counts that route separately from the registry so a test can
// assert the exact number of times it was asked, which for a free publish is zero.
type pricingLimitsBackend struct {
	paths         []string
	limitsCalls   int
	registryCalls int
	envelope      map[string]interface{}
}

func (b *pricingLimitsBackend) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.paths = append(b.paths, r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pricing/limits":
			b.limitsCalls++
			fmt.Fprintf(w, `{"minPricePerTask":"0.01","minPricePerMinute":"0.01",`+
				`"maxPricePerTask":%q,"maxPricePerMinute":"1.00",`+
				`"maxFreeTasksAllowed":5,"maxFreeMinutesAllowed":30}`,
				backendMaxPricePerTask)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/registry/agents":
			b.registryCalls++
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &b.envelope)
			writeRegistered(w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedNetworkPublish points a stock Blocks Network profile at srv and puts a valid card
// in the working directory, so the cases below differ only in the arguments they pass and
// the answers they give.
func seedNetworkPublish(t *testing.T, srvURL string) string {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{
		BaseURL:      srvURL,
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_net"}},
	})
	dir := writeValidProject(t)
	t.Chdir(dir)
	t.Setenv(blocksBackendURLEnv, srvURL)
	t.Setenv(blocksAPIKeyEnv, "")
	t.Setenv(blocksAppBaseURLEnv, "")
	t.Setenv(blocksDashboardURLEnv, "")
	resetPublishFlags()
	t.Cleanup(resetPublishFlags)
	return filepath.Join(dir, "agent-card.json")
}

// answerStdin feeds lines to the prompts CollectPromotionInput reads. Those prompts build
// their own scanner over os.Stdin rather than taking this package's shared one, so
// answerPrompts is not enough for them and the file descriptor itself has to be replaced.
func answerStdin(t *testing.T, lines string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(lines)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })
}

// Every use of the pricing bounds inside CollectPromotionInput — both price prompts and
// their help text, the free-tasks and free-minutes ceilings, and the refusals citing them
// — sits inside its one `billingMode == "paid"` block. So a free publish cannot be changed
// by any value the request returns, and it was still made: on Blocks Network the
// marketplace check that was supposed to skip it does not fire, so --billing-mode free
// paid up to the pricing client's five-second timeout for nothing at all.
//
// Zero requests, not "fewer": the assertion is on the count, because a route asked once
// per publish is exactly what went unnoticed.
func TestPublishAsksForNoPricingLimitsWhenBillingIsFree(t *testing.T) {
	t.Run("the flag settles it as free", func(t *testing.T) {
		backend := &pricingLimitsBackend{}
		cardPath := seedNetworkPublish(t, backend.start(t).URL)

		captureStdout(func() {
			rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free"})
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("publish failed: %v", err)
			}
		})

		if backend.registryCalls != 1 {
			t.Fatalf("registry calls = %d, want 1 — the publish must have run for this to prove anything", backend.registryCalls)
		}
		if backend.limitsCalls != 0 {
			t.Errorf("pricing limits asked for %d times, want 0; paths = %v", backend.limitsCalls, backend.paths)
		}
	})

	// Enterprise arrives here the same way rather than by a separate rule: a deployment
	// with no marketplace has its billing mode forced to free before this point, so the
	// skip is the same skip. That it still holds is asserted by
	// TestPublishAsksForNoPricingLimitsWhereThereIsNoMarketplace, over the real flow.
}

// The interactive publish that has not been told a billing mode used to be the case the
// skip could not cover: the bounds were handed to registry.CollectPromotionInput as a
// value, so they had to be fetched before the user had answered — one round-trip for an
// invocation that may then choose free and use none of it.
//
// It takes a function now, called only once the mode is known to be paid, so choosing free
// at the prompt costs no request at all. That the deferral does not cost the paid path its
// bounds is held by TestAnInteractivePaidPublishPromptsWithTheDeploymentsPriceRange, which
// pins that the range the price prompt renders is still the deployment's own.
func TestAnInteractivePublishThatChoosesFreeFetchesNoBounds(t *testing.T) {
	backend := &pricingLimitsBackend{}
	cardPath := seedNetworkPublish(t, backend.start(t).URL)
	isInteractive = func() bool { return true }
	answerStdin(t, "1\n1\n") // public, then Free Agent

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.registryCalls != 1 {
		t.Fatalf("registry calls = %d, want 1 — the publish must have run for this to prove anything", backend.registryCalls)
	}
	if got := backend.envelope["billingMode"]; got != "free" {
		t.Fatalf("billingMode = %v, want free — the prompt answers did not take", got)
	}
	if backend.limitsCalls != 0 {
		t.Errorf("pricing limits asked for %d times, want 0 — a free publish cannot use them; paths = %v", backend.limitsCalls, backend.paths)
	}
}

// The other half: where the mode can still be paid the fetch has to happen, and what
// comes back has to be what validates. The price below is inside the CLI's compiled-in
// ceiling and outside the deployment's, so only a bound read off the wire can refuse it —
// this fails as loudly for a fetch that was skipped as for one whose answer was ignored.
func TestPublishValidatesAPaidPriceAgainstTheDeploymentsOwnLimits(t *testing.T) {
	backend := &pricingLimitsBackend{}
	cardPath := seedNetworkPublish(t, backend.start(t).URL)
	// Forced off, not left to the default: isInteractive is true under `go test` because
	// it accepts any character device and stdin is /dev/null, so without this the refusal
	// below becomes a prompt loop against a stdin that answers nothing.
	isInteractive = func() bool { return false }

	var err error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public",
			"--billing-mode", "paid", "--price", "1.00", "--accept-terms"})
		err = rootCmd.Execute()
	})

	if err == nil {
		t.Fatal("a price above the deployment's ceiling must be refused")
	}
	if !strings.Contains(err.Error(), backendMaxPricePerTask) {
		t.Errorf("the refusal must quote the deployment's own ceiling %s: %v", backendMaxPricePerTask, err)
	}
	if backend.limitsCalls != 1 {
		t.Errorf("pricing limits asked for %d times, want 1; paths = %v", backend.limitsCalls, backend.paths)
	}
	if backend.registryCalls != 0 {
		t.Errorf("registry calls = %d, want 0 — the refusal must land before anything is published", backend.registryCalls)
	}
}

// The bound on how far the fetch can be deferred: the prompt after the billing-mode
// question renders the permitted range, so the bounds must be in hand by the time an
// interactive paid publish reaches it. Deferring to the start of the paid block satisfies
// that — the range printed here is the deployment's own, which pins the ordering rather
// than merely the fact of the request. Deferring any later, into the price prompt itself,
// would not.
func TestAnInteractivePaidPublishPromptsWithTheDeploymentsPriceRange(t *testing.T) {
	backend := &pricingLimitsBackend{}
	cardPath := seedNetworkPublish(t, backend.start(t).URL)
	isInteractive = func() bool { return true }
	answerStdin(t, "2\n0.25\n") // Paid Agent, then a price inside the deployment's range

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public",
			"--accept-terms", "--free-tasks", "0"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.limitsCalls != 1 {
		t.Fatalf("pricing limits asked for %d times, want 1; paths = %v", backend.limitsCalls, backend.paths)
	}
	if want := "up to " + backendMaxPricePerTask; !strings.Contains(output, want) {
		t.Errorf("the price prompt must offer the deployment's ceiling (%q):\n%s", want, output)
	}
	if got := backend.envelope["billingMode"]; got != "paid" {
		t.Errorf("billingMode = %v, want paid", got)
	}
	if got := backend.envelope["pricePerTask"]; got != "0.250000" {
		t.Errorf("pricePerTask = %v, want the price entered at the prompt", got)
	}
}

// A refusal has to arrive before the organization picker, not after it. The picker's
// first act on a multi-organization enterprise account is to mint a key at the
// deployment for the organization chosen, and a flag rejected afterwards leaves that
// credential behind with nothing to spend it on — the publish it was minted for never
// happened.
func TestPublishRefusesPaidBillingBeforeThePickerMintsAnything(t *testing.T) {
	backend := &multiOrgEnterpriseServer{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // would pick Operations, whose key has to be minted

	var err error
	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private",
			"--billing-mode", "paid", "--price", "0.15", "--accept-terms"})
		err = rootCmd.Execute()
	})

	if err == nil {
		t.Fatal("--billing-mode paid must be refused where there is no marketplace")
	}
	if backend.mintCalls != 0 {
		t.Errorf("mint calls = %d, want 0 — a refused publish must not leave a key behind", backend.mintCalls)
	}
	if backend.orgListCalls != 0 {
		t.Errorf("org list calls = %d, want 0 — the flag needs no deployment lookup to be refused", backend.orgListCalls)
	}
	if backend.registryCalls != 0 {
		t.Errorf("registry calls = %d, want 0", backend.registryCalls)
	}
	if strings.Contains(out, "Which organization should own this agent?") {
		t.Errorf("the user was asked a question whose answer was already useless:\n%s", out)
	}
}
