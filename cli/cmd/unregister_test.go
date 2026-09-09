package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/pflag"
)

// The response contract type now lives in unregister.go, because the command verifies
// the body rather than discarding it; the fixtures below build that same type.

// deletedResponseTS is a fixed epoch-millisecond stamp. A fixture wants a value that
// cannot vary between runs, and the CLI reads none of the response, so any valid
// integer serves; what matters is that the field is present at all.
const deletedResponseTS int64 = 1_756_000_000_000

// deletedResponseBody encodes that contract rather than spelling a JSON literal at each
// mock, so no single mock can drift from it.
func deletedResponseBody(agentName string) []byte {
	body, err := json.Marshal(agentDeleteResponse{AgentName: agentName, Status: "deleted", TS: deletedResponseTS})
	if err != nil {
		// Three scalar fields: unreachable, and a mock that could not describe the
		// contract must not quietly answer something else.
		panic(err)
	}
	return body
}

// writeDeleted answers a delete the way a deployment does, echoing the agent the request
// named. Every success mock in this file goes through it.
//
// Five of them used to answer `{}` — a body carrying neither required field. That passed
// only because runUnregister decodes into a map[string]interface{} it never reads, which
// is exactly why it was worth fixing: the mocks are the only statement of the response
// contract these tests make, and one that omits required fields lets a decoder start
// depending on them with no test noticing. The correction changes no behaviour today.
func writeDeleted(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write(deletedResponseBody(r.URL.Query().Get("agentName")))
}

// The fixture is only worth correcting if something holds it to the contract, so the
// shape is asserted directly: both required fields present, status the one permitted
// value, and nothing else in the body. Without this the fields are inert — nothing in
// runUnregister reads the response — and `{}` could come back unnoticed.
//
// `ts` is asserted here because the endpoint sends it on every success, not because the
// schema requires it. The schema declares it and deliberately leaves it out of
// `required`: declaring it is what makes a real response valid under
// `additionalProperties: false`, while requiring it would reject a response that omits
// it and turn an additive correction into a breaking one. So this pins the deployment's
// behaviour; a consumer must not reject a schema-valid body that omits `ts`.
func TestDeletedResponseFixtureMatchesTheDeleteContract(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(deletedResponseBody("my_agent")))
	// The contract permits no additional properties, so the fixture must not carry any.
	dec.DisallowUnknownFields()
	var got agentDeleteResponse
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the delete fixture must decode strictly into the response contract: %v", err)
	}
	if got.AgentName != "my_agent" {
		t.Errorf("agentName = %q, want the agent the delete named", got.AgentName)
	}
	if got.Status != "deleted" {
		t.Errorf("status = %q, want %q — the contract's only permitted value", got.Status, "deleted")
	}
	if got.TS != deletedResponseTS {
		t.Errorf("ts = %d, want %d — the endpoint always sends it, so the fixture must too", got.TS, deletedResponseTS)
	}
}

func writeCardWithName(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	card := map[string]interface{}{
		"identity": map[string]interface{}{"agentName": name},
	}
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal card: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-card.json"), data, 0644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	return dir
}

func TestReadAgentNameFromCard(t *testing.T) {
	dir := writeCardWithName(t, "my_agent")
	got, err := readAgentNameFromCard(filepath.Join(dir, "agent-card.json"))
	if err != nil {
		t.Fatalf("readAgentNameFromCard: %v", err)
	}
	if got != "my_agent" {
		t.Fatalf("got %q, want %q", got, "my_agent")
	}
}

