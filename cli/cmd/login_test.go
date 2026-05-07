package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

func TestLoginWithApiKeyFlag(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	// Reset flags
	loginApiKey = ""
	loginApiKeyStdin = false

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--api-key", "bk_test_key_12345678"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --api-key failed: %v", err)
		}
	})

	// Should save credentials
	creds, err := auth.Load()
	if err != nil {
		t.Fatalf("credentials not saved: %v", err)
	}
	if creds.ApiKey != "bk_test_key_12345678" {
		t.Errorf("saved key = %q, want bk_test_key_12345678", creds.ApiKey)
	}

	// Should print masked key
	if !strings.Contains(output, "bk_tes...678") {
		t.Errorf("output should contain masked key, got: %q", output)
	}

	// Should NOT create .env
	if _, err := os.Stat(".env.test_login_check"); err == nil {
		t.Error("login should not create .env files")
	}
}
