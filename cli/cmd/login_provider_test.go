package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// TestLoginProviderCloudflareEnvVar runs `blocks login --provider cloudflare`
// when CLOUDFLARE_API_TOKEN is set. It must succeed without prompting.
func TestLoginProviderCloudflareEnvVar(t *testing.T) {
	t.Setenv("CLOUDFLARE_API_TOKEN", "cf_login_test_token")

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	loginProvider = "cloudflare"
	defer func() { loginProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--provider", "cloudflare"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --provider cloudflare: %v", err)
		}
	})

	if !strings.Contains(output, "Logged in to Cloudflare") {
		t.Errorf("output = %q, want 'Logged in to Cloudflare'", output)
	}
}

// TestLoginProviderVercelEnvVar runs `blocks login --provider vercel`
// when VERCEL_TOKEN is set.
func TestLoginProviderVercelEnvVar(t *testing.T) {
	t.Setenv("VERCEL_TOKEN", "vc_login_test_token")

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	loginProvider = "vercel"
	defer func() { loginProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--provider", "vercel"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --provider vercel: %v", err)
		}
	})

	if !strings.Contains(output, "Logged in to Vercel") {
		t.Errorf("output = %q, want 'Logged in to Vercel'", output)
	}
}

// TestLoginProviderNetlifyEnvVar runs `blocks login --provider netlify`
// when NETLIFY_AUTH_TOKEN is set.
func TestLoginProviderNetlifyEnvVar(t *testing.T) {
	t.Setenv("NETLIFY_AUTH_TOKEN", "nl_login_test_token")

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	loginProvider = "netlify"
	defer func() { loginProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--provider", "netlify"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --provider netlify: %v", err)
		}
	})

	if !strings.Contains(output, "Logged in to Netlify") {
		t.Errorf("output = %q, want 'Logged in to Netlify'", output)
	}
}

// TestLoginProviderUnknown verifies that an unknown --provider value returns an error.
func TestLoginProviderUnknown(t *testing.T) {
	loginProvider = "aws"
	defer func() { loginProvider = "blocks" }()

	rootCmd.SetArgs([]string{"login", "--provider", "aws"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error = %q, want 'unknown provider'", err.Error())
	}
}

// TestLoginProviderStoredToken verifies that login succeeds when a token is
// already stored (no prompt needed).
func TestLoginProviderStoredToken(t *testing.T) {
	os.Unsetenv("CLOUDFLARE_API_TOKEN")

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	// Pre-store a token so no interactive prompt occurs.
	auth.SetProviderCredential(credFile, "cloudflare", &auth.ProviderEntry{
		MintMethod:  "api_token",
		AccessToken: "cf_already_stored",
	})

	loginProvider = "cloudflare"
	defer func() { loginProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--provider", "cloudflare"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --provider cloudflare (stored): %v", err)
		}
	})

	if !strings.Contains(output, "Logged in to Cloudflare") {
		t.Errorf("output = %q, want 'Logged in to Cloudflare'", output)
	}
}
