package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// resetInitFlags zeros the package-level flag vars between tests.
func resetInitFlags() {
	initYes = false
	initLanguage = ""
	initMode = ""
	initType = ""
	initAgents = nil
	initBlocksBaseURL = ""
}

func TestEnsureOrOfferBlocksLoginUsesProfileKey(t *testing.T) {
	defer isolateProfiles(t)()
	// Point the legacy credentials path at an empty dir so ONLY the profile is present.
	tmpDir := t.TempDir()
	origCred := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) {
		return filepath.Join(tmpDir, "credentials.json"), nil
	}
	defer func() { auth.CredentialPathFunc = origCred }()

	// Seed a profile with a usable key (the canonical home).
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Eng", ApiKey: "bk_profile_key"}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	got := ensureOrOfferBlocksLogin(context.Background())
	if got != "bk_profile_key" {
		t.Errorf("ensureOrOfferBlocksLogin = %q, want bk_profile_key (must read profile, not re-prompt)", got)
	}
}

// fixtureDir resolves the absolute path of internal/cardfetch/testdata/.
// Anchored on this source file's location via runtime.Caller so it stays
// valid after tests change directories.
func fixtureDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "internal", "cardfetch", "testdata"))
	if err != nil {
		t.Fatalf("resolve fixtureDir: %v", err)
	}
	return abs
}

// fakeRegistryServer returns an httptest.Server that serves the cardfetch
// fixture body for any GET to /api/v1/registry/agents?agentName=<name>.
// Names not in the bodyByName map respond 404 with the not_found fixture.
func fakeRegistryServer(t *testing.T, bodyByName map[string]string) *httptest.Server {
	t.Helper()
	dir := fixtureDir(t)
	notFound, err := os.ReadFile(filepath.Join(dir, "not_found.json"))
	if err != nil {
		t.Fatalf("read not_found fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("agentName")
		body, ok := bodyByName[name]
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(notFound)
			return
		}
		raw, err := os.ReadFile(filepath.Join(dir, body))
		if err != nil {
			t.Errorf("read fixture %s: %v", body, err)
			http.Error(w, "fixture missing", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withFakeBackend wires both fake credentials and BLOCKS_BACKEND_URL.
func withFakeBackend(t *testing.T, srv *httptest.Server) {
	t.Helper()
	cleanup := setupFakeCredentials(t)
	t.Cleanup(cleanup)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
}

// withAnonymousBackend points the credential path at an empty temp dir (so
// auth.Load fails → optionalCredentials returns "") and wires
// BLOCKS_BACKEND_URL. Models a fresh machine that has never run 'blocks login'.
func withAnonymousBackend(t *testing.T, srv *httptest.Server) {
	t.Helper()
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, "blocks", "credentials.json")
	origFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credPath, nil }
	t.Cleanup(func() { auth.CredentialPathFunc = origFunc })
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
}

func TestInitWebappSingleAgentScaffolds(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "my_webapp", "--mode", "webapp", "--agent", "echo2"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	projDir := filepath.Join(dir, "my_webapp")
	for _, f := range []string{"web/index.html", "web/app.js", "web/styles.css", "README.md", "blocks.config.json"} {
		if _, err := os.Stat(filepath.Join(projDir, f)); err != nil {
			t.Errorf("expected %s to exist", f)
		}
	}
	// agent.yaml and src/ MUST NOT be present (vestigial provider companions removed).
	if _, err := os.Stat(filepath.Join(projDir, "agent.yaml")); err == nil {
		t.Error("agent.yaml should not be scaffolded for webapp projects")
	}
	if _, err := os.Stat(filepath.Join(projDir, "src")); err == nil {
		t.Error("src/ should not be scaffolded for webapp projects")
	}

	data, err := os.ReadFile(filepath.Join(projDir, "blocks.config.json"))
	if err != nil {
		t.Fatalf("read blocks.config.json: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse blocks.config.json: %v", err)
	}
	agents, ok := cfg["agents"].([]interface{})
	if !ok || len(agents) != 1 || agents[0] != "echo2" {
		t.Errorf("agents = %v, want [echo2]", cfg["agents"])
	}
	// The project folder must NOT be the agent name; that fallback was dropped.
	if _, err := os.Stat(filepath.Join(dir, "echo2")); err == nil {
		t.Error("expected no directory named after the agent; webapp folder must be the positional arg")
	}
}

func TestInitWebappMultiAgent_RepeatedFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{
		"echo2":  "echo2.json",
		"stest1": "stest1.json",
	})
	withFakeBackend(t, srv)

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "echo2", "--agent", "stest1"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	projDir := filepath.Join(dir, "demo")
	data, err := os.ReadFile(filepath.Join(projDir, "blocks.config.json"))
	if err != nil {
		t.Fatalf("read blocks.config.json: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse blocks.config.json: %v", err)
	}
	agents, ok := cfg["agents"].([]interface{})
	if !ok || len(agents) != 2 || agents[0] != "echo2" || agents[1] != "stest1" {
		t.Errorf("agents = %v, want [echo2 stest1]", cfg["agents"])
	}

	app, err := os.ReadFile(filepath.Join(projDir, "web", "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !strings.Contains(string(app), "signInAndGetClients") {
		t.Error("multi-agent app.js should use signInAndGetClients")
	}
}

func TestInitWebappMultiAgent_CommaSeparated(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{
		"echo2":  "echo2.json",
		"stest1": "stest1.json",
	})
	withFakeBackend(t, srv)

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "echo2,stest1"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	projDir := filepath.Join(dir, "demo")
	if _, err := os.Stat(filepath.Join(projDir, "web", "app.js")); err != nil {
		t.Errorf("expected web/app.js to exist")
	}
}

