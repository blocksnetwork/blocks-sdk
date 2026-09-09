package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func TestLoadEnvFileWithEmpty(t *testing.T) {
	// Test 1: Empty BLOCKS_API_KEY should not be set in environment
	dir := t.TempDir()
	t.Chdir(dir)

	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Clear any existing value
	t.Setenv("BLOCKS_API_KEY", "")
	if err := os.Unsetenv("BLOCKS_API_KEY"); err != nil {
		t.Fatal(err)
	}

	loadEnvFile(".env")

	if os.Getenv("BLOCKS_API_KEY") != "" {
		t.Errorf("BLOCKS_API_KEY should not be set in environment after loading empty value, got %q", os.Getenv("BLOCKS_API_KEY"))
	}
}

func TestLoadEnvFileWithRealValue(t *testing.T) {
	// Test 2: Non-empty value should be set
	dir := t.TempDir()
	t.Chdir(dir)

	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=bk_real\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Clear any existing value
	t.Setenv("BLOCKS_API_KEY", "")
	if err := os.Unsetenv("BLOCKS_API_KEY"); err != nil {
		t.Fatal(err)
	}

	loadEnvFile(".env")

	if os.Getenv("BLOCKS_API_KEY") != "bk_real" {
		t.Errorf("BLOCKS_API_KEY = %q, want %q", os.Getenv("BLOCKS_API_KEY"), "bk_real")
	}
}

func TestLoadEnvFilePreservesExistingEnv(t *testing.T) {
	// Test 3: Pre-existing env value should win over file value
	dir := t.TempDir()
	t.Chdir(dir)

	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=bk_from_file\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Set env var first
	t.Setenv("BLOCKS_API_KEY", "bk_from_env")

	loadEnvFile(".env")

	if os.Getenv("BLOCKS_API_KEY") != "bk_from_env" {
		t.Errorf("BLOCKS_API_KEY = %q, want %q (existing env should win)", os.Getenv("BLOCKS_API_KEY"), "bk_from_env")
	}
}

// A mistyped --profile used to be discarded silently, and "discarded" did not mean
// "selected nothing": the resolver fell through to the next tier — an ambient backend
// URL, the legacy store, the linker default, a redirected CDM endpoint — so the command
// acted on whatever those named. For `unregister` that is an irreversible action on a
// deployment the user never chose. It is refused now.
//
// `login` is exempt on purpose, because `--profile` also names the profile a login
// creates; holding it to "must already exist" would break creating one.
func TestASelectedProfileThatDoesNotExistIsRefused(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	orig := rootProfile
	t.Cleanup(func() { rootProfile = orig })

	// Nothing selected: every command proceeds, since the default profile always exists.
	rootProfile = ""
	if err := requireSelectedProfileExists(publishCmd); err != nil {
		t.Fatalf("no explicit selection must not be refused: %v", err)
	}

	// A name the store does not have, on a command that reads a profile.
	rootProfile = "prod-typo"
	err := requireSelectedProfileExists(publishCmd)
	if err == nil {
		t.Fatal("a --profile naming no saved profile must be refused, not ignored")
	}
	if !strings.Contains(err.Error(), "prod-typo") {
		t.Errorf("the refusal must name the profile asked for, got %v", err)
	}

	// The same name is fine for login, which creates it.
	if err := requireSelectedProfileExists(loginCmd); err != nil {
		t.Errorf("login names the profile it creates, so it must not be refused: %v", err)
	}

	// And for the profile subcommands, which manage the store itself.
	if err := requireSelectedProfileExists(profileCmd); err != nil {
		t.Errorf("profile management must not be refused: %v", err)
	}

	// BLOCKS_PROFILE is the same explicit selection by another route.
	rootProfile = ""
	t.Setenv("BLOCKS_PROFILE", "prod-typo")
	if err := requireSelectedProfileExists(publishCmd); err == nil {
		t.Error("BLOCKS_PROFILE naming no saved profile must be refused too")
	}
}
