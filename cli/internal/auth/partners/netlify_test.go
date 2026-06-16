package partners

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// TestNetlifyEnvVarPath verifies that when NETLIFY_AUTH_TOKEN is set,
// Ensure returns it immediately without touching the credential store.
func TestNetlifyEnvVarPath(t *testing.T) {
	t.Setenv(netlifyEnvVar, "nl_env_token_789")

	flow := &NetlifyFlow{CredsPath: filepath.Join(t.TempDir(), "creds.json")}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "nl_env_token_789" {
		t.Errorf("AccessToken = %q, want nl_env_token_789", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "netlify" {
		t.Errorf("Provider = %q, want netlify", creds.Provider)
	}
	// Credential file must NOT have been written.
	if _, err := os.Stat(flow.CredsPath); !os.IsNotExist(err) {
		t.Error("Ensure wrote a credentials file for an env-var token")
	}
}

// TestNetlifyStoredTokenPath verifies that a previously stored token is
// returned without prompting the user.
func TestNetlifyStoredTokenPath(t *testing.T) {
	os.Unsetenv(netlifyEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")

	// Pre-store a token.
	if err := auth.SetProviderCredential(credPath, "netlify", &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: "nl_stored_token",
	}); err != nil {
		t.Fatalf("SetProviderCredential: %v", err)
	}

	flow := &NetlifyFlow{CredsPath: credPath}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "nl_stored_token" {
		t.Errorf("AccessToken = %q, want nl_stored_token", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "netlify" {
		t.Errorf("Provider = %q, want netlify", creds.Provider)
	}
}

// TestNetlifyValidTokenStored verifies that a valid token entered interactively
// is verified and stored.
func TestNetlifyValidTokenStored(t *testing.T) {
	os.Unsetenv(netlifyEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &NetlifyFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("nl_valid_token\n"),
		Validator: func(_ context.Context, token string) error { return nil },
	}

	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "nl_valid_token" {
		t.Errorf("AccessToken = %q, want nl_valid_token", creds.AccessToken)
	}

	// Token must be persisted for next run.
	entry, _ := auth.GetProviderCredential(credPath, "netlify")
	if entry == nil || entry.AccessToken != "nl_valid_token" {
		t.Error("valid token was not stored in credentials file")
	}
}

// TestNetlifyInvalidTokenRejected verifies that a token rejected by the
// Netlify API is not stored and Ensure returns an error.
func TestNetlifyInvalidTokenRejected(t *testing.T) {
	os.Unsetenv(netlifyEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &NetlifyFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("nl_bogus_token\n"),
		Validator: func(_ context.Context, _ string) error {
			return errors.New("token rejected by provider (HTTP 401) — check scopes and try again")
		},
	}

	_, err := flow.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure should have returned an error for an invalid token")
	}

	// No credential must be stored.
	entry, _ := auth.GetProviderCredential(credPath, "netlify")
	if entry != nil && entry.AccessToken != "" {
		t.Error("invalid token was stored in credentials file")
	}
}

// TestNetlifyProviderName verifies the Provider() method returns "netlify".
func TestNetlifyProviderName(t *testing.T) {
	f := &NetlifyFlow{}
	if f.Provider() != "netlify" {
		t.Errorf("Provider() = %q, want netlify", f.Provider())
	}
}
