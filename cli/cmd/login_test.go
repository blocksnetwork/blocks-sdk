package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// loginTestServer returns an httptest server that makes the login discovery +
// org-resolution calls hermetic: cli-config is a stock-Network 404, and
// publish-context returns a fixed org so the --api-key path can seed a profile.
func loginTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli-config":
			w.WriteHeader(http.StatusNotFound) // older/stock backend → non-enterprise
		case "/api/v1/registry/publish-context":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"orgId":"org-login","orgName":"Login Org","agentCount":0}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// loginServerOrgResolutionFails is like loginTestServer but publish-context
// errors, so the --api-key path cannot resolve an org for the supplied key.
func loginServerOrgResolutionFails(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli-config":
			w.WriteHeader(http.StatusNotFound) // non-enterprise
		case "/api/v1/registry/publish-context":
			w.WriteHeader(http.StatusInternalServerError) // org lookup fails
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// activeProfileKey reads the active profile's default-org API key for assertions.
func activeProfileKey(t *testing.T) string {
	t.Helper()
	_, p, err := profiles.Active()
	if err != nil {
		t.Fatalf("profiles.Active: %v", err)
	}
	k, ok := p.DefaultOrgKey()
	if !ok {
		t.Fatalf("active profile has no default org key")
	}
	return k.ApiKey
}

// resetLoginFlags clears both the package-level flag variables and
// cobra's per-flag `Changed` tracking. Cobra's MarkFlagsMutuallyExclusive
// reads `Changed`, which persists across rootCmd.Execute() calls in the
// same test binary; without this reset, a test that sets --write-env
// would cause a later test that sets --no-write-env to fail with a
// spurious mutex error.
func resetLoginFlags() {
	loginApiKey = ""
	loginApiKeyStdin = false
	loginWriteEnv = false
	loginNoWriteEnv = false
	loginDir = ""
	for _, name := range []string{"api-key", "api-key-stdin", "write-env", "no-write-env", "dir"} {
		if f := loginCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

func TestLoginWithApiKeyFlagNoEnvPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()

	// Create a .env in a target dir to verify it's NOT touched
	targetDir := t.TempDir()
	envFile := filepath.Join(targetDir, ".env")
	os.WriteFile(envFile, []byte("BLOCKS_API_KEY=\n"), 0644)

	oldDir, _ := os.Getwd()
	os.Chdir(targetDir)
	defer os.Chdir(oldDir)

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_no_env_prompt_test"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --api-key failed: %v", err)
	}

	// .env should NOT be updated (--api-key suppresses prompt)
	data, _ := os.ReadFile(envFile)
	if strings.Contains(string(data), "bk_no_env_prompt_test") {
		t.Error("--api-key should not write .env without --write-env")
	}
}

func TestLoginWriteEnvDir(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()

	targetDir := t.TempDir()

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_dir_test_key_12345", "--write-env", "--dir", targetDir})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --api-key --write-env --dir failed: %v", err)
	}

	// .env should be created in the target dir
	data, err := os.ReadFile(filepath.Join(targetDir, ".env"))
	if err != nil {
		t.Fatalf("expected .env in target dir: %v", err)
	}
	if !strings.Contains(string(data), "BLOCKS_API_KEY=bk_dir_test_key_12345") {
		t.Errorf("expected key in .env, got: %q", string(data))
	}
}