func TestReadAgentNameFromCardWorksOnSchemaInvalidCard(t *testing.T) {
	// Deliberately missing every field except identity.agentName: unregister
	// must not require a valid card.
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(path, []byte(`{"identity":{"agentName":"broken_agent"},"junk":1}`), 0644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	got, err := readAgentNameFromCard(path)
	if err != nil {
		t.Fatalf("readAgentNameFromCard: %v", err)
	}
	if got != "broken_agent" {
		t.Fatalf("got %q", got)
	}
}

func TestReadAgentNameFromCardMissingName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(path, []byte(`{"identity":{}}`), 0644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	if _, err := readAgentNameFromCard(path); err == nil {
		t.Fatal("expected an error when identity.agentName is absent")
	}
}

func TestUnregisterDeletesAgent(t *testing.T) {
	var gotMethod, gotPath, gotAgentName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAgentName = r.URL.Query().Get("agentName")
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	out := captureStdout(func() {
		if err := runUnregister(t.Context(), []string{"my_agent"}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", gotMethod)
	}
	// The delete endpoint takes the agent name as a query parameter:
	// DELETE /api/v1/registry/agents?agentName=<name>
	if gotPath != "/api/v1/registry/agents" {
		t.Errorf("path = %s, want /api/v1/registry/agents", gotPath)
	}
	if gotAgentName != "my_agent" {
		t.Errorf("agentName query param = %s, want my_agent", gotAgentName)
	}
	// The banner names the deployment and stops there: the organization the key
	// belongs to does not scope a removal, so asserting one would describe the
	// credential while reading as a promise about what the command can reach. The
	// agent is named by the confirmation prompt, and by the success line below.
	if !strings.Contains(out, "[umbrella.blocks.ai]") {
		t.Errorf("banner missing:\n%s", out)
	}
	if strings.Contains(out, "Engineering") {
		t.Errorf("banner asserts an organization that does not scope the deletion:\n%s", out)
	}
	if !strings.Contains(out, "my_agent removed") {
		t.Errorf("success line missing:\n%s", out)
	}
}

func TestUnregisterReadsNameFromCardWhenNoArg(t *testing.T) {
	var gotPath, gotAgentName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAgentName = r.URL.Query().Get("agentName")
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Chdir(writeCardWithName(t, "card_agent"))
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	if err := runUnregister(t.Context(), nil); err != nil {
		t.Fatalf("runUnregister: %v", err)
	}
	// The delete endpoint takes the agent name as a query parameter:
	// DELETE /api/v1/registry/agents?agentName=<name>
	if gotPath != "/api/v1/registry/agents" {
		t.Errorf("path = %s, want /api/v1/registry/agents", gotPath)
	}
	if gotAgentName != "card_agent" {
		t.Errorf("agentName query param = %s, want card_agent", gotAgentName)
	}
}

func TestUnregisterSurfacesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	err := runUnregister(t.Context(), []string{"ghost"})
	if err == nil {
		t.Fatal("expected an error for a missing agent")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the agent, got: %v", err)
	}
}

func TestUnregisterConfirmationDeclined(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)

	// Set up interactive mode and simulate "n" response
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() { isTTY = origIsTTY })

	origStdinScanner := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader("n\n"))
	t.Cleanup(func() { stdinScanner = origStdinScanner })

	if err := runUnregister(t.Context(), []string{"test_agent"}); err != nil {
		t.Fatalf("runUnregister should not error on declined confirmation: %v", err)
	}

	if requestCount != 0 {
		t.Errorf("expected 0 DELETE requests when confirmation declined, got %d", requestCount)
	}
}

func TestUnregisterNonInteractiveWithoutYesRefuses(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)

	// Set up non-interactive mode
	origIsTTY := isTTY
	isTTY = func() bool { return false }
	t.Cleanup(func() { isTTY = origIsTTY })

	err := runUnregister(t.Context(), []string{"test_agent"})
	if err == nil {
		t.Fatal("expected an error when non-interactive without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should mention --yes flag, got: %v", err)
	}

	if requestCount != 0 {
		t.Errorf("expected 0 DELETE requests when non-interactive without --yes, got %d", requestCount)
	}
}

