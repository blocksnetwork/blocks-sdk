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
		w.WriteHeader(200)
		w.Write([]byte(`{"id": "inv-1", "status": "sent"}`))
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
		w.WriteHeader(200)
		w.Write([]byte(`{"id": "inv-1", "status": "sent"}`))
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
		w.Write([]byte(`{"invitations": []}`))
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
		resp := `{"invitations": [{"id": "inv-1", "email": "alice@example.com", "scope": "user", "createdAt": "2025-01-01T00:00:00Z", "expiresAt": "2025-01-08T00:00:00Z"}]}`
		w.WriteHeader(200)
		w.Write([]byte(resp))
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
	if !strings.Contains(output, "inv-1") {
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
		w.Write([]byte(`{"agentName": "test_agent"}`))
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
		w.Write([]byte(`{"grants": []}`))
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
		resp := `{"grants": [{"id": "grant-1", "scope": "user", "createdAt": "2025-01-01T00:00:00Z", "granteeUser": {"name": "Alice", "email": "alice@example.com"}}]}`
		w.WriteHeader(200)
		w.Write([]byte(resp))
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
	if !strings.Contains(output, "grant-1") {
		t.Errorf("output missing grant ID:\n%s", output)
	}
	if !strings.Contains(output, "alice@example.com") {
		t.Errorf("output missing grantee email:\n%s", output)
	}
	if !strings.Contains(output, "user") {
		t.Errorf("output missing scope:\n%s", output)
	}
}