func TestLoginWithApiKeyFlag(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	// Reset flags
	resetLoginFlags()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"login", "--api-key", "bk_test_key_12345678"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("login --api-key failed: %v", err)
		}
	})

	// Should save the key into the active profile (org resolved via publish-context).
	if got := activeProfileKey(t); got != "bk_test_key_12345678" {
		t.Errorf("saved key = %q, want bk_test_key_12345678", got)
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

// TestLoginApiKeyOrgResolutionFailsErrors verifies that when the --api-key path
// cannot resolve an org for the key (publish-context errors), login fails with a
// clear error and does NOT print success or store an unusable key.
func TestLoginApiKeyOrgResolutionFailsErrors(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginServerOrgResolutionFails(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_orphan_key_123", "--no-write-env"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when the org cannot be resolved for the API key")
	}
	if !strings.Contains(err.Error(), "could not determine the organization") {
		t.Errorf("error should explain org resolution failed, got: %v", err)
	}

	// The unusable key must NOT have been stored under any org.
	_, p, perr := profiles.Active()
	if perr != nil {
		t.Fatalf("profiles.Active: %v", perr)
	}
	if _, ok := p.DefaultOrgKey(); ok {
		t.Error("key should not be stored when org resolution fails")
	}
}

// TestLoginNoWriteEnvSkipsWrite verifies that --no-write-env suppresses
// the .env write (and the interactive prompt that would precede it).
// This is the coding-agent path that this change unblocks.
func TestLoginNoWriteEnvSkipsWrite(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()

	targetDir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(targetDir)
	defer os.Chdir(oldDir)

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_no_write_env_test", "--no-write-env"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --no-write-env failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, ".env")); err == nil {
		t.Error("--no-write-env should not create .env")
	}
}

// TestLoginConflictingWriteEnvFlags verifies that combining --write-env
// and --no-write-env errors out (via cobra's MarkFlagsMutuallyExclusive)
// instead of silently picking one. Asserts the error names both flags so
// the user knows what to remove.
func TestLoginConflictingWriteEnvFlags(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()

	resetLoginFlags()

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_conflict_test", "--write-env", "--no-write-env"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --write-env and --no-write-env are both set")
	}
	if !strings.Contains(err.Error(), "write-env") || !strings.Contains(err.Error(), "no-write-env") {
		t.Errorf("error should mention both flag names, got: %v", err)
	}
}

// TestShouldWriteEnvNonTTYNoFlag verifies that `shouldWriteEnv` returns
// false (no write, no prompt) when stdin is not a TTY and no flag is
// given — the deterministic fail-safe default. Regression test for the
// hang reported pre-fix: the function would have called
// bufio.Scanner(os.Stdin) and blocked waiting for input. The test
// guards against any regression that re-introduces a stdin read on this
// path. Calls shouldWriteEnv() directly so the test is unaffected by
// the upstream auth flow and the --api-key short-circuit.
func TestShouldWriteEnvNonTTYNoFlag(t *testing.T) {
	loginApiKey = ""
	loginApiKeyStdin = false
	loginWriteEnv = false
	loginNoWriteEnv = false

	// Replace stdin with a closed pipe so isInteractive() returns false.
	// A closed pipe also makes any accidental stdin read return EOF
	// immediately rather than blocking — but the test still wraps the
	// call in a wall-clock guard to catch a hypothetical hang.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	done := make(chan bool, 1)
	var got bool
	go func() {
		got = shouldWriteEnv()
		done <- true
	}()

	select {
	case <-done:
		if got {
			t.Error("shouldWriteEnv() returned true on non-TTY no-flag input; expected false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shouldWriteEnv() hung on non-TTY stdin (regression)")
	}
}

func TestLoginApiKeyDoesNotWriteLegacyBlocksSlot(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()

	rootCmd.SetArgs([]string{"login", "--api-key", "bk_no_legacy_slot", "--no-write-env"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --api-key failed: %v", err)
	}

	// Key must be in the profile (canonical home)...
	if got := activeProfileKey(t); got != "bk_no_legacy_slot" {
		t.Errorf("profile key = %q, want bk_no_legacy_slot", got)
	}
	// ...and the legacy credentials.json "blocks" slot must NOT have been written.
	entry, err := auth.GetProviderCredential(credFile, "blocks")
	if err != nil {
		t.Fatalf("GetProviderCredential: %v", err)
	}
	if entry != nil {
		t.Errorf("legacy blocks slot was written: %+v", entry)
	}
}
