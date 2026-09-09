package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

// The invite commands talk to five endpoints, and the mocks in this file are the only
// statement these tests make about what those endpoints return. A mock that answers a
// body no deployment produces — one missing required properties, one carrying properties
// the contract forbids, or a body at all where the contract is 204 with none — lets a
// decoder start depending on a shape that cannot exist, with every test still green.
//
// So each success answer is built from a type mirroring the endpoint's published response
// contract, and TestInviteFixturesMatchThePublishedResponseContracts holds the built bodies
// to the required and permitted property sets those contracts declare — including that
// grant revocation answers 204 with no body at all.
// Most of the corrected properties are inert as far as the CLI is concerned — it decodes
// `notified`, `inviteUrl`, `agentName` and the list/grant columns and ignores the rest —
// so the fixtures are only worth correcting if something checks them, and that test is
// what does.
//
// Identifiers are UUIDs because the contracts declare them as such; the CLI prints them
// verbatim, so the assertions name the same constants.
const (
	inviteFixtureID    = "1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed"
	grantFixtureID     = "2c0e7cde-ccfe-4c3e-8c6e-bc9efcce5cfe"
	agentFixtureID     = "3d1f8def-ddaf-4d4f-9d7f-cdaf0ddf6daf"
	orgFixtureID       = "4e2a9efa-eeba-4e5a-8e8a-deba1eea7eba"
	userFixtureID      = "5f3bafab-ffcb-4f6b-9f9b-efcb2ffb8fcb"
	inviteFixtureURL   = "https://app.example.com/agent-invite?token=t"
	inviteFixtureEmail = "user@example.com"
)

// invitationProjection mirrors the `invitation` object of the create response. It is a
// projection of the stored row, not the row: the invitation's token is deliberately not
// part of it, because the bearer secret behind inviteUrl has no business appearing a
// second time in a body clients log and store.
type invitationProjection struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Scope       string  `json:"scope"`
	TargetOrgID *string `json:"targetOrgId,omitempty"`
	ExpiresAt   string  `json:"expiresAt"`
	CreatedAt   string  `json:"createdAt"`
}

// invitationCreateResponse mirrors the create response: the invitation, the link that
// accepts it, and how many addresses were emailed. Notified is a pointer because a
// server older than the field omits it, which is not the same as reporting zero.
type invitationCreateResponse struct {
	Invitation invitationProjection `json:"invitation"`
	InviteURL  string               `json:"inviteUrl"`
	Notified   *int                 `json:"notified,omitempty"`
}

// invitationListEntry mirrors one row of the list response. `email` on an
// organization-scoped invitation is a contact address rather than the grantee, which is
// what targetOrgName is for.
type invitationListEntry struct {
	ID            string  `json:"id"`
	AgentID       string  `json:"agentId"`
	Email         string  `json:"email"`
	TargetOrgID   *string `json:"targetOrgId,omitempty"`
	TargetOrgName *string `json:"targetOrgName,omitempty"`
	Scope         string  `json:"scope"`
	ExpiresAt     string  `json:"expiresAt"`
	CreatedAt     string  `json:"createdAt"`
	InvitedByName string  `json:"invitedByName"`
}

type invitationListResponse struct {
	Invitations []invitationListEntry `json:"invitations"`
}

// grantRow mirrors the created grant the accept response carries. The CLI reads only
// agentName beside it, but a response without the grant is not one any deployment sends.
type grantRow struct {
	ID            string  `json:"id"`
	AgentID       string  `json:"agentId"`
	GranteeUserID *string `json:"granteeUserId,omitempty"`
	GranteeOrgID  *string `json:"granteeOrgId,omitempty"`
	GrantedBy     string  `json:"grantedBy"`
	CreatedAt     string  `json:"createdAt"`
}

type invitationAcceptResponse struct {
	Grant            grantRow `json:"grant"`
	AgentName        string   `json:"agentName"`
	AgentDisplayName *string  `json:"agentDisplayName,omitempty"`
	AlreadyAccepted  *bool    `json:"alreadyAccepted,omitempty"`
}

type grantGranteeUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type grantGranteeOrg struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// grantListEntry mirrors one row of the grant list. Exactly one grantee is set: a grant
// is either to a user or to an organization.
type grantListEntry struct {
	ID          string            `json:"id"`
	Scope       string            `json:"scope"`
	GranteeUser *grantGranteeUser `json:"granteeUser,omitempty"`
	GranteeOrg  *grantGranteeOrg  `json:"granteeOrg,omitempty"`
	CreatedAt   string            `json:"createdAt"`
}

type grantListResponse struct {
	Grants []grantListEntry `json:"grants"`
}

// mustMarshal renders a fixture, or panics. The handlers that write these run on the
// server's goroutine, where t.Fatalf is not allowed, and a mock that could not describe
// the contract must not quietly answer something else.
func mustMarshal(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return body
}

// invitationCreatedBody is the body of a created invitation for the given scope.
// notified nil stands for a server old enough not to report delivery at all.
func invitationCreatedBody(scope string, notified *int) []byte {
	inv := invitationProjection{
		ID:        inviteFixtureID,
		Email:     inviteFixtureEmail,
		Scope:     scope,
		ExpiresAt: "2025-01-08T00:00:00Z",
		CreatedAt: "2025-01-01T00:00:00Z",
	}
	if scope == "org" {
		orgID := orgFixtureID
		inv.TargetOrgID = &orgID
	}
	return mustMarshal(invitationCreateResponse{
		Invitation: inv,
		InviteURL:  inviteFixtureURL,
		Notified:   notified,
	})
}

// writeInvitationCreated answers a send the way a deployment does: 201, since the
// invitation is created rather than merely accepted for processing.
func writeInvitationCreated(w http.ResponseWriter, scope string, notified *int) {
	w.WriteHeader(http.StatusCreated)
	w.Write(invitationCreatedBody(scope, notified))
}

func notified(n int) *int { return &n }

func invitationListBody(entries ...invitationListEntry) []byte {
	if entries == nil {
		entries = []invitationListEntry{}
	}
	return mustMarshal(invitationListResponse{Invitations: entries})
}

// pendingUserInvitation is the one row the list assertions read.
func pendingUserInvitation(email string) invitationListEntry {
	return invitationListEntry{
		ID:            inviteFixtureID,
		AgentID:       agentFixtureID,
		Email:         email,
		Scope:         "user",
		ExpiresAt:     "2025-01-08T00:00:00Z",
		CreatedAt:     "2025-01-01T00:00:00Z",
		InvitedByName: "Owner",
	}
}

func invitationAcceptedBody(agentName string) []byte {
	userID := userFixtureID
	return mustMarshal(invitationAcceptResponse{
		Grant: grantRow{
			ID:            grantFixtureID,
			AgentID:       agentFixtureID,
			GranteeUserID: &userID,
			GrantedBy:     userFixtureID,
			CreatedAt:     "2025-01-01T00:00:00Z",
		},
		AgentName: agentName,
	})
}

func grantListBody(entries ...grantListEntry) []byte {
	if entries == nil {
		entries = []grantListEntry{}
	}
	return mustMarshal(grantListResponse{Grants: entries})
}

// userGrant is the row the revoke flow matches on: it finds the grant whose grantee
// email is the one asked for, then deletes that grant by id.
func userGrant(name, email string) grantListEntry {
	return grantListEntry{
		ID:          grantFixtureID,
		Scope:       "user",
		GranteeUser: &grantGranteeUser{Name: name, Email: email},
		CreatedAt:   "2025-01-01T00:00:00Z",
	}
}

// writeGrantRevoked answers a revoke the way the deployment does: 204 with no body at
// all. Every mock in this file that used to answer it with `{"status":"ok"}` described a
// server that does not exist — the revoke controller sends 204 and sends nothing.
func writeGrantRevoked(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// serveInviteEndpoints answers the three endpoints a send-then-revoke sequence touches,
// each with the body (or the studied absence of one) its own contract specifies, and
// appends every request line it saw to asked.
func serveInviteEndpoints(asked *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if asked != nil {
			*asked = append(*asked, r.Method+" "+r.URL.RequestURI())
		}
		switch {
		case r.Method == http.MethodDelete:
			writeGrantRevoked(w)
		case strings.HasSuffix(r.URL.Path, "/grants"):
			w.WriteHeader(http.StatusOK)
			w.Write(grantListBody(userGrant("Alice", inviteFixtureEmail)))
		default:
			writeInvitationCreated(w, "user", notified(1))
		}
	}
}