func TestInitWebappMissingAgent(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "my_webapp", "--mode", "webapp"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --agent is missing")
	}
	if !strings.Contains(err.Error(), "--mode webapp requires") {
		t.Errorf("error = %q, want '--mode webapp requires' wording", err.Error())
	}
}

func TestInitWebappSlashInAgent(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "acme/translator"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for namespaced agent name")
	}
	if !strings.Contains(err.Error(), "bare agent name") {
		t.Errorf("error = %q, want 'bare agent name' hint", err.Error())
	}
}

func TestInitWebappAgentNotFound(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{}) // every agent → 404
	withFakeBackend(t, srv)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "unknownAgent"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found' hint", err.Error())
	}
	// Project directory must not be left behind.
	if _, err := os.Stat(filepath.Join(dir, "demo")); err == nil {
		t.Error("project directory must not be left behind on fetch failure")
	}
}

// A fresh machine with no stored credentials must still scaffold a webapp that
// references only public agents — the registry fetch path supports anonymous
// public reads, so requiring login here was a regression.
func TestInitWebappPublicAgentScaffoldsAnonymously(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withAnonymousBackend(t, srv)

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "echo2"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected anonymous public scaffold to succeed, got: %v", err)
		}
	})

	if _, err := os.Stat(filepath.Join(dir, "demo", "blocks.config.json")); err != nil {
		t.Errorf("expected blocks.config.json to be scaffolded anonymously, got: %v", err)
	}
}

func TestInitWebappRequiresPositionalName(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	// Single-agent without positional name → error (no agent-name fallback).
	rootCmd.SetArgs([]string{"init", "--mode", "webapp", "--agent", "echo2"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for webapp scaffold without positional name")
	}
	if !strings.Contains(err.Error(), "require a project name") {
		t.Errorf("error = %q, want 'require a project name' wording", err.Error())
	}
	// Multi-agent without positional name → same error.
	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "--mode", "webapp", "--agent", "echo2", "--agent", "stest1"})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for multi-agent webapp without positional name")
	}
	if !strings.Contains(err.Error(), "require a project name") {
		t.Errorf("error = %q, want 'require a project name' wording", err.Error())
	}
}

func TestInitWebappRejectsLanguageFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "echo2", "--language", "node"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error when --language is combined with --mode webapp")
	}
	if !strings.Contains(err.Error(), "--language is not valid with --mode webapp") {
		t.Errorf("error = %q, want explanation that --language is not allowed with webapp", err.Error())
	}
}

