package cmd

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
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
		case "/config.json":
			w.WriteHeader(http.StatusOK)
			// CDM config pointing back to this test server
			baseURL := "http://" + r.Host
			config := fmt.Sprintf(`{
				"playground": {"publishKey": "demo", "subscribeKey": "demo"},
				"network": {"publishKey": "demo", "subscribeKey": "demo"},
				"api": {"baseUrl": "%s", "clientId": "test"}
			}`, baseURL)
			w.Write([]byte(config))
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
//
// It also drops the credential this invocation resolved. That is not a flag, which
// is exactly why it belongs here: --api-key is read once per invocation and
// memoized in clictx, and credentialSupplied() answers from the memo rather than
// from loginApiKey. Clearing the variable without the memo leaves the next test
// believing a key was handed to it by a command that finished long ago — a login
// prompt that silently short-circuits, which looks like a bug in whichever test
// happens to run second.
func resetLoginFlags() {
	loginApiKey = ""
	loginApiKeyStdin = false
	loginWriteEnv = false
	loginNoWriteEnv = false
	loginDir = ""
	loginNetwork = false
	resetNoInputState()
	clictx.Reset()
	for _, name := range []string{"api-key", "api-key-stdin", "write-env", "no-write-env", "dir", "network"} {
		if f := loginCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// resetNoInputState puts back every piece of --no-input state a test can leave
// behind. It is process state rather than a login flag, but it is reached through
// this package's prompt primitives, so a test that sets it — through argv or
// directly — leaks it into every later prompt: the symptom is an unrelated test
// mysteriously refusing to prompt.
//
// All four pieces live here together because a partial reset is indistinguishable
// from no reset at all. The flag variable and cobra's Changed bit decide what
// PersistentPreRun propagates on the next Execute; the two prompt primitives are
// what the prompts themselves read.
func resetNoInputState() {
	rootNoInput = false
	if f := rootCmd.PersistentFlags().Lookup("no-input"); f != nil {
		f.Changed = false
	}
	setNoInputMode(false)
	wizard.SetNoInputMode(false)
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
	var writeEnvErr error
	go func() {
		got, writeEnvErr = shouldWriteEnv()
		done <- true
	}()

	select {
	case <-done:
		if writeEnvErr != nil {
			t.Fatalf("shouldWriteEnv() error: %v", writeEnvErr)
		}
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

// withoutATerminal replaces stdin with a pipe holding nothing, so the real
// isInteractive and isTTY predicates both report false and any read that should
// not have happened returns EOF at once instead of blocking. Tests use it rather
// than overriding the predicates, because the distinction under test is exactly
// what those predicates answer.
func withoutATerminal(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	origScanner := stdinScanner
	stdinScanner = nil
	t.Cleanup(func() {
		os.Stdin = orig
		r.Close()
		stdinScanner = origScanner
	})
	if isInteractive() || isTTY() {
		t.Fatal("premise: a pipe must not read as a terminal")
	}
}

// promptly runs fn and fails the test if it has not returned within a few
// seconds. Every case below is meant to answer without reading stdin at all, so a
// blocked read is the regression, and a bare call would hang the suite rather
// than report it.
func promptly[T any](t *testing.T, what string, fn func() (T, error)) (T, error) {
	t.Helper()
	type result struct {
		out T
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := fn()
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		return r.out, r.err
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("%s blocked instead of answering — it is reading stdin on a path that must not", what)
		return zero, nil
	}
}

// requireNoSuppliedCredential states the premise every --no-input prompt case rests
// on: nothing handed this invocation a key. The tier that answers "did the caller
// supply one" sits above the tier under test, so a case that reaches it proves
// nothing — and it reads a value memoized per invocation rather than a flag
// variable, which is what makes it survive a reset that only clears flags.
func requireNoSuppliedCredential(t *testing.T) {
	t.Helper()
	if credentialSupplied() {
		t.Fatal("premise: no credential may have been supplied to this invocation — a resolved --api-key answers the question before the tier under test is reached")
	}
}

// The tier order the .env question resolves in, pinned at its one contentious step:
// a key the caller supplied is an answer, so --no-input must NOT turn it into an
// error. Only a question with no answer left is refused. Getting this backwards
// would fail every `blocks --no-input login --api-key …`, which is the shape a
// coding agent and a CI job both use.
func TestASuppliedKeyAnswersTheEnvQuestionEvenUnderNoInput(t *testing.T) {
	restoreCLIState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)

	loginApiKey = "bk_supplied_by_the_caller"
	resolveCLIContext(t, loginCmd)
	if !credentialSupplied() {
		t.Fatal("premise: --api-key must register as a supplied credential")
	}
	setNoInputMode(true)

	writeEnv, err := promptly(t, "shouldWriteEnv with a supplied key", shouldWriteEnv)
	if err != nil {
		t.Fatalf("a supplied key answers the .env question; --no-input must not error: %v", err)
	}
	if writeEnv {
		t.Error("a supplied key means automation, which does not write .env without --write-env")
	}
}

// --no-input and "no terminal" are different requests, and only one of them lets
// the CLI answer this question itself. Off a terminal the .env question has a safe
// default and takes it; --no-input is a refusal to be guessed at as much as a
// refusal to be asked, so it has to name the flags that answer instead — even
// though, stdin being a pipe, no prompt could have been printed anyway.
func TestNoInputRefusesTheEnvQuestionOffATerminalToo(t *testing.T) {
	restoreCLIState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)
	// Resolved here rather than left to whatever ran before: the tier above the one
	// under test short-circuits on a supplied credential, and --api-key is read once
	// per invocation and memoized, so a key an earlier command resolved would answer
	// the question and hide the regression. Asserted as well as reset, so a leak that
	// gets past this reports itself instead of looking like the fix not working.
	resolveCLIContext(t, loginCmd)
	requireNoSuppliedCredential(t)
	setNoInputMode(true)

	dir := t.TempDir()
	loginDir = dir

	_, err := promptly(t, "maybeWriteEnv under --no-input", func() (struct{}, error) {
		return struct{}{}, maybeWriteEnv("bk_should_not_be_written", deploymentChoice{})
	})
	if err == nil {
		t.Fatal("--no-input must not silently decide the .env question off a terminal")
	}
	for _, want := range []string{"--no-input", "--write-env", "--no-write-env"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr == nil {
		t.Error("a refused question must leave the project .env alone")
	}
}

// The same distinction for the deployment question, which has the sharper
// consequence: the silent default is Blocks Network, so a CI job that asked not to
// be guessed at would authenticate against a deployment it never named.
func TestNoInputRefusesTheDeploymentQuestionOffATerminalToo(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)

	// Nothing has answered the question: a profile that records no deployment, no
	// --network, no instance argument and no supplied key.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	resolveCLIContext(t, loginCmd)
	requireNoSuppliedCredential(t)
	setNoInputMode(true)

	got, err := promptly(t, "promptDeploymentIfFirstLogin under --no-input", promptDeploymentIfFirstLogin)
	if err == nil {
		t.Fatalf("--no-input must not silently default to Blocks Network off a terminal, got %+v", got)
	}
	for _, want := range []string{"--no-input", "--network", "instance URL or short name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

// End to end through the root command, so the refusal is what the user actually
// gets from `blocks --no-input login` rather than only what a helper returns. The
// ambient state names no deployment either, so a regression cannot reach a real
// one: it fails on the missing backend URL instead, which is a different error and
// so still a failure here.
func TestNoInputLoginFailsNamingTheFlagsThatAnswerIt(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	withoutATerminal(t)
	t.Cleanup(resetNoInputState)

	// An empty HOME, so no cached remote config of the developer's can name a
	// deployment for this login to reach.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	openBrowserFunc = func(string) error { return nil }

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	out, err := promptly(t, "blocks --no-input login", func() (string, error) {
		return runLoginCapturing(t, "--no-input", "login")
	})
	if err == nil {
		t.Fatalf("blocks --no-input login must fail rather than pick a deployment:\n%s", out)
	}
	for _, want := range []string{"--no-input", "--network"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v\n%s", want, err, out)
		}
	}
	for name, p := range mustLoadProfiles(t).Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused login must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// The load-bearing complement, and the reason --no-input is checked separately
// from the TTY guards rather than folded into them: CI runs `blocks login` off a
// terminal today without the flag, and both questions must keep taking their
// silent default there — .env untouched, Blocks Network non-explicitly.
func TestOffATerminalWithoutNoInputBothQuestionsKeepTheirSilentDefault(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)
	if noInputMode {
		t.Fatal("premise: --no-input must not be set for this case")
	}

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	resolveCLIContext(t, loginCmd)
	requireNoSuppliedCredential(t)

	writeEnv, err := promptly(t, "shouldWriteEnv off a terminal", shouldWriteEnv)
	if err != nil {
		t.Fatalf("shouldWriteEnv must not error without --no-input: %v", err)
	}
	if writeEnv {
		t.Error("off a terminal and with no flag, .env must be left untouched")
	}

	choice, err := promptly(t, "promptDeploymentIfFirstLogin off a terminal", promptDeploymentIfFirstLogin)
	if err != nil {
		t.Fatalf("promptDeploymentIfFirstLogin must not error without --no-input: %v", err)
	}
	if choice.instanceURL != "" {
		t.Errorf("off a terminal nothing may be prompted for, got instanceURL=%q", choice.instanceURL)
	}
	if choice.explicitNetwork {
		t.Error("a default taken off a terminal is not an explicit Network choice")
	}
}