// objectShape is a response contract expressed as property sets: what it requires, and —
// when the contract permits nothing else — everything it allows. Checking a fixture's
// keys against these is what makes the corrected shapes load-bearing; decoding a fixture
// back into the type that produced it would prove only that Go marshals its own structs.
type objectShape struct {
	what      string
	required  []string
	permitted []string // nil when the contract does not close the object
}

func assertObjectShape(t *testing.T, shape objectShape, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: fixture is not a JSON object: %v", shape.what, err)
	}
	for _, name := range shape.required {
		if _, ok := obj[name]; !ok {
			t.Errorf("%s: fixture omits the required property %q: %s", shape.what, name, raw)
		}
	}
	if shape.permitted != nil {
		allowed := map[string]bool{}
		for _, name := range shape.permitted {
			allowed[name] = true
		}
		for name := range obj {
			if !allowed[name] {
				t.Errorf("%s: the contract permits no additional properties, fixture carries %q: %s", shape.what, name, raw)
			}
		}
	}
	return obj
}

// TestInviteFixturesMatchThePublishedResponseContracts is what the corrected fixtures
// are load-bearing for. Without it the properties the CLI does not read are inert, and a
// mock could drift back to `{"id":"inv-1","status":"sent"}` — a body with neither
// required property and two the contract forbids — without a single test noticing.
func TestInviteFixturesMatchThePublishedResponseContracts(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		body := assertObjectShape(t, objectShape{
			what:      "POST /api/v1/agents/:agentName/invitations",
			required:  []string{"invitation", "inviteUrl"},
			permitted: []string{"invitation", "inviteUrl", "notified"},
		}, invitationCreatedBody("org", notified(1)))
		assertObjectShape(t, objectShape{
			what:     "the create response's invitation projection",
			required: []string{"id", "email", "scope", "expiresAt", "createdAt"},
		}, body["invitation"])
	})

	// A server older than the delivery count omits it and still sends the link. Its
	// absence is the whole point of the case that reads it, so the fixture must be
	// complete in every other respect.
	t.Run("create without a delivery count", func(t *testing.T) {
		body := invitationCreatedBody("user", nil)
		assertObjectShape(t, objectShape{
			what:      "POST /api/v1/agents/:agentName/invitations (no notified)",
			required:  []string{"invitation", "inviteUrl"},
			permitted: []string{"invitation", "inviteUrl"},
		}, body)
	})

	t.Run("list", func(t *testing.T) {
		body := assertObjectShape(t, objectShape{
			what:     "GET /api/v1/agents/:agentName/invitations",
			required: []string{"invitations"},
		}, invitationListBody(pendingUserInvitation(inviteFixtureEmail)))
		var rows []json.RawMessage
		if err := json.Unmarshal(body["invitations"], &rows); err != nil {
			t.Fatalf("invitations is not an array: %v", err)
		}
		for _, row := range rows {
			assertObjectShape(t, objectShape{
				what:     "an invitation list row",
				required: []string{"id", "agentId", "email", "scope", "expiresAt", "createdAt", "invitedByName"},
			}, row)
		}
	})

	t.Run("accept", func(t *testing.T) {
		body := assertObjectShape(t, objectShape{
			what:      "POST /api/v1/agent-invitations/accept",
			required:  []string{"grant", "agentName"},
			permitted: []string{"grant", "agentName", "agentDisplayName", "alreadyAccepted"},
		}, invitationAcceptedBody("test_agent"))
		assertObjectShape(t, objectShape{
			what:     "the accept response's grant",
			required: []string{"id", "agentId", "grantedBy", "createdAt"},
		}, body["grant"])
	})

	t.Run("grants", func(t *testing.T) {
		body := assertObjectShape(t, objectShape{
			what:     "GET /api/v1/agents/:agentName/grants",
			required: []string{"grants"},
		}, grantListBody(userGrant("Alice", inviteFixtureEmail)))
		var rows []json.RawMessage
		if err := json.Unmarshal(body["grants"], &rows); err != nil {
			t.Fatalf("grants is not an array: %v", err)
		}
		for _, row := range rows {
			grant := assertObjectShape(t, objectShape{
				what:     "a grant list row",
				required: []string{"id", "scope", "createdAt"},
			}, row)
			assertObjectShape(t, objectShape{
				what:     "a grant's user grantee",
				required: []string{"name", "email"},
			}, grant["granteeUser"])
		}
	})
}

