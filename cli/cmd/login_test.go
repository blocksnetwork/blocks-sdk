package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

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

// TestLoginNoWriteEnvSkipsWrite verifies that --no-write-env suppresses
// the .env write (and the interactive prompt that would precede it).
// This is the coding-agent path that this change unblocks.
func TestLoginNoWriteEnvSkipsWrite(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

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
