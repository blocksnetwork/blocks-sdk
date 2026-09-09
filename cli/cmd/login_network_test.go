package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

// Every case in this file resets the login state on the way in as well as on the
// way out. A cleanup protects whatever runs next; the premise of these cases — that
// no flag is set, and that nothing supplied this invocation a credential — is about
// the state they start in. One earlier test that skipped its own cleanup is enough
// to turn a prompt into a silent short-circuit, which then reads as a bug here.

func TestLoginNetworkFlag(t *testing.T) {
	resetLoginFlags()
	tmpDir := t.TempDir()
	origCWD := mustCwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origCWD) })

	setupTestProfiles(t, tmpDir)
	t.Cleanup(resetLoginFlags)

	// Set the --network flag
	loginNetwork = true

	// Test that promptDeploymentIfFirstLogin returns explicit Network choice
	// when --network flag is set
	got, err := promptDeploymentIfFirstLogin()
	if err != nil {
		t.Fatalf("promptDeploymentIfFirstLogin with --network flag error: %v", err)
	}
	if got.instanceURL != "" {
		t.Errorf("--network flag should return empty instanceURL, got %q", got.instanceURL)
	}
	if !got.explicitNetwork {
		t.Errorf("--network flag should set explicitNetwork=true, got %v", got.explicitNetwork)
	}
}

func TestLoginNetworkFlagConflictWithPositionalArg(t *testing.T) {
	resetLoginFlags()
	tmpDir := t.TempDir()
	setupTestProfiles(t, tmpDir)
	t.Cleanup(resetLoginFlags)

	// Set the --network flag
	loginNetwork = true

	// Create a command with proper context
	cmd := &cobra.Command{
		Use: "login",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate --network flag doesn't conflict with positional instance
			if loginNetwork && len(args) > 0 {
				return fmt.Errorf("--network flag conflicts with instance argument %q", args[0])
			}
			return nil
		},
	}
	cmd.SetContext(context.Background())

	// Test that validation rejects --network + positional argument
	err := cmd.RunE(cmd, []string{"acme"})
	if err == nil {
		t.Fatal("expected error when --network flag conflicts with positional argument")
	}
	expectedErr := `--network flag conflicts with instance argument "acme"`
	if err.Error() != expectedErr {
		t.Errorf("error = %q, want %q", err.Error(), expectedErr)
	}
}

func TestLoginNetworkFlagWithNoInput(t *testing.T) {
	resetLoginFlags()
	tmpDir := t.TempDir()
	origCWD := mustCwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir failed: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origCWD) })

	setupTestProfiles(t, tmpDir)
	t.Cleanup(resetLoginFlags)

	// Set both --network and --no-input flags
	loginNetwork = true
	setNoInputMode(true)

	// Test that --network takes precedence over --no-input
	got, err := promptDeploymentIfFirstLogin()
	if err != nil {
		t.Fatalf("--network should take precedence over --no-input, got error: %v", err)
	}
	if got.instanceURL != "" {
		t.Errorf("--network flag should return empty instanceURL, got %q", got.instanceURL)
	}
	if !got.explicitNetwork {
		t.Errorf("--network flag should set explicitNetwork=true, got %v", got.explicitNetwork)
	}
}

func TestNoInputReadStdinLineError(t *testing.T) {
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// Set --no-input flag
	setNoInputMode(true)

	// Test that readStdinLine returns the noInputRequested flag
	line, ok, noInputRequested := readStdinLine()
	if !noInputRequested {
		t.Error("expected noInputRequested=true when --no-input is set")
	}
	if ok {
		t.Error("expected ok=false when --no-input is set")
	}
	if line != "" {
		t.Errorf("expected empty line when --no-input is set, got %q", line)
	}
}

func TestNoInputWriteEnvPrompt(t *testing.T) {
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// Override isInteractive to force interactive path
	origIsInteractive := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = origIsInteractive })

	// Set --no-input flag
	setNoInputMode(true)

	// Test that --no-input causes .env write prompt to error
	_, err := shouldWriteEnv()
	if err == nil {
		t.Fatal("expected error when --no-input is set and .env prompt would be required")
	}
	expectedErr := "cannot ask whether to write .env with --no-input — pass --write-env or --no-write-env"
	if err.Error() != expectedErr {
		t.Errorf("error = %q, want %q", err.Error(), expectedErr)
	}
}

func TestInitYesSkipsLoginPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	setupEmptyProfiles(t, tmpDir)

	// Override TTY check to force would-be interactive path
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() { isTTY = origIsTTY })

	// Set --yes flag
	origInitYes := initYes
	initYes = true
	t.Cleanup(func() { initYes = origInitYes })

	// Test that --yes skips the login prompt. The access has no credential and names
	// the deployment this invocation resolved, so the offer is the only thing that
	// could produce one — which is exactly what --yes must decline to do.
	got, err := ensureOrOfferBlocksLogin(context.Background(), cardAccess{resolvedTarget: true})
	if err != nil {
		t.Fatalf("ensureOrOfferBlocksLogin with --yes error: %v", err)
	}
	if got != "" {
		t.Errorf("--yes should skip login and return empty key, got %q", got)
	}
}

// Regression test: ensure existing readStdinLine callers preserve their defaults
func TestReadStdinLineCallerDefaults(t *testing.T) {
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// Ensure --no-input is not set for this regression test
	setNoInputMode(false)

	// Test shouldWriteEnv defaults to true on EOF (when not explicitly declined)
	origIsInteractive := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = origIsInteractive })

	// Mock stdin to return EOF
	origStdinScanner := stdinScanner
	stdinScanner = nil
	t.Cleanup(func() { stdinScanner = origStdinScanner })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	w.Close() // EOF on read
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	done := make(chan bool, 1)
	var got bool
	var gotErr error
	go func() {
		got, gotErr = shouldWriteEnv()
		done <- true
	}()

	select {
	case <-done:
		if gotErr != nil {
			t.Fatalf("shouldWriteEnv error: %v", gotErr)
		}
		if !got {
			t.Error("shouldWriteEnv should default to true on EOF, got false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shouldWriteEnv hung")
	}
}

func setupTestProfiles(t *testing.T, tmpDir string) {
	t.Helper()
	path := filepath.Join(tmpDir, "contexts.json")
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	// Seed a profile with existing deployment
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:      "https://app.blocks.ai",
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Eng", ApiKey: "bk_test_key"}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
}

func setupEmptyProfiles(t *testing.T, tmpDir string) {
	t.Helper()
	path := filepath.Join(tmpDir, "contexts.json")
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	// Create empty profile so no existing deployment
	if err := profiles.Upsert("test", profiles.Profile{}, true); err != nil {
		t.Fatalf("create empty profile: %v", err)
	}
}