// The revoke endpoint answers 204 with no body, so the fixture for it is the absence of
// one — which a JSON assertion cannot express. This drives the writer through a real
// server and reads the response the CLI would get: the status the contract specifies,
// and nothing to decode.
func TestGrantRevokeFixtureAnswersTheNoContentContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGrantRevoked(w)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/agents/my_agent/grants/"+grantFixtureID, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want %d — the revoke contract is No Content", resp.StatusCode, http.StatusNoContent)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want none — a 204 carries no payload", body)
	}
}

func resetInviteFlags() {
	inviteSendEmail = ""
	inviteSendOrg = ""
	inviteRevokeEmail = ""
	inviteRevokeOrg = ""
	inviteSendCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
	inviteRevokeCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
}

func TestInviteSendRequiresEmailOrOrg(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	resetInviteFlags()

	rootCmd.SetArgs([]string{"invite", "send", "my_agent"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when neither --email nor --org is provided")
	}
	if !strings.Contains(err.Error(), "either --email or --org is required") {
		t.Errorf("error = %q, want 'either --email or --org is required'", err.Error())
	}
}

func TestInviteSendRejectsBothEmailAndOrg(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	resetInviteFlags()

	rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "user@example.com", "--org", "my-org"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when both --email and --org are provided")
	}
	if !strings.Contains(err.Error(), "--email and --org are mutually exclusive") {
		t.Errorf("error = %q, want '--email and --org are mutually exclusive'", err.Error())
	}
}

func TestInviteSendWithOrgSlug(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var receivedBody map[string]interface{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		writeInvitationCreated(w, "org", notified(1))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--org", "acme-corp"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send with --org failed: %v", err)
		}
	})

	if receivedBody["targetOrgSlug"] != "acme-corp" {
		t.Errorf("body targetOrgSlug = %v, want acme-corp", receivedBody["targetOrgSlug"])
	}
	if receivedBody["email"] != nil {
		t.Errorf("body email should be absent, got %v", receivedBody["email"])
	}
	if !strings.Contains(output, "Invitation sent to org acme-corp") {
		t.Errorf("output = %q, want 'Invitation sent to org acme-corp'", output)
	}
	// An organization-scoped invitation is still authorized by ownership of the agent,
	// so the banner names the agent and the invited organization — the pair the
	// deployment decides on — rather than the organization the key belongs to.
	if !strings.Contains(output, "/ agent my_agent → org acme-corp]") {
		t.Errorf("banner should name the agent and the invited org:\n%s", output)
	}
}

func TestInviteSendSuccess(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var receivedMethod string
	var receivedPath string
	var receivedBody map[string]interface{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		writeInvitationCreated(w, "user", notified(1))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send failed: %v", err)
		}
	})

	if receivedMethod != "POST" {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if receivedPath != "/api/v1/agents/my_agent/invitations" {
		t.Errorf("path = %q, want /api/v1/agents/my_agent/invitations", receivedPath)
	}
	if receivedBody["email"] != "user@example.com" {
		t.Errorf("body email = %v, want user@example.com", receivedBody["email"])
	}
	if !strings.Contains(output, "Invitation sent to user@example.com") {
		t.Errorf("output = %q, want 'Invitation sent to user@example.com'", output)
	}
}

