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

// TestVercelEnvVarPath verifies that when VERCEL_TOKEN is set,
// Ensure returns it immediately without touching the credential store.
func TestVercelEnvVarPath(t *testing.T) {
	t.Setenv(vercelEnvVar, "vc_env_token_456")

	flow := &VercelFlow{CredsPath: filepath.Join(t.TempDir(), "creds.json")}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "vc_env_token_456" {
		t.Errorf("AccessToken = %q, want vc_env_token_456", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "vercel" {
		t.Errorf("Provider = %q, want vercel", creds.Provider)
	}
	// Credential file must NOT have been written.
	if _, err := os.Stat(flow.CredsPath); !os.IsNotExist(err) {
		t.Error("Ensure wrote a credentials file for an env-var token")
	}
}

// TestVercelStoredTokenPath verifies that a previously stored token is
// returned without prompting the user.
func TestVercelStoredTokenPath(t *testing.T) {
	os.Unsetenv(vercelEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")

	// Pre-store a token.
	if err := auth.SetProviderCredential(credPath, "vercel", &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: "vc_stored_token",
	}); err != nil {
		t.Fatalf("SetProviderCredential: %v", err)
	}

	flow := &VercelFlow{CredsPath: credPath}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "vc_stored_token" {
		t.Errorf("AccessToken = %q, want vc_stored_token", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "vercel" {
		t.Errorf("Provider = %q, want vercel", creds.Provider)
	}
}

// TestVercelValidTokenStored verifies that a valid token entered interactively
// is verified and stored.
func TestVercelValidTokenStored(t *testing.T) {
	os.Unsetenv(vercelEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &VercelFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("vc_valid_token\n"),
		Validator: func(_ context.Context, token string) error { return nil },
	}

	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "vc_valid_token" {
		t.Errorf("AccessToken = %q, want vc_valid_token", creds.AccessToken)
	}

	// Token must be persisted for next run.
	entry, _ := auth.GetProviderCredential(credPath, "vercel")
	if entry == nil || entry.AccessToken != "vc_valid_token" {
		t.Error("valid token was not stored in credentials file")
	}
}

// TestVercelInvalidTokenRejected verifies that a token rejected by the
// Vercel API is not stored and Ensure returns an error.
func TestVercelInvalidTokenRejected(t *testing.T) {
	os.Unsetenv(vercelEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &VercelFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("vc_bogus_token\n"),
		Validator: func(_ context.Context, _ string) error {
			return errors.New("token rejected by provider (HTTP 401) — check scopes and try again")
		},
	}

	_, err := flow.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure should have returned an error for an invalid token")
	}

	// No credential must be stored.
	entry, _ := auth.GetProviderCredential(credPath, "vercel")
	if entry != nil && entry.AccessToken != "" {
		t.Error("invalid token was stored in credentials file")
	}
}

// TestVercelProviderName verifies the Provider() method returns "vercel".
func TestVercelProviderName(t *testing.T) {
	f := &VercelFlow{}
	if f.Provider() != "vercel" {
		t.Errorf("Provider() = %q, want vercel", f.Provider())
	}
}
