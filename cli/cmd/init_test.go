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
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

// resetInitFlags zeros the package-level flag vars between tests.
func resetInitFlags() {
	initYes = false
	initLanguage = ""
	initMode = ""
	initType = ""
	initAgents = nil
	initBlocksBaseURL = ""
	initBackendURL = ""
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

func TestInitWebapp_BakesProfileBackendURL(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})

	// Hermetic profile store, and no env override — so the active profile's
	// BaseURL is what resolveWebappBackendURL must pick. The profile BaseURL
	// doubles as the card-fetch backend, so it points at the fake registry.
	defer isolateProfiles(t)()
	t.Setenv("BLOCKS_BACKEND_URL", "")
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: srv.URL,
		Orgs:    map[string]profiles.OrgKey{},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	// Isolate the credential path at an empty dir so card-fetch runs
	// anonymously (the public fixture needs no auth) instead of reading the
	// developer's real credentials.json.
	credDir := t.TempDir()
	origCred := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) {
		return filepath.Join(credDir, "credentials.json"), nil
	}
	defer func() { auth.CredentialPathFunc = origCred }()

	resetInitFlags()
	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "my_webapp", "--mode", "webapp", "--agent", "echo2"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// The active profile's BaseURL (srv.URL) must be baked into config,
	// NOT the default https://app.blocks.ai.
	bc, err := readBlocksConfig(filepath.Join(dir, "my_webapp"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if bc["backendBaseUrl"] != srv.URL {
		t.Errorf("backendBaseUrl = %v, want %q (from active profile BaseURL)", bc["backendBaseUrl"], srv.URL)
	}
}

func TestInitWebapp_BackendURLFlagWins(t *testing.T) {
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
		rootCmd.SetArgs([]string{"init", "my_webapp", "--mode", "webapp",
			"--agent", "echo2", "--backend-url", srv.URL})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	bc, err := readBlocksConfig(filepath.Join(dir, "my_webapp"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if bc["backendBaseUrl"] != srv.URL {
		t.Errorf("backendBaseUrl = %v, want the --backend-url flag value", bc["backendBaseUrl"])
	}
}

func TestInitWebapp_BackendURLFlagTrailingSlashTrimmed(t *testing.T) {
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
		rootCmd.SetArgs([]string{"init", "my_webapp", "--mode", "webapp",
			"--agent", "echo2", "--backend-url", srv.URL + "/"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// blocks.config.json must store the slash-free origin.
	bc, err := readBlocksConfig(filepath.Join(dir, "my_webapp"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if bc["backendBaseUrl"] != srv.URL {
		t.Errorf("backendBaseUrl = %v, want the trailing slash stripped", bc["backendBaseUrl"])
	}

	// The baked app.js resolver literal must be slash-free, or auto-resume's
	// partition key diverges from sign-in's.
	app, err := os.ReadFile(filepath.Join(dir, "my_webapp", "web", "app.js"))
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !strings.Contains(string(app), fmt.Sprintf(`"%s"`, srv.URL)) {
		t.Errorf("app.js must bake the slash-free backend literal")
	}
	if strings.Contains(string(app), fmt.Sprintf(`"%s/"`, srv.URL)) {
		t.Errorf("app.js must NOT bake a trailing-slash backend literal")
	}
}

func TestInitWebapp_RejectsInvalidBackendURL(t *testing.T) {
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)
	resetInitFlags()
	initBackendURL = "not a url"
	err := runWebapp(context.Background(), "my_webapp")
	initBackendURL = ""
	if err == nil || !strings.Contains(err.Error(), "backend-url") {
		t.Fatalf("expected --backend-url validation error, got: %v", err)
	}
}

func TestInitWebappRejectsNonLoopbackHTTPBackendURL(t *testing.T) {
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)
	resetInitFlags()
	initAgents = []string{"echo2"}
	initBackendURL = "http://staging.example.com"
	err := runWebapp(context.Background(), "my_webapp")
	initBackendURL = ""
	initAgents = nil
	if err == nil || !strings.Contains(err.Error(), "backend-url") {
		t.Fatalf("expected --backend-url scheme validation error, got: %v", err)
	}
}

// TestInitWebappRejectsNonLoopbackHTTPAssetURL locks in the intentional
// (documented in CHANGELOG) tightening: --blocks-base-url is validated with the
// same https/loopback rule as the backend, so a cleartext non-loopback asset
// host is rejected at init instead of serving the widget bundle over http.
func TestInitWebappRejectsNonLoopbackHTTPAssetURL(t *testing.T) {
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)
	resetInitFlags()
	initAgents = []string{"echo2"}
	initBlocksBaseURL = "http://cdn.example.com"
	err := runWebapp(context.Background(), "my_webapp")
	initBlocksBaseURL = ""
	initAgents = nil
	if err == nil || !strings.Contains(err.Error(), "blocks-base-url") {
		t.Fatalf("expected --blocks-base-url scheme validation error, got: %v", err)
	}
}

// TestInitWebapp_RejectsNonLoopbackBackendFromEnv proves the resolved backend
// origin is validated even when it comes from BLOCKS_BACKEND_URL rather than
// the --backend-url flag. Without validation at the scaffold choke point, a
// plain-http non-loopback backend would be baked into web/app.js and only
// rejected later at browser sign-in.
func TestInitWebapp_RejectsNonLoopbackBackendFromEnv(t *testing.T) {
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)
	// Override the loopback httptest URL withFakeBackend set: model an operator
	// whose BLOCKS_BACKEND_URL points at a cleartext non-loopback host.
	t.Setenv("BLOCKS_BACKEND_URL", "http://staging.example.com")

	resetInitFlags()
	initAgents = []string{"echo2"}
	err := runWebapp(context.Background(), "my_webapp")
	initAgents = nil

	if err == nil {
		t.Fatal("expected the resolved backend URL to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "backend URL") {
		t.Fatalf("error should name the resolved backend URL; got: %v", err)
	}
}

// TestInitWebapp_SurfacesProfileError proves that when backend resolution hits a
// real profile-store error (here: BLOCKS_PROFILE names a profile that doesn't
// exist), runWebapp fails loudly instead of silently baking the asset-base
// fallback into the scaffold.
func TestInitWebapp_SurfacesProfileError(t *testing.T) {
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	withFakeBackend(t, srv)
	restore := isolateProfiles(t)
	defer restore()
	// No --backend-url and no BLOCKS_BACKEND_URL, so resolution reaches the
	// profile tier; name a nonexistent profile to force profiles.Active() error.
	t.Setenv("BLOCKS_BACKEND_URL", "")
	t.Setenv("BLOCKS_PROFILE", "does-not-exist")

	resetInitFlags()
	initAgents = []string{"echo2"}
	err := runWebapp(context.Background(), "my_webapp")
	initAgents = nil

	if err == nil {
		t.Fatal("expected a profile-resolution error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should name the missing profile; got: %v", err)
	}
}

// readBlocksConfig is a tiny helper that unmarshals blocks.config.json.
func readBlocksConfig(projDir string) (map[string]interface{}, error) {
	data, err := os.ReadFile(filepath.Join(projDir, "blocks.config.json"))
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// TestPrintWebappResolvedURLs_HintReflectsAssetFallback verifies the fallback
// hint fires only when the backend was NOT explicitly resolved (it fell back to
// the asset base), and stays silent both when a distinct backend was resolved
// and when an explicit backend happens to equal the asset base.
func TestPrintWebappResolvedURLs_HintReflectsAssetFallback(t *testing.T) {
	const customAsset = "https://assets.example.test"

	// Backend fell back to the asset base (no --backend-url/env/profile/ldflag):
	// the hint MUST fire. This is the exact footgun the PR exists to surface.
	out := captureStdout(func() {
		printWebappResolvedURLs(customAsset, customAsset, false)
	})
	if !strings.Contains(out, "--backend-url") {
		t.Errorf("expected the fallback hint when backend fell back to asset base; got:\n%s", out)
	}

	// A distinct backend was resolved: no hint.
	out = captureStdout(func() {
		printWebappResolvedURLs(customAsset, "https://blocks.acme.com", true)
	})
	if strings.Contains(out, "--backend-url") {
		t.Errorf("did not expect the fallback hint when a distinct backend was resolved; got:\n%s", out)
	}

	// An EXPLICIT backend that happens to equal the asset base: no hint, because
	// the user did specify a backend.
	out = captureStdout(func() {
		printWebappResolvedURLs(customAsset, customAsset, true)
	})
	if strings.Contains(out, "--backend-url") {
		t.Errorf("did not expect the fallback hint when the backend was explicitly resolved; got:\n%s", out)
	}
}

// TestValidateWebappURLFlags checks the shared flag validator both webapp
// paths call. It must reject a cleartext non-loopback --blocks-base-url and a
// malformed --backend-url, and accept empty/https/loopback values.
func TestValidateWebappURLFlags(t *testing.T) {
	cases := []struct {
		name       string
		blocksBase string
		backend    string
		wantErrSub string // "" means expect no error
	}{
		{"both empty", "", "", ""},
		{"https both", "https://cdn.example.com", "https://api.example.com", ""},
		{"loopback http asset", "http://localhost:4242", "", ""},
		{"cleartext non-loopback asset", "http://cdn.example.com", "", "blocks-base-url"},
		{"malformed backend", "", "not a url", "backend-url"},
		{"cleartext non-loopback backend", "", "http://staging.example.com", "backend-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetInitFlags()
			initBlocksBaseURL = tc.blocksBase
			initBackendURL = tc.backend
			err := validateWebappURLFlags()
			resetInitFlags()
			if tc.wantErrSub == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
			}
		})
	}
}

// TestRunWebappWizard_RejectsCleartextAssetHost is the regression guard for the
// interactive-picker security hole: the wizard path (runWebappWizard) must
// reject a cleartext non-loopback --blocks-base-url with the SAME rule the
// flag-driven path enforces, so it can never bake an http:// widget-bundle host
// into index.html. Uses a valid https backend so the ONLY thing that can fail
// is the asset-host guard we are adding.
func TestRunWebappWizard_RejectsCleartextAssetHost(t *testing.T) {
	defer isolateProfiles(t)()
	t.Setenv("BLOCKS_BACKEND_URL", "https://api.example.com")

	resetInitFlags()
	initBlocksBaseURL = "http://cdn.example.com"
	err := runWebappWizard(context.Background())
	resetInitFlags()

	if err == nil || !strings.Contains(err.Error(), "blocks-base-url") {
		t.Fatalf("expected --blocks-base-url validation error from the wizard path, got: %v", err)
	}
}

// TestInitWebapp_CardFetchUsesResolvedBackend proves that when --backend-url is
// explicit, the CLI fetches agent cards from the SAME origin it bakes into the
// page. Before the fix, card lookup used resolveBackendURL() (env/profile),
// which could snapshot cards from a different deployment than the one the page
// signs into. Here only the --backend-url server serves the card; the env
// backend serves nothing, so a passing scaffold proves the card came from the
// flag origin.
func TestInitWebapp_CardFetchUsesResolvedBackend(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	// Server that HAS the card — this is what --backend-url points at.
	cardSrv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})
	// Env backend that serves NO agents (empty registry) — the old code would
	// have queried this and failed to find echo2.
	emptySrv := fakeRegistryServer(t, map[string]string{})
	t.Setenv("BLOCKS_BACKEND_URL", emptySrv.URL)

	credDir := t.TempDir()
	origCred := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) {
		return filepath.Join(credDir, "credentials.json"), nil
	}
	defer func() { auth.CredentialPathFunc = origCred }()

	resetInitFlags()
	initAgents = []string{"echo2"}
	initBackendURL = cardSrv.URL
	var err error
	captureStdout(func() {
		err = runWebapp(context.Background(), "my_webapp")
	})
	resetInitFlags()

	if err != nil {
		t.Fatalf("expected card fetch to succeed against --backend-url origin, got: %v", err)
	}
	bc, rerr := readBlocksConfig(filepath.Join(dir, "my_webapp"))
	if rerr != nil {
		t.Fatalf("read config: %v", rerr)
	}
	if bc["backendBaseUrl"] != cardSrv.URL {
		t.Errorf("backendBaseUrl = %v, want the --backend-url origin %q", bc["backendBaseUrl"], cardSrv.URL)
	}
}