// The invitation exists and its link works; only the email failed. Reporting
// "sent" and dropping the link would leave the invitee with no way in at all.
func TestInviteSendReportsAnInvitationNobodyWasEmailed(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeInvitationCreated(w, "user", notified(0))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send failed: %v", err)
		}
	})

	if strings.Contains(output, "Invitation sent") {
		t.Errorf("output claims the invitation was sent:\n%s", output)
	}
	if !strings.Contains(output, "could not be emailed") {
		t.Errorf("output = %q, want 'could not be emailed'", output)
	}
	if !strings.Contains(output, inviteFixtureURL) {
		t.Errorf("output missing the invitation link:\n%s", output)
	}
}

// A server older than the `notified` contract answers without the field. It did
// email the invitation, so its silence must not be read as nobody.
func TestInviteSendTreatsAMissingNotifiedCountAsEmailed(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeInvitationCreated(w, "org", nil)
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--org", "acme-corp"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send failed: %v", err)
		}
	})

	if !strings.Contains(output, "Invitation sent to org acme-corp") {
		t.Errorf("output = %q, want 'Invitation sent to org acme-corp'", output)
	}
	if strings.Contains(output, "could not be emailed") {
		t.Errorf("output warns about delivery the server never reported:\n%s", output)
	}
}

func TestInviteListHelpDescribesUnacceptedInvitations(t *testing.T) {
	const want = "List unaccepted invitations for a private agent"
	if got := inviteListCmd.Short; got != want {
		t.Errorf("invite list help = %q, want %q", got, want)
	}
}

func TestInviteListEmpty(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(invitationListBody())
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "list", "my_agent"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite list failed: %v", err)
		}
	})

	if !strings.Contains(output, "No unaccepted invitations.") {
		t.Errorf("output = %q, want 'No unaccepted invitations.'", output)
	}
}

func TestInviteListWithResults(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var receivedMethod string
	var receivedPath string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		w.WriteHeader(200)
		w.Write(invitationListBody(pendingUserInvitation("alice@example.com")))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "list", "my_agent"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite list failed: %v", err)
		}
	})

	if receivedMethod != "GET" {
		t.Errorf("method = %q, want GET", receivedMethod)
	}
	if receivedPath != "/api/v1/agents/my_agent/invitations" {
		t.Errorf("path = %q, want /api/v1/agents/my_agent/invitations", receivedPath)
	}
	if !strings.Contains(output, "ID") || !strings.Contains(output, "EMAIL") || !strings.Contains(output, "SCOPE") {
		t.Errorf("output missing table headers:\n%s", output)
	}
	if !strings.Contains(output, inviteFixtureID) {
		t.Errorf("output missing invitation ID:\n%s", output)
	}
	if !strings.Contains(output, "alice@example.com") {
		t.Errorf("output missing email:\n%s", output)
	}
	if !strings.Contains(output, "user") {
		t.Errorf("output missing scope:\n%s", output)
	}
}

func TestInviteAcceptSuccess(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var receivedMethod string
	var receivedPath string
	var receivedBody map[string]string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &receivedBody)
		w.WriteHeader(200)
		w.Write(invitationAcceptedBody("test_agent"))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "accept", "tok-abc123"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite accept failed: %v", err)
		}
	})

	if receivedMethod != "POST" {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if receivedPath != "/api/v1/agent-invitations/accept" {
		t.Errorf("path = %q, want /api/v1/agent-invitations/accept", receivedPath)
	}
	if receivedBody["token"] != "tok-abc123" {
		t.Errorf("body token = %q, want tok-abc123", receivedBody["token"])
	}
	if !strings.Contains(output, "Access granted to test_agent") {
		t.Errorf("output = %q, want 'Access granted to test_agent'", output)
	}
}

func TestInviteGrantsEmpty(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(grantListBody())
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "grants", "my_agent"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite grants failed: %v", err)
		}
	})

	if !strings.Contains(output, "No active grants.") {
		t.Errorf("output = %q, want 'No active grants.'", output)
	}
}