func TestUnregisterNonInteractiveWithYesProceeds(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	// Set up non-interactive mode
	origIsTTY := isTTY
	isTTY = func() bool { return false }
	t.Cleanup(func() { isTTY = origIsTTY })

	if err := runUnregister(t.Context(), []string{"test_agent"}); err != nil {
		t.Fatalf("runUnregister should not error with --yes in non-interactive mode: %v", err)
	}

	if requestCount != 1 {
		t.Errorf("expected 1 DELETE request when non-interactive with --yes, got %d", requestCount)
	}
}

// hostOf is the host a message should name when the deployment cannot be named by
// product — the same label the context banner uses.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Host
}

// setupUnregisterConfirmation puts the command in the interactive shape where the
// confirmation prompt is reached, answering it with "y".
func setupUnregisterConfirmation(t *testing.T) {
	t.Helper()
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	origScanner := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader("y\n"))
	origYes := unregisterYes
	unregisterYes = false
	origNoInput := noInputMode
	noInputMode = false
	t.Cleanup(func() {
		isTTY = origIsTTY
		stdinScanner = origScanner
		unregisterYes = origYes
		noInputMode = origNoInput
	})
}

// clearUnregisterFlagValues puts the flags cobra parsed onto the command back to
// their defaults. Cobra keeps a parsed value on the flag itself, so an --api-key from
// an earlier Execute in the same binary is still there for effectiveOverrides to
// read, which would decide this invocation's credential.
func clearUnregisterFlagValues(t *testing.T) {
	t.Helper()
	reset := func() {
		unregisterCmd.Flags().VisitAll(func(f *pflag.Flag) {
			if err := f.Value.Set(f.DefValue); err != nil {
				t.Fatalf("reset --%s: %v", f.Name, err)
			}
			f.Changed = false
		})
	}
	reset()
	t.Cleanup(reset)
}

// A multi-organization enterprise user holds one key per organization, and the
// deployment authorizes a removal by that caller's rights over the named agent — not
// by the organization the key was minted for. So a key for Engineering deletes an
// agent owned by Operations, and a banner reading "/ Engineering" would be a true
// statement about the credential that an operator reads as a promise about what is in
// reach. The banner must assert no organization here; the confirmation the operator
// actually answers names the agent, which for an irreversible removal is the fact
// they need.
func TestUnregisterBannerAssertsNoOrganizationWhenAnotherOrgOwnsTheAgent(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)

	var gotAuth, gotAgentName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAgentName = r.URL.Query().Get("agentName")
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	// The profile's default organization is Engineering, and Engineering's key is the
	// one the invocation will send. ops_agent belongs to Operations, whose cached key
	// is never touched — the deployment accepts the deletion all the same.
	seedProfile(t, "acme.example.com", profiles.Profile{
		BaseURL:      srv.URL,
		Enterprise:   true,
		ProductName:  "Acme Corp",
		DefaultOrgID: "org-eng",
		Orgs: map[string]profiles.OrgKey{
			"org-eng": {OrgName: "Engineering", ApiKey: "bk_eng"},
			"org-ops": {OrgName: "Operations", ApiKey: "bk_ops"},
		},
	})
	t.Setenv(blocksBackendURLEnv, srv.URL)
	t.Setenv(blocksAPIKeyEnv, "")
	resetUnregisterFlags(t)
	clearUnregisterFlagValues(t)
	setupUnregisterConfirmation(t)

	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"unregister", "ops_agent"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unregister failed: %v", err)
		}
	})

	// The premise: the removal really did go out as Engineering and really did delete
	// another organization's agent. Without this the test would prove nothing about
	// what the banner may claim.
	if want := "Bearer bk_eng"; gotAuth != want {
		t.Fatalf("DELETE authorization = %q, want %q", gotAuth, want)
	}
	if gotAgentName != "ops_agent" {
		t.Fatalf("agentName query param = %q, want ops_agent", gotAgentName)
	}

	if strings.Contains(out, "Engineering") || strings.Contains(out, "Operations") {
		t.Errorf("the banner must not assert an organization that does not scope the deletion:\n%s", out)
	}
	if strings.Contains(out, "organization") {
		t.Errorf("naming the organization at all invites the inference the banner exists to prevent:\n%s", out)
	}
	banner := strings.Index(out, "[acme.example.com]")
	prompt := strings.Index(out, "Remove ops_agent from Acme Corp?")
	switch {
	case banner < 0:
		t.Fatalf("the banner must still name the deployment the removal reaches:\n%s", out)
	case prompt < 0:
		t.Fatalf("the confirmation must name the agent being removed:\n%s", out)
	case banner > prompt:
		t.Errorf("the deployment is stated after the confirmation has already been asked:\n%s", out)
	}
	if !strings.Contains(out, "This cannot be undone.") {
		t.Errorf("the destructive confirmation is missing:\n%s", out)
	}
}