// TestScaffoldWebappProject_RejectsCleartextAssetHost is a defense-in-depth
// guard: even if cfg.BlocksBaseURL were ever populated from a non-flag source
// (profile/env) that bypasses validateWebappURLFlags, the scaffold choke point
// must reject a cleartext non-loopback asset host before baking it into
// index.html. A valid https backend isolates the asset-host check as the only
// possible failure.
func TestScaffoldWebappProject_RejectsCleartextAssetHost(t *testing.T) {
	cfg := wizard.Config{
		Name:           "my_webapp",
		Mode:           "webapp",
		Agents:         []string{"echo2"},
		BlocksBaseURL:  "http://cdn.example.com",
		BackendBaseURL: "https://api.example.com",
	}
	err := scaffoldWebappProject(context.Background(), cfg, blocksapi.NewClient("https://api.example.com", ""))
	if err == nil || !strings.Contains(err.Error(), "asset") {
		t.Fatalf("expected asset-host validation error at scaffold layer, got: %v", err)
	}
}

// TestInitWebapp_BakesProfileAssetHost proves the flag-driven path now bakes the
// active profile's origin as the widget-bundle asset host in index.html — not the
// hardcoded https://app.blocks.ai. Before the fix, index.html loaded the widget
// from app.blocks.ai even though the backend was correctly profile-aware, which
// is fatal on a network-restricted enterprise instance. Both init paths share
// resolveWebappURLs (unit-tested in helpers_test.go); this guards the wiring.
func TestInitWebapp_BakesProfileAssetHost(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	// The profile origin doubles as the card-fetch backend (explicit source), so
	// it must serve the card. httptest gives an http loopback URL, which
	// ValidateBackendBaseURL accepts (loopback is exempt from the https rule).
	srv := fakeRegistryServer(t, map[string]string{"echo2": "echo2.json"})

	defer isolateProfiles(t)()
	t.Setenv("BLOCKS_BACKEND_URL", "")
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: srv.URL,
		Orgs:    map[string]profiles.OrgKey{},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	credDir := t.TempDir()
	origCred := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) {
		return filepath.Join(credDir, "credentials.json"), nil
	}
	defer func() { auth.CredentialPathFunc = origCred }()

	resetInitFlags()
	initAgents = []string{"echo2"}
	var err error
	captureStdout(func() {
		err = runWebapp(context.Background(), "my_webapp")
	})
	resetInitFlags()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	indexBytes, rerr := os.ReadFile(filepath.Join(dir, "my_webapp", "web", "index.html"))
	if rerr != nil {
		t.Fatalf("read index.html: %v", rerr)
	}
	indexHTML := string(indexBytes)

	// index.html JS-escapes the asset host (forward slashes → \/). Assert the
	// profile origin is present as the widget host and app.blocks.ai is absent.
	escapedProfile := strings.ReplaceAll(srv.URL, "/", `\/`)
	if !strings.Contains(indexHTML, escapedProfile) {
		t.Errorf("index.html must load the widget from the profile origin %q; got:\n%s", srv.URL, indexHTML)
	}
	if strings.Contains(indexHTML, "app.blocks.ai") {
		t.Errorf("index.html must NOT fall back to app.blocks.ai when a profile origin is active; got:\n%s", indexHTML)
	}
}