func TestInviteGrantsWithResults(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var receivedMethod string
	var receivedPath string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		w.WriteHeader(200)
		w.Write(grantListBody(userGrant("Alice", "alice@example.com")))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "grants", "my_agent"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite grants failed: %v", err)
		}
	})

	if receivedMethod != "GET" {
		t.Errorf("method = %q, want GET", receivedMethod)
	}
	if receivedPath != "/api/v1/agents/my_agent/grants" {
		t.Errorf("path = %q, want /api/v1/agents/my_agent/grants", receivedPath)
	}
	if !strings.Contains(output, "ID") || !strings.Contains(output, "SCOPE") || !strings.Contains(output, "GRANTEE") {
		t.Errorf("output missing table headers:\n%s", output)
	}
	if !strings.Contains(output, grantFixtureID) {
		t.Errorf("output missing grant ID:\n%s", output)
	}
	if !strings.Contains(output, "alice@example.com") {
		t.Errorf("output missing grantee email:\n%s", output)
	}
	if !strings.Contains(output, "user") {
		t.Errorf("output missing scope:\n%s", output)
	}
}

func TestInviteSendCallsBannerFunction(t *testing.T) {
	// This test proves that the invite send command path calls clictx.PrintBanner().
	// Seeds enterprise context so the banner is non-empty, then verifies the
	// banner appears in the command output.
	//
	// Sharing a private agent is authorized by ownership of that agent, never by the
	// organization the key belongs to, so the banner names the pair the deployment
	// decides on — this agent, this grantee — and not that organization.

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeInvitationCreated(w, "user", notified(1))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	// Set up enterprise profile context and use --profile flag to ensure it's used
	// The profile must record the deployment the command is pointed at, so the
	// context banner describes the request rather than refusing to.
	seedEnterpriseProfileForTest(t, ts.URL)

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send failed: %v", err)
		}
	})

	// Verify both that the banner appears (proving PrintBanner() is called)
	// and that the command succeeds
	if !strings.Contains(output, "[umbrella.blocks.ai / agent my_agent → user@example.com]") {
		t.Fatalf("invite send command should print banner when enterprise context is available, got:\n%s", output)
	}
	if strings.Contains(output, "Engineering") {
		t.Fatalf("the banner names an organization that does not scope the invitation:\n%s", output)
	}
	if !strings.Contains(output, "Invitation sent to user@example.com") {
		t.Fatalf("invite send command should succeed, got:\n%s", output)
	}
}

func TestInviteRevokeCallsBannerFunction(t *testing.T) {
	// This test proves that the invite revoke command path calls clictx.PrintBanner().
	// Seeds enterprise context so the banner is non-empty, then verifies the
	// banner appears in the command output.

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(serveInviteEndpoints(nil)))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	// Set up enterprise profile context and use --profile flag to ensure it's used
	// The profile must record the deployment the command is pointed at, so the
	// context banner describes the request rather than refusing to.
	seedEnterpriseProfileForTest(t, ts.URL)

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "revoke", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite revoke failed: %v", err)
		}
	})

	// Verify both that the banner appears (proving PrintBanner() is called)
	// and that the command succeeds
	if !strings.Contains(output, "[umbrella.blocks.ai / agent my_agent → user@example.com]") {
		t.Fatalf("invite revoke command should print banner when enterprise context is available, got:\n%s", output)
	}
	if strings.Contains(output, "Engineering") {
		t.Fatalf("the banner names an organization that does not scope the revocation:\n%s", output)
	}
	if !strings.Contains(output, "Access revoked for user@example.com") {
		t.Fatalf("invite revoke command should succeed, got:\n%s", output)
	}
}