// An ambient backend URL sends the removal somewhere the active profile does not
// describe. The confirmation the user answers, and the line reporting what was
// removed, must name that deployment — confirming a deletion "from <product>"
// while the request goes elsewhere is the failure this guards.
func TestUnregisterNamesTheEffectiveDeploymentWhenTheProfileIsDisplaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	// The profile records a different deployment from the one the request reaches.
	seedEnterpriseProfileForTest(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_elsewhere")
	resolveCLIContext(t, unregisterCmd)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)
	setupUnregisterConfirmation(t)

	out := captureStdout(func() {
		if err := runUnregister(t.Context(), []string{"my_agent"}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	host := hostOf(t, srv.URL)
	if strings.Contains(out, "Umbrella Corporation") {
		t.Errorf("output must not name a product the request never reached:\n%s", out)
	}
	if want := "Remove my_agent from " + host + "?"; !strings.Contains(out, want) {
		t.Errorf("confirmation should read %q:\n%s", want, out)
	}
	if want := "✓ my_agent removed from " + host; !strings.Contains(out, want) {
		t.Errorf("success line should read %q:\n%s", want, out)
	}
}

// The normal case — nothing displaces the profile — must keep reading exactly as
// it does today, product name and all.
func TestUnregisterNamesTheProductWhenTheProfileIsTheTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	resolveCLIContext(t, unregisterCmd)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)
	setupUnregisterConfirmation(t)

	out := captureStdout(func() {
		if err := runUnregister(t.Context(), []string{"my_agent"}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	if want := "Remove my_agent from Umbrella Corporation?"; !strings.Contains(out, want) {
		t.Errorf("confirmation should read %q:\n%s", want, out)
	}
	if want := "✓ my_agent removed from Umbrella Corporation"; !strings.Contains(out, want) {
		t.Errorf("success line should read %q:\n%s", want, out)
	}
	if strings.Contains(out, hostOf(t, srv.URL)+"?") {
		t.Errorf("a verifiable product name must not be replaced by a host:\n%s", out)
	}
}

// A decline is a successful outcome, and the caller has to be able to tell it from
// a failure without reading the message text.
func TestUnregisterDeclineIsReportedAsCancellation(t *testing.T) {
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	origScanner := stdinScanner
	origYes := unregisterYes
	unregisterYes = false
	origNoInput := noInputMode
	noInputMode = false
	t.Cleanup(func() {
		isTTY = origIsTTY
		stdinScanner = origScanner
		unregisterYes = origYes
		noInputMode = origNoInput
	})

	stdinScanner = bufio.NewScanner(strings.NewReader("n\n"))
	err := confirmUnregister("my_agent")
	if !errors.Is(err, errUnregisterCancelled) {
		t.Fatalf("confirmUnregister on a decline = %v, want the cancellation sentinel", err)
	}

	// An unreadable answer is not a decline: it must name the flag that answers.
	stdinScanner = bufio.NewScanner(strings.NewReader(""))
	err = confirmUnregister("my_agent")
	if errors.Is(err, errUnregisterCancelled) {
		t.Fatalf("a failed read must not be reported as a decline, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("confirmUnregister on a failed read = %v, want an error naming --yes", err)
	}
}

// The agent name reaching unregister is deliberately unvalidated — it comes from an
// argument, or straight out of a possibly mid-edit agent card — so it is the one
// input that can carry a terminal escape sequence into the two lines this command
// prints for the operator to check. An erase-line and cursor-up pair placed there
// wipes out "Remove <name> from <deployment>?" and "This cannot be undone", which is
// the difference between a confirmation and a rubber stamp.
func TestUnregisterPromptCannotBeForgedByTheAgentName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	setupUnregisterConfirmation(t)

	const hostile = "victim_agent\x1b[2K\r\x1b[1Aharmless_agent"
	out := captureStdout(func() {
		if err := runUnregister(t.Context(), []string{hostile}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	for _, r := range out {
		if r < 0x20 && r != '\n' || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("output carries the control character %U, which can rewrite the confirmation:\n%q", r, out)
		}
	}
	// The operator must still see which agent is being removed, or the prompt has
	// been made useless by the very guard meant to protect it.
	if !strings.Contains(out, "Remove victim_agent") {
		t.Errorf("the prompt must still name the agent:\n%q", out)
	}
}

// The request keeps using the name as given: sanitizing is a rendering concern, and a
// name the server would reject must be rejected by the server, not silently rewritten
// into a different agent's name by the CLI.
func TestUnregisterSendsTheAgentNameUnmodified(t *testing.T) {
	var gotAgentName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAgentName = r.URL.Query().Get("agentName")
		writeDeleted(w, r)
	}))
	t.Cleanup(srv.Close)

	seedEnterpriseProfileForTest(t, srv.URL)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_API_KEY", "bk_test")
	resolveCLIContext(t, unregisterCmd)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	const hostile = "victim_agent\x1b[2K"
	captureStdout(func() {
		if err := runUnregister(t.Context(), []string{hostile}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	if gotAgentName != hostile {
		t.Errorf("agentName query param = %q, want the name exactly as given (%q)", gotAgentName, hostile)
	}
}

// The command used to decode the delete response into a map it never read, so every 2xx
// printed "removed" — including a 2xx that is not this endpoint's answer. A proxy or
// captive portal answering 200 with an empty or HTML body, or a deployment reporting a
// different agent, all read as success. For a destructive command whose value is telling
// the operator what happened, an unconfirmed success is worse than an error.
func TestUnregisterRefusesToConfirmAnUnrecognisedResponse(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"empty object", `{}`, `status ""`},
		{"proxy answered with something else", `{"ok":true}`, `status ""`},
		{"status is not deleted", `{"agentName":"my_agent","status":"queued"}`, `status "queued"`},
		{"a different agent", `{"agentName":"other_agent","status":"deleted"}`, `agent "other_agent"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreCLIState(t)
			isolateCredentials(t)
			defer isolateProfiles(t)()
			isolateAmbientState(t)
			resetUnregisterFlags(t)

			backend := newRecordingDeployment(t, tc.body)
			t.Setenv("BLOCKS_BACKEND_URL", backend.url)

			out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_test", "--yes")
			if err == nil {
				t.Fatalf("an unconfirmed removal must be an error, got success:\n%s", out)
			}
			if !strings.Contains(err.Error(), "could not confirm") {
				t.Errorf("the error must say the removal was not confirmed, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error must quote what the deployment said (%s), got %v", tc.want, err)
			}
			if strings.Contains(out, "removed from") {
				t.Errorf("success must not be printed for an unconfirmed removal:\n%s", out)
			}
		})
	}
}

// The contract's optional field stays optional: a response omitting `ts` is valid and
// must still confirm, or the CLI would reject bodies the schema permits.
func TestUnregisterConfirmsAResponseWithoutTheOptionalTimestamp(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	backend := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	t.Setenv("BLOCKS_BACKEND_URL", backend.url)

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_test", "--yes")
	if err != nil {
		t.Fatalf("a schema-valid response without ts must confirm: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed from") {
		t.Errorf("the removal must be reported:\n%s", out)
	}
}
