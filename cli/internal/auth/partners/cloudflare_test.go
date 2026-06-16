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

// TestCloudflareEnvVarPath verifies that when CLOUDFLARE_API_TOKEN is set,
// Ensure returns it immediately without touching the credential store.
func TestCloudflareEnvVarPath(t *testing.T) {
	t.Setenv(cloudflareEnvVar, "cf_env_token_123")

	flow := &CloudflareFlow{CredsPath: filepath.Join(t.TempDir(), "creds.json")}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "cf_env_token_123" {
		t.Errorf("AccessToken = %q, want cf_env_token_123", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want cloudflare", creds.Provider)
	}
	// Credential file must NOT have been written.
	if _, err := os.Stat(flow.CredsPath); !os.IsNotExist(err) {
		t.Error("Ensure wrote a credentials file for an env-var token")
	}
}

// TestCloudflareStoredTokenPath verifies that a previously stored token is
// returned without prompting the user.
func TestCloudflareStoredTokenPath(t *testing.T) {
	os.Unsetenv(cloudflareEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")

	// Pre-store a token.
	if err := auth.SetProviderCredential(credPath, "cloudflare", &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: "cf_stored_token",
	}); err != nil {
		t.Fatalf("SetProviderCredential: %v", err)
	}

	flow := &CloudflareFlow{CredsPath: credPath}
	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "cf_stored_token" {
		t.Errorf("AccessToken = %q, want cf_stored_token", creds.AccessToken)
	}
	if creds.Kind != auth.CredentialKindAPIToken {
		t.Errorf("Kind = %q, want %q", creds.Kind, auth.CredentialKindAPIToken)
	}
	if creds.Provider != "cloudflare" {
		t.Errorf("Provider = %q, want cloudflare", creds.Provider)
	}
}

// TestCloudflareValidTokenStored verifies that a valid token entered interactively
// is verified and stored.
func TestCloudflareValidTokenStored(t *testing.T) {
	os.Unsetenv(cloudflareEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &CloudflareFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("cf_valid_token\n"),
		Validator: func(_ context.Context, token string) error { return nil },
	}

	creds, err := flow.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if creds.AccessToken != "cf_valid_token" {
		t.Errorf("AccessToken = %q, want cf_valid_token", creds.AccessToken)
	}

	// Token must be persisted for next run.
	entry, _ := auth.GetProviderCredential(credPath, "cloudflare")
	if entry == nil || entry.AccessToken != "cf_valid_token" {
		t.Error("valid token was not stored in credentials file")
	}
}

// TestCloudflareInvalidTokenRejected verifies that a token rejected by the
// Cloudflare API is not stored and Ensure returns an error.
func TestCloudflareInvalidTokenRejected(t *testing.T) {
	os.Unsetenv(cloudflareEnvVar)

	credPath := filepath.Join(t.TempDir(), "creds.json")
	flow := &CloudflareFlow{
		CredsPath: credPath,
		Reader:    strings.NewReader("cf_bogus_token\n"),
		Validator: func(_ context.Context, _ string) error {
			return errors.New("token rejected by provider (HTTP 401) — check scopes and try again")
		},
	}

	_, err := flow.Ensure(context.Background())
	if err == nil {
		t.Fatal("Ensure should have returned an error for an invalid token")
	}

	// No credential must be stored.
	entry, _ := auth.GetProviderCredential(credPath, "cloudflare")
	if entry != nil && entry.AccessToken != "" {
		t.Error("invalid token was stored in credentials file")
	}
}

// TestCloudflareProviderName verifies the Provider() method returns "cloudflare".
func TestCloudflareProviderName(t *testing.T) {
	f := &CloudflareFlow{}
	if f.Provider() != "cloudflare" {
		t.Errorf("Provider() = %q, want cloudflare", f.Provider())
	}
}