func TestInviteAcceptCallsBannerFunction(t *testing.T) {
	// This test proves that the invite accept command path calls clictx.PrintBanner().
	// Seeds enterprise context so the banner is non-empty, then verifies the
	// banner appears in the command output.

	cleanup := setupFakeCredentials(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(invitationAcceptedBody("my_agent"))
	}))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)

	resetInviteFlags()

	// Set up enterprise profile context and use --profile flag to ensure it's used
	// The profile must record the deployment the command is pointed at, so the
	// context banner describes the request rather than refusing to.
	seedEnterpriseProfileForTest(t, ts.URL)

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "accept", "tok-abc123"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite accept failed: %v", err)
		}
	})

	// Verify both that the banner appears (proving PrintBanner() is called)
	// and that the command succeeds
	// Accepting is authorized by being the invitation's recipient, and the only
	// subject the CLI has before the request is the token — a secret the caller just
	// typed, and no help to print back. So the banner states the deployment alone, and
	// the agent appears in the success line, which is where the CLI first learns it.
	if !strings.Contains(output, "[umbrella.blocks.ai]") {
		t.Fatalf("invite accept command should print banner when enterprise context is available, got:\n%s", output)
	}
	if strings.Contains(output, "Engineering") {
		t.Fatalf("the banner names an organization that does not scope the acceptance:\n%s", output)
	}
	if strings.Contains(output, "tok-abc123") {
		t.Fatalf("the invitation token must not be echoed back:\n%s", output)
	}
	if !strings.Contains(output, "Access granted to my_agent") {
		t.Fatalf("invite accept command should succeed, got:\n%s", output)
	}
}

// An agent name is caller-supplied text that lands inside the request path, so a
// '/', '?' or '#' in one used to choose the endpoint. `send` and `revoke` print a
// safety banner naming the agent exactly as supplied, so such a name left the
// operator confirming one operation while the CLI performed another.
//
// The assertion is at the socket rather than on printed text, because the request
// line is the only place the endpoint actually reached is visible. A refused name
// must produce no request at all — not a request to a merely different path.
func TestInviteRefusesAnAgentNameThatCouldChooseTheEndpoint(t *testing.T) {
	names := map[string]string{
		"a path separator": "my_agent/grants/grant-1",
		"a parent segment": "../../api/v1/agents/other_agent",
		"a query":          "my_agent?scope=admin",
		"a fragment":       "my_agent#grants",
		"an encoded slash": "my_agent%2Fgrants",
	}
	for _, verb := range []string{"send", "revoke"} {
		for what, name := range names {
			t.Run(verb+"/"+what, func(t *testing.T) {
				cleanup := setupFakeCredentials(t)
				defer cleanup()

				var asked []string
				ts := httptest.NewServer(serveInviteEndpoints(&asked))
				defer ts.Close()

				t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
				resetInviteFlags()
				t.Cleanup(resetInviteFlags)

				var err error
				out := captureStdout(func() {
					rootCmd.SetArgs([]string{"invite", verb, name, "--email", "user@example.com"})
					err = rootCmd.Execute()
				})

				if err == nil {
					t.Fatalf("invite %s accepted an agent name carrying %s (%q); output:\n%s", verb, what, name, out)
				}
				if !strings.Contains(err.Error(), "alphanumeric characters and underscores") {
					t.Errorf("error should say what an agent name may contain, got: %v", err)
				}
				if len(asked) != 0 {
					t.Errorf("the deployment was asked for %v; a refused name must reach no endpoint at all", asked)
				}
			})
		}
	}
}

// The complement, so the refusal above cannot be satisfied by refusing everything:
// an ordinary name still reaches exactly the documented paths, and revoke still
// addresses the grant the deployment reported.
func TestInviteAddressesTheDocumentedPathsForAnOrdinaryAgentName(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var asked []string
	ts := httptest.NewServer(serveInviteEndpoints(&asked))
	defer ts.Close()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
	resetInviteFlags()
	t.Cleanup(resetInviteFlags)

	captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send: %v", err)
		}
	})
	resetInviteFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "revoke", "my_agent", "--email", "user@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite revoke: %v", err)
		}
	})

	want := []string{
		"POST /api/v1/agents/my_agent/invitations",
		"GET /api/v1/agents/my_agent/grants",
		"DELETE /api/v1/agents/my_agent/grants/" + grantFixtureID,
	}
	if len(asked) != len(want) {
		t.Fatalf("requests = %v, want %v", asked, want)
	}
	for i, w := range want {
		if asked[i] != w {
			t.Errorf("request %d = %q, want %q", i, asked[i], w)
		}
	}
}