func TestInitWebappPositionalDirName(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "mydir", "--mode", "webapp", "--agent", "echo2"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	if _, err := os.Stat(filepath.Join(dir, "mydir", "web", "app.js")); err != nil {
		t.Errorf("expected directory mydir/web/app.js to exist")
	}
	if _, err := os.Stat(filepath.Join(dir, "echo2")); err == nil {
		t.Error("default-named directory should not exist when positional name is provided")
	}
}

// Each of these previously-valid flags is now unknown; cobra rejects them.
func TestInitWebappRejectsLegacyTemplateFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "--template", "chat", "--agent", "echo2"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected unknown-flag error for --template")
	}
	if !strings.Contains(err.Error(), "unknown flag") && !strings.Contains(err.Error(), "--template") {
		t.Errorf("error = %q, want unknown-flag rejection", err.Error())
	}
}

func TestInitWebappRejectsLegacyWebOnlyFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "--mode", "webapp", "--agent", "echo2", "--web-only"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected unknown-flag error for --web-only")
	}
	if !strings.Contains(err.Error(), "unknown flag") && !strings.Contains(err.Error(), "--web-only") {
		t.Errorf("error = %q, want unknown-flag rejection", err.Error())
	}
}

func TestInitWebappRejectsLegacyAgentsPluralFlag(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "--mode", "webapp", "--agents", "echo2"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected unknown-flag error for --agents (plural)")
	}
	if !strings.Contains(err.Error(), "unknown flag") && !strings.Contains(err.Error(), "--agents") {
		t.Errorf("error = %q, want unknown-flag rejection", err.Error())
	}
}

// --type is a deprecated alias for --mode, kept for one release. It maps
// provider/consumer through, rejects the webapp value (which postdates the
// flag), and refuses a conflicting --mode.
func TestInitLegacyTypeFlagMapsToMode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "democonsumer", "--type", "consumer", "--yes"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--type consumer should map to --mode consumer, got error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "democonsumer")); statErr != nil {
		t.Errorf("expected consumer project scaffolded, stat err: %v", statErr)
	}
}

func TestInitLegacyTypeFlagRejectsWebapp(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--type", "webapp", "--agent", "echo2"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for --type webapp")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("error = %q, want 'unsupported type' rejection", err.Error())
	}
}

func TestInitLegacyTypeFlagConflictsWithMode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--type", "consumer", "--mode", "provider", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for conflicting --type and --mode")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error = %q, want 'conflict' wording", err.Error())
	}
}

func TestInitWebappAgentFlagWithoutWebappMode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "myagent", "--agent", "echo2", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for --agent without --mode webapp")
	}
	if !strings.Contains(err.Error(), "only valid with --mode webapp") {
		t.Errorf("error = %q, want '--agent is only valid with --mode webapp' wording", err.Error())
	}
}

// TestInitWebappRejectsDuplicateAgent verifies the dedup guard fires before
// any network call. Mirrors the uniqueness rule blocks.config.json
// validation and signInAndGetClients enforce downstream.
func TestInitWebappRejectsDuplicateAgent(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	rootCmd.SetArgs([]string{"init", "demo", "--mode", "webapp", "--agent", "echo2", "--agent", "echo2"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for duplicate --agent values")
	}
	if !strings.Contains(err.Error(), "must be unique") {
		t.Errorf("error = %q, want 'must be unique' wording", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "demo")); !os.IsNotExist(statErr) {
		t.Error("expected no project directory to be created on validation failure")
	}
}

// TestInitWebappRejectsTooManyAgents verifies the 25-agent ceiling fires
// before any network call. Mirrors blocks.config.json's max-25 rule.
func TestInitWebappRejectsTooManyAgents(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	resetInitFlags()
	args := []string{"init", "demo", "--mode", "webapp"}
	for i := 0; i < 26; i++ {
		args = append(args, "--agent", fmt.Sprintf("agent%d", i))
	}
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for >25 --agent values")
	}
	if !strings.Contains(err.Error(), "at most 25") {
		t.Errorf("error = %q, want 'at most 25' wording", err.Error())
	}
}
