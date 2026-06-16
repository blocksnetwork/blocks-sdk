package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// TestLogoutProviderCloudflare verifies that `blocks logout --provider cloudflare`
// removes the cloudflare credential and prints the confirmation message.
func TestLogoutProviderCloudflare(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	// Pre-store a Cloudflare token.
	auth.SetProviderCredential(credFile, "cloudflare", &auth.ProviderEntry{
		MintMethod:  "api_token",
		AccessToken: "cf_to_delete",
	})

	logoutProvider = "cloudflare"
	defer func() { logoutProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"logout", "--provider", "cloudflare"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("logout --provider cloudflare: %v", err)
		}
	})

	if !strings.Contains(output, "Logged out of Cloudflare") {
		t.Errorf("output = %q, want 'Logged out of Cloudflare'", output)
	}

	// Token must be gone.
	entry, err := auth.GetProviderCredential(credFile, "cloudflare")
	if err != nil {
		t.Fatalf("GetProviderCredential: %v", err)
	}
	if entry != nil && entry.AccessToken != "" {
		t.Errorf("cloudflare token still present after logout: %s", entry.AccessToken)
	}
}

// TestLogoutProviderVercel verifies partner logout for Vercel.
func TestLogoutProviderVercel(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	auth.SetProviderCredential(credFile, "vercel", &auth.ProviderEntry{
		MintMethod:  "api_token",
		AccessToken: "vc_to_delete",
	})

	logoutProvider = "vercel"
	defer func() { logoutProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"logout", "--provider", "vercel"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("logout --provider vercel: %v", err)
		}
	})

	if !strings.Contains(output, "Logged out of Vercel") {
		t.Errorf("output = %q, want 'Logged out of Vercel'", output)
	}
}

// TestLogoutProviderNetlify verifies partner logout for Netlify.
func TestLogoutProviderNetlify(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	auth.SetProviderCredential(credFile, "netlify", &auth.ProviderEntry{
		MintMethod:  "api_token",
		AccessToken: "nl_to_delete",
	})

	logoutProvider = "netlify"
	defer func() { logoutProvider = "blocks" }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"logout", "--provider", "netlify"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("logout --provider netlify: %v", err)
		}
	})

	if !strings.Contains(output, "Logged out of Netlify") {
		t.Errorf("output = %q, want 'Logged out of Netlify'", output)
	}
}

// TestLogoutProviderUnknown verifies that an unknown --provider value returns an error.
func TestLogoutProviderUnknown(t *testing.T) {
	logoutProvider = "aws"
	defer func() { logoutProvider = "blocks" }()

	rootCmd.SetArgs([]string{"logout", "--provider", "aws"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error = %q, want 'unknown provider'", err.Error())
	}
}

// TestLogoutProviderPreservesOthers verifies that logging out of one provider
// does not remove credentials for other providers.
func TestLogoutProviderPreservesOthers(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	auth.SetProviderCredential(credFile, "cloudflare", &auth.ProviderEntry{AccessToken: "cf_keep"})
	auth.SetProviderCredential(credFile, "vercel", &auth.ProviderEntry{AccessToken: "vc_keep"})

	logoutProvider = "netlify"
	defer func() { logoutProvider = "blocks" }()

	captureStdout(func() {
		rootCmd.SetArgs([]string{"logout", "--provider", "netlify"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.Execute()
	})

	cf, _ := auth.GetProviderCredential(credFile, "cloudflare")
	if cf == nil || cf.AccessToken != "cf_keep" {
		t.Errorf("cloudflare token lost after netlify logout: %v", cf)
	}
	vc, _ := auth.GetProviderCredential(credFile, "vercel")
	if vc == nil || vc.AccessToken != "vc_keep" {
		t.Errorf("vercel token lost after netlify logout: %v", vc)
	}
}
