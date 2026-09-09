package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
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

// runInitArgs drives `blocks init` the way a user does — through argv, so the
// persistent --no-input flag is parsed and propagated by the root command's
// PersistentPreRun rather than set on a package variable the real binary never
// touches. It isolates the profile store, the environment and the working
// directory, forces the session to look interactive (so a suppressed prompt can
// only be the flag's doing, not a missing TTY), and hands stdin a finite,
// already-closed answer: a wizard that still prompted would consume it and
// scaffold, and hit EOF rather than deadlocking the suite.
func runInitArgs(t *testing.T, stdin string, args ...string) (string, string, error) {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateAmbientState(t)
	forceTTY(t)
	resetNoInput(t)
	pipeStdin(t, stdin)
	resetInitFlags()
	t.Cleanup(resetInitFlags)

	dir := t.TempDir()
	t.Chdir(dir)

	var err error
	out := captureStdout(func() {
		rootCmd.SetArgs(append([]string{"init"}, args...))
		err = rootCmd.Execute()
	})
	return out, dir, err
}

// --no-input means never prompt, whatever stdin happens to be. Without a name
// there is nothing to default to, so init must report the missing argument
// instead of asking for it — and must not fall into the wizard, whose name
// prompt would have accepted the answer waiting on stdin.
func TestNoInputInitReportsMissingNameInsteadOfPrompting(t *testing.T) {
	out, dir, err := runInitArgs(t, "wizard_named_me\ny\n", "--mode", "consumer", "--language", "python", "--no-input")

	if err == nil {
		t.Fatal("init must fail rather than prompt for a name when --no-input is set")
	}
	if !strings.Contains(err.Error(), "non-interactive") || !strings.Contains(err.Error(), "blocks init <name>") {
		t.Errorf("error must name the argument that supplies the name, got: %v", err)
	}
	if strings.Contains(out, "Agent name") {
		t.Errorf("the name prompt was printed despite --no-input:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "wizard_named_me")); statErr == nil {
		t.Error("init scaffolded a project from an answer read off stdin despite --no-input")
	}
}

// The fully specified form has an answer for everything, so --no-input must let
// it through — without stopping at the final confirmation, which is the second
// prompt this command reads stdin for.
func TestNoInputInitScaffoldsWithoutConfirmationPrompt(t *testing.T) {
	out, dir, err := runInitArgs(t, "", "my_consumer", "--mode", "consumer", "--language", "python", "--no-input")

	if err != nil {
		t.Fatalf("fully specified init must proceed under --no-input, got: %v", err)
	}
	if strings.Contains(out, "Continue?") {
		t.Errorf("the scaffold confirmation was asked despite --no-input:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "my_consumer", "main.py")); statErr != nil {
		t.Errorf("expected the consumer scaffold to be written: %v", statErr)
	}
}

// The webapp wizard is a chain of prompts (login offer, project name, agent
// autocomplete). --no-input must turn it into the error naming the flag that
// supplies the missing answer rather than entering it.
func TestNoInputInitWebappReportsMissingAgentInsteadOfWizard(t *testing.T) {
	out, _, err := runInitArgs(t, "translator\nn\n", "my_page", "--mode", "webapp", "--no-input")

	if err == nil {
		t.Fatal("webapp init must fail rather than enter the wizard when --no-input is set")
	}
	if !strings.Contains(err.Error(), "--agent") {
		t.Errorf("error must name the flag that supplies the agents, got: %v", err)
	}
	if strings.Contains(out, "Web app name") {
		t.Errorf("the webapp wizard ran despite --no-input:\n%s", out)
	}
}

func TestEnsureOrOfferBlocksLoginUsesProfileKey(t *testing.T) {
	defer isolateProfiles(t)()
	// Point the legacy credentials path at an empty dir so ONLY the profile is present,
	// and neutralise everything ambient so the profile's key is the one tier that can
	// answer. Both matter now that the credential comes from the resolved context: an
	// inherited BLOCKS_API_KEY would outrank the profile, which is the correct
	// precedence and not what this case is about.
	isolateCredentials(t)
	t.Setenv(blocksAPIKeyEnv, "")
	t.Setenv(blocksBackendURLEnv, "")
	resetInitFlags()
	t.Cleanup(resetInitFlags)

	// Seed a profile with a usable key (the canonical home). It records its own
	// deployment so resolution never reaches remote config, which would otherwise make
	// this case depend on a fetch it is not about.
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL:      "https://blocks.acme.example",
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Eng", ApiKey: "bk_profile_key"}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	resolveCLIContext(t, rootCmd)

	access, err := resolveCardAccess()
	if err != nil {
		t.Fatalf("resolveCardAccess error: %v", err)
	}
	got, err := ensureOrOfferBlocksLogin(context.Background(), access)
	if err != nil {
		t.Fatalf("ensureOrOfferBlocksLogin error: %v", err)
	}
	if got != "bk_profile_key" {
		t.Errorf("ensureOrOfferBlocksLogin = %q, want bk_profile_key (must read the resolved credential, not re-prompt)", got)
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

// withAnonymousBackend points the credential path at an empty temp dir (so the last
// tier of the credential precedence finds nothing) and wires BLOCKS_BACKEND_URL.
// Models a fresh machine that has never run 'blocks login'.
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
	if !strings.Contains(err.Error(), "invalid --type") {
		t.Errorf("error = %q, want 'invalid --type' rejection", err.Error())
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
	// The scaffold resolves its origin and credential from the request context, which
	// the root command's PersistentPreRun normally settles; a direct call has to.
	t.Cleanup(isolateProfiles(t))
	resolveCLIContext(t, rootCmd)

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

// cardServer stands in for a deployment whose registry serves one agent's card only to
// a caller presenting requiredKey — the shape of a private agent. It records every
// Authorization header it received, so a test can assert which credential the CLI
// actually sent rather than which one it said it would.
type cardServer struct {
	url string
	// authorizations holds the Authorization header of every request it received.
	authorizations []string
}

func newCardServer(t *testing.T, agentName, requiredKey, fixture string) *cardServer {
	t.Helper()
	dir := fixtureDir(t)
	notFound, err := os.ReadFile(filepath.Join(dir, "not_found.json"))
	if err != nil {
		t.Fatalf("read not_found fixture: %v", err)
	}
	card, err := os.ReadFile(filepath.Join(dir, fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	s := &cardServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.authorizations = append(s.authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("agentName") != agentName || r.Header.Get("Authorization") != "Bearer "+requiredKey {
			// What a registry answers a caller with no right to see the agent: the same
			// not-found it answers for a name that does not exist.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(notFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(card)
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// received reports whether the server was handed key as a bearer credential.
func (s *cardServer) received(key string) bool {
	for _, sent := range s.authorizations {
		if sent == "Bearer "+key {
			return true
		}
	}
	return false
}

// receivedAnyCredential reports whether any request carried a credential at all.
func (s *cardServer) receivedAnyCredential() bool {
	for _, sent := range s.authorizations {
		if strings.TrimSpace(sent) != "" {
			return true
		}
	}
	return false
}

// seedDisplacedProfile makes a profile for some other deployment the active one, with a
// key of its own, and returns that key. It is the state every case below starts from: a
// developer who is logged in somewhere, scaffolding a page for somewhere else.
func seedDisplacedProfile(t *testing.T) string {
	t.Helper()
	const key = "bk_key_of_another_deployment"
	if err := profiles.Upsert("acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.example",
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Engineering", ApiKey: key}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return key
}

// seedLegacyCredential writes a key into the legacy credential file the CLI still reads
// as the last tier of the credential precedence. A case that means to prove a stored key
// stays home needs one there to withhold.
func seedLegacyCredential(t *testing.T, key string) {
	t.Helper()
	path, err := auth.CredentialPathFunc()
	if err != nil {
		t.Fatalf("resolve credential path: %v", err)
	}
	expiry := time.Now().Add(24 * time.Hour)
	if err := auth.SetProviderCredential(path, "blocks", &auth.ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     key,
		OrgId:      "org-local",
		ExpiresAt:  &expiry,
	}); err != nil {
		t.Fatalf("seed legacy credential: %v", err)
	}
}

// The credential a webapp scaffold sends is the one this invocation resolved, and it
// goes to the deployment the lookup actually queries. Here BLOCKS_API_KEY names a key
// for the deployment --backend-url points at while a profile for a different deployment
// is active: the environment outranks the profile in the one precedence, so the page's
// wiring is snapshotted from a card only that key can see.
//
// The assertion is at the server, on the Authorization header, because that is the only
// place the question is settled — printed output would say nothing about which key left
// the process.
func TestWebappInitCarriesTheResolvedCredentialToTheDeploymentItQueries(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)
	t.Chdir(t.TempDir())
	resetInitFlags()
	t.Cleanup(resetInitFlags)

	const envKey = "bk_env_key_for_the_page_backend"
	deployment := newCardServer(t, "echo2", envKey, "echo2.json")
	profileKey := seedDisplacedProfile(t)
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(blocksAPIKeyEnv, envKey)
	resolveCLIContext(t, rootCmd)

	initAgents = []string{"echo2"}
	initBackendURL = deployment.url
	var err error
	captureStdout(func() { err = runWebapp(context.Background(), "my_page") })
	if err != nil {
		t.Fatalf("the scaffold must resolve the private card with the key the invocation named, got: %v", err)
	}
	if !deployment.received(envKey) {
		t.Errorf("the deployment was never handed the resolved credential; it saw %q", deployment.authorizations)
	}
	if deployment.received(profileKey) {
		t.Errorf("another deployment's stored key was transmitted to this one: %q", deployment.authorizations)
	}
}

// The complement: with nothing but a stored key for a different deployment, the lookup
// is anonymous. A key found in local storage belongs to the deployment whose profile
// holds it, so it may not be sent to the one --backend-url named — and a private agent
// there is then simply not found, which is the outcome that names the remedy.
func TestWebappInitWithholdsAStoredKeyFromAnotherDeployment(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)
	dir := t.TempDir()
	t.Chdir(dir)
	resetInitFlags()
	t.Cleanup(resetInitFlags)

	deployment := newCardServer(t, "echo2", "bk_key_only_this_deployment_knows", "echo2.json")
	profileKey := seedDisplacedProfile(t)
	// Both local tiers hold a key, because both used to be read directly and each
	// answered for a deployment this lookup is not pointed at: the wizard path read the
	// profile, the flag-driven path read the legacy credential file.
	const legacyKey = "bk_legacy_store_key"
	seedLegacyCredential(t, legacyKey)
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(blocksAPIKeyEnv, "")
	resolveCLIContext(t, rootCmd)

	initAgents = []string{"echo2"}
	initBackendURL = deployment.url
	var err error
	captureStdout(func() { err = runWebapp(context.Background(), "my_page") })
	if err == nil {
		t.Fatal("a private card must not resolve for an invocation with no credential for that deployment")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "blocks login") {
		t.Errorf("the failure must name the remedy, got: %v", err)
	}
	if deployment.received(profileKey) {
		t.Errorf("another deployment's stored key was transmitted to this one: %q", deployment.authorizations)
	}
	if deployment.received(legacyKey) {
		t.Errorf("a key from the legacy credential file was transmitted to a deployment it does not describe: %q", deployment.authorizations)
	}
	if deployment.receivedAnyCredential() {
		t.Errorf("the lookup must be anonymous when no credential describes the deployment, got %q", deployment.authorizations)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "my_page")); statErr == nil {
		t.Error("no project directory may be left behind when the card cannot be fetched")
	}
}

// The next-steps block asks for a login when this project has no credential for the
// deployment it will reach, and stops asking as soon as it has one. A key cached in a
// profile for some other deployment is not one — reading it there reported "logged in"
// and dropped the login that deployment requires — and a key the environment supplies is,
// however loudly the profile disagrees.
func TestNextStepsAsksForTheLoginTheResolvedDeploymentNeeds(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)
	// A local deployment answers discovery, so the enterprise question this block asks
	// costs no round-trip outside the test.
	deployment := newDeploymentServer(t, false)
	seedDisplacedProfile(t)
	t.Setenv(blocksAPIKeyEnv, "")
	t.Setenv(blocksBackendURLEnv, deployment.url)
	resolveCLIContext(t, rootCmd)

	cfg := wizard.Config{Name: "test_agent", Language: "python", Mode: "provider"}
	out := captureStdout(func() { printNextSteps(cfg) })
	if !strings.Contains(out, "blocks login") {
		t.Errorf("a project with no credential for the deployment it targets must be told to log in:\n%s", out)
	}

	// The same project, with a key for that deployment in the environment: nothing left
	// to log in for.
	t.Setenv(blocksAPIKeyEnv, "bk_env_key_for_the_target")
	resolveCLIContext(t, rootCmd)
	out = captureStdout(func() { printNextSteps(cfg) })
	if strings.Contains(out, "blocks login") {
		t.Errorf("a credential the invocation already carries must not be answered with a login step:\n%s", out)
	}
}

// The wizard path resolves the same way. It used to prefer the active profile before it
// had even worked out which deployment it would query, so a profile for deployment A
// supplied the key for a lookup against deployment B; and a BLOCKS_API_KEY for B was
// ignored entirely, leaving a private agent unresolvable from the suggestions and the
// card fetch alike.
func TestTheWebappWizardCarriesTheResolvedCredentialToTheDeploymentItQueries(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)
	t.Chdir(t.TempDir())
	resetInitFlags()
	t.Cleanup(resetInitFlags)

	const envKey = "bk_env_key_for_the_page_backend"
	deployment := newCardServer(t, "echo2", envKey, "echo2.json")
	profileKey := seedDisplacedProfile(t)
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(blocksAPIKeyEnv, envKey)
	resolveCLIContext(t, rootCmd)
	// The wizard's answers: a project name, one agent, then a blank line to finish. A
	// pipe is not a terminal, so the agent list is collected line by line rather than
	// through the raw-mode autocomplete.
	pipeStdin(t, "my_page\necho2\n\n")

	initBackendURL = deployment.url
	var err error
	captureStdout(func() { err = runWebappWizard(context.Background()) })
	if err != nil {
		t.Fatalf("the wizard must resolve the private card with the key the invocation named, got: %v", err)
	}
	if !deployment.received(envKey) {
		t.Errorf("the deployment was never handed the resolved credential; it saw %q", deployment.authorizations)
	}
	if deployment.received(profileKey) {
		t.Errorf("another deployment's stored key was transmitted to this one: %q", deployment.authorizations)
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
	// Calling runWebapp directly skips the root command's PersistentPreRun, so the
	// request context this scaffold resolves its origin and credential from has to be
	// resolved here — after the profile is seeded.
	resolveCLIContext(t, rootCmd)

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

func TestInitAcceptsModeAliases(t *testing.T) {
	// The alias must be accepted on any profile, not just enterprise, so CI
	// validation never depends on which profile happens to be active.
	for _, alias := range []string{"connect-agent", "call-agent"} {
		if _, ok := wizard.NormalizeMode(alias); !ok {
			t.Errorf("alias %q should be accepted", alias)
		}
	}
}

func TestPrintNextSteps_LoginLine_AlreadyLoggedIn(t *testing.T) {
	defer isolateProfiles(t)()
	// Seed a profile with a usable org key (logged in state)
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Eng", ApiKey: "bk_test_key"}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	// Reset and resolve clictx
	clictx.Reset()
	clictx.Resolve(nil)

	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	out := captureStdout(func() {
		printNextSteps(cfg)
	})

	// Should NOT contain any blocks login line
	if strings.Contains(out, "blocks login") {
		t.Errorf("output should not contain 'blocks login' when already logged in: %s", out)
	}
	// Should NOT contain Enterprise hint
	if strings.Contains(out, "<your-instance>") {
		t.Errorf("output should not contain '<your-instance>' when already logged in: %s", out)
	}
	// Should still contain the other lines
	if !strings.Contains(out, "blocks register") {
		t.Errorf("output should still contain 'blocks register': %s", out)
	}
}

func TestPrintNextSteps_LoginLine_NotLoggedInEnterprise(t *testing.T) {
	defer isolateProfiles(t)()
	// Seed enterprise profile with base_url but empty orgs (post-logout state)
	if err := profiles.Upsert("acme.blocks.ai", profiles.Profile{
		BaseURL:    "https://acme.blocks.ai",
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{}, // empty = not logged in
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}

	// Reset and resolve clictx
	clictx.Reset()
	clictx.Resolve(nil)

	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	out := captureStdout(func() {
		printNextSteps(cfg)
	})

	// Should contain the instance-qualified login command, with a placeholder rather
	// than the profile name it knows. The profile name reaches the store from a host
	// slug — a value a project .env or a deployment's discovery payload can choose —
	// and this line is formatted as something to copy into a shell, where `;` and
	// $(...) mean what they mean. See printNextSteps and
	// TestTheNextStepsBlockOffersNoPastableCommandBuiltFromTheProfile.
	if !strings.Contains(out, "blocks login <your-instance> --write-env") {
		t.Errorf("output should contain 'blocks login <your-instance> --write-env': %s", out)
	}
	if strings.Contains(out, "acme.blocks.ai") {
		t.Errorf("output must not interpolate the profile name into a pasteable command: %s", out)
	}
	// Should NOT contain the Blocks Network wording, which is the other branch
	if strings.Contains(out, "authenticate with Blocks Network") {
		t.Errorf("output should not use the Blocks Network wording on enterprise: %s", out)
	}
}

func TestPrintNextSteps_LoginLine_NotLoggedInNetwork(t *testing.T) {
	defer isolateProfiles(t)()
	// Use isolated profile store with default network state
	// Reset and resolve clictx (no enterprise profile = network mode)
	clictx.Reset()
	clictx.Resolve(nil)

	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	out := captureStdout(func() {
		printNextSteps(cfg)
	})

	// Should contain the new Blocks Network login command
	if !strings.Contains(out, "blocks login --write-env  # authenticate with Blocks Network (first time only)") {
		t.Errorf("output should contain 'blocks login --write-env  # authenticate with Blocks Network' on network: %s", out)
	}
	// Should contain the Enterprise hint with placeholder
	if !strings.Contains(out, "for Blocks Enterprise: blocks login <your-instance> --write-env") {
		t.Errorf("output should contain Enterprise hint with <your-instance> placeholder on network: %s", out)
	}
	// Should contain free pricing
	if !strings.Contains(out, "privately and free") {
		t.Errorf("output should contain 'privately and free' on network: %s", out)
	}
}

// TestPrintNextSteps_ShowVerbatimOutputs prints the verbatim next-steps blocks
// for documentation purposes. Run with: go test -v -run TestPrintNextSteps_ShowVerbatimOutputs
func TestPrintNextSteps_ShowVerbatimOutputs(t *testing.T) {
	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	// Case 1: Already logged in
	t.Log("=== CASE 1: Already logged in ===")
	defer isolateProfiles(t)()
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "o1",
		Orgs:         map[string]profiles.OrgKey{"o1": {OrgName: "Eng", ApiKey: "bk_test_key"}},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	clictx.Reset()
	clictx.Resolve(nil)
	out1 := captureStdout(func() { printNextSteps(cfg) })
	t.Logf("Output:\n%s", out1)

	// Case 2: Not logged in + enterprise
	t.Log("=== CASE 2: Not logged in + enterprise ===")
	defer isolateProfiles(t)()
	if err := profiles.Upsert("acme.blocks.ai", profiles.Profile{
		BaseURL:    "https://acme.blocks.ai",
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	clictx.Reset()
	clictx.Resolve(nil)
	out2 := captureStdout(func() { printNextSteps(cfg) })
	t.Logf("Output:\n%s", out2)

	// Case 3: Not logged in + network
	t.Log("=== CASE 3: Not logged in + network ===")
	defer isolateProfiles(t)()
	clictx.Reset()
	clictx.Resolve(nil)
	out3 := captureStdout(func() { printNextSteps(cfg) })
	t.Logf("Output:\n%s", out3)
}

func TestPrintNextSteps_EnterpriseSuppressesFreePricing(t *testing.T) {
	// Test enterprise mode: should not contain "free" but should contain "privately"
	seedEnterpriseProfileForTest(t)

	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	out := captureStdout(func() {
		printNextSteps(cfg)
	})

	if !strings.Contains(out, "blocks register") {
		t.Errorf("enterprise output should contain 'blocks register': %s", out)
	}
	if strings.Contains(out, "free") {
		t.Errorf("enterprise output should not contain 'free': %s", out)
	}
	if !strings.Contains(out, "privately") {
		t.Errorf("enterprise output should contain 'privately': %s", out)
	}
}

func TestPrintNextSteps_NetworkIncludesFreePricing(t *testing.T) {
	// Use isolated profile store to ensure clean state
	restore := isolateProfiles(t)
	defer restore()

	// Reset to network mode explicitly
	clictx.Reset()
	clictx.Resolve(nil)

	cfg := wizard.Config{
		Name:     "test_agent",
		Language: "python",
		Mode:     "provider",
	}

	out := captureStdout(func() {
		printNextSteps(cfg)
	})

	if !strings.Contains(out, "blocks register") {
		t.Errorf("network output should contain 'blocks register': %s", out)
	}
	if !strings.Contains(out, "privately and free") {
		t.Errorf("network output should contain 'privately and free': %s", out)
	}
}

func TestInitDeploymentHeader(t *testing.T) {
	tests := []struct {
		name                 string
		setupProfile         func(t *testing.T)
		expectHeader         string
		expectEnterpriseHint bool
	}{
		{
			name: "logged in to enterprise profile",
			setupProfile: func(t *testing.T) {
				seedEnterpriseProfileForTest(t)
				// Seed with a logged-in state (has orgs with keys)
				if err := profiles.Upsert("umbrella.blocks.ai", profiles.Profile{
					BaseURL:      "https://umbrella.blocks.ai",
					Enterprise:   true,
					DefaultOrgID: "org1",
					Orgs:         map[string]profiles.OrgKey{"org1": {OrgName: "Engineering", ApiKey: "bk_test"}},
				}, true); err != nil {
					t.Fatalf("seed logged-in enterprise profile: %v", err)
				}
				branding.Set("Umbrella Corporation")
				t.Cleanup(func() { branding.Reset() })
			},
			expectHeader:         "Creating a project for Umbrella Corporation.",
			expectEnterpriseHint: false,
		},
		{
			name: "logged in to Network",
			setupProfile: func(t *testing.T) {
				restore := isolateProfiles(t)
				t.Cleanup(restore)
				// Seed a Network profile with logged-in state
				if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
					DefaultOrgID: "org1",
					Orgs:         map[string]profiles.OrgKey{"org1": {OrgName: "MyOrg", ApiKey: "bk_network_key"}},
				}, true); err != nil {
					t.Fatalf("seed logged-in network profile: %v", err)
				}
				clictx.Reset()
				clictx.Resolve(nil)
			},
			expectHeader:         "Creating a project for Blocks Network.",
			expectEnterpriseHint: false,
		},
		{
			name: "not logged in, stock Network profile",
			setupProfile: func(t *testing.T) {
				restore := isolateProfiles(t)
				t.Cleanup(restore)
				// "Not logged in" has to mean no credential from ANY tier, not just an
				// empty profile store: an ambient BLOCKS_API_KEY counts as one, and
				// `go test` runs with cwd set to the package directory, so root.go's
				// init() loads any .env sitting beside these sources. A stray one there
				// — the kind a test that called maybeWriteEnv without chdir leaves
				// behind — otherwise makes this case assert the opposite of its name.
				t.Setenv(blocksAPIKeyEnv, "")
				// Default state: no profiles set up, should be Network mode with no login
				clictx.Reset()
				clictx.Resolve(nil)
			},
			expectHeader:         "Creating a project for Blocks Network.",
			expectEnterpriseHint: true,
		},
		{
			name: "not logged in but enterprise profile retained",
			setupProfile: func(t *testing.T) {
				restore := isolateProfiles(t)
				t.Cleanup(restore)
				// Enterprise profile exists but has empty orgs (post-logout state)
				if err := profiles.Upsert("acme.blocks.ai", profiles.Profile{
					BaseURL:    "https://acme.blocks.ai",
					Enterprise: true,
					Orgs:       map[string]profiles.OrgKey{}, // empty = not logged in
				}, true); err != nil {
					t.Fatalf("seed post-logout enterprise profile: %v", err)
				}
				branding.Set("PubNub")
				clictx.Reset()
				clictx.Resolve(nil)
			},
			expectHeader:         "Creating a project for PubNub.",
			expectEnterpriseHint: false, // This is the key case: enterprise profile but no hint
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset branding to default at start of each test
			branding.Reset()
			t.Cleanup(func() { branding.Reset() })

			// Reset clictx to default at start of each test
			clictx.Reset()

			// Simulate a terminal. Both answers are forced: the header is shown
			// only to a session that may prompt, which is a real terminal the
			// caller has not opted out of with --no-input or --yes.
			forceTTY(t)
			resetNoInput(t)

			// Reset init flags
			resetInitFlags()
			t.Cleanup(resetInitFlags)

			// Set up the profile state
			tt.setupProfile(t)

			// Create temp dir for the test
			dir := t.TempDir()
			oldDir, _ := os.Getwd()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(oldDir)

			// Capture output by running just enough to trigger the header
			// We'll simulate a cancel after the header prints
			var output string
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Expected: wizard will fail when we don't provide input, that's ok
					}
				}()

				// Override stdin to provide EOF immediately (simulates canceling the wizard)
				oldStdin := os.Stdin
				r, w, _ := os.Pipe()
				os.Stdin = r
				w.Close() // EOF
				defer func() { os.Stdin = oldStdin }()

				output = captureStdout(func() {
					rootCmd.SetArgs([]string{"init"})
					_ = rootCmd.Execute() // Will fail due to EOF, that's expected
				})
			}()

			// Check the header is present
			if !strings.Contains(output, tt.expectHeader) {
				t.Errorf("Expected header %q in output:\n%s", tt.expectHeader, output)
			}

			// Check enterprise hint presence/absence
			enterpriseHintPresent := strings.Contains(output, "For a Blocks Enterprise deployment, run `blocks login <your-instance>` first.")
			if tt.expectEnterpriseHint && !enterpriseHintPresent {
				t.Errorf("Expected enterprise hint to be present in output:\n%s", output)
			}
			if !tt.expectEnterpriseHint && enterpriseHintPresent {
				t.Errorf("Expected enterprise hint to be absent in output:\n%s", output)
			}
		})
	}
}

// TestInitHeaderVerbatimOutput shows the exact header output for documentation.
func TestInitHeaderVerbatimOutput(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(t *testing.T)
		scenario string
	}{
		{
			name: "network_not_logged_in",
			setup: func(t *testing.T) {
				restore := isolateProfiles(t)
				t.Cleanup(restore)
				branding.Reset()
				clictx.Reset()
				clictx.Resolve(nil)
			},
			scenario: "not logged in Network",
		},
		{
			name: "enterprise_not_logged_in_post_logout",
			setup: func(t *testing.T) {
				restore := isolateProfiles(t)
				t.Cleanup(restore)
				if err := profiles.Upsert("acme.blocks.ai", profiles.Profile{
					BaseURL:    "https://acme.blocks.ai",
					Enterprise: true,
					Orgs:       map[string]profiles.OrgKey{},
				}, true); err != nil {
					t.Fatalf("seed post-logout enterprise profile: %v", err)
				}
				branding.Reset()
				branding.Set("PubNub")
				clictx.Reset()
				clictx.Resolve(nil)
			},
			scenario: "not logged in enterprise (post-logout)",
		},
		{
			name: "enterprise_logged_in",
			setup: func(t *testing.T) {
				seedEnterpriseProfileForTest(t)
				branding.Set("Umbrella Corporation")
			},
			scenario: "logged in enterprise",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate a terminal the caller has not opted out of, which is the
			// only session the header is printed for.
			forceTTY(t)
			resetNoInput(t)

			resetInitFlags()
			t.Cleanup(resetInitFlags)

			tc.setup(t)

			dir := t.TempDir()
			oldDir, _ := os.Getwd()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(oldDir)

			// Capture header output by simulating EOF immediately
			var output string
			func() {
				defer func() {
					if r := recover(); r != nil {
						// Expected: wizard fails on EOF, that's fine
					}
				}()

				oldStdin := os.Stdin
				r, w, _ := os.Pipe()
				os.Stdin = r
				w.Close() // EOF
				defer func() { os.Stdin = oldStdin }()

				output = captureStdout(func() {
					rootCmd.SetArgs([]string{"init"})
					_ = rootCmd.Execute() // Will fail due to EOF, expected
				})
			}()

			t.Logf("=== VERBATIM OUTPUT for %s ===", tc.scenario)
			t.Logf("%s", output)
			t.Logf("=== END OUTPUT ===")
		})
	}
}

func TestInitModeAliasParity(t *testing.T) {
	// Parity test: scaffolding with mode aliases must produce byte-identical
	// output to scaffolding with canonical modes. This ensures aliases truly
	// normalize throughout the entire pipeline, not just in string comparisons.

	testCases := []struct {
		name      string
		canonical string
		alias     string
		language  string
	}{
		{"provider vs connect-agent python", "provider", "connect-agent", "python"},
		{"consumer vs call-agent python", "consumer", "call-agent", "python"},
		{"provider vs connect-agent node", "provider", "connect-agent", "node"},
		{"consumer vs call-agent node", "consumer", "call-agent", "node"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create two separate temp directories
			canonicalDir := t.TempDir()
			aliasDir := t.TempDir()

			// Scaffold with canonical mode
			resetInitFlags()
			t.Chdir(canonicalDir)
			rootCmd.SetArgs([]string{"init", "test_agent", "--yes", "--language", tc.language, "--mode", tc.canonical})
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("canonical mode %q failed: %v", tc.canonical, err)
			}

			// Scaffold with alias mode
			resetInitFlags()
			t.Chdir(aliasDir)
			rootCmd.SetArgs([]string{"init", "test_agent", "--yes", "--language", tc.language, "--mode", tc.alias})
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("alias mode %q failed: %v", tc.alias, err)
			}

			resetInitFlags()

			// Compare the scaffolded project trees byte-for-byte
			canonicalRoot := filepath.Join(canonicalDir, "test_agent")
			aliasRoot := filepath.Join(aliasDir, "test_agent")

			canonicalFiles := make(map[string][]byte)
			aliasFiles := make(map[string][]byte)

			// Walk canonical directory
			err := filepath.WalkDir(canonicalRoot, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				relPath, err := filepath.Rel(canonicalRoot, path)
				if err != nil {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				canonicalFiles[relPath] = content
				return nil
			})
			if err != nil {
				t.Fatalf("failed to walk canonical directory: %v", err)
			}

			// Walk alias directory
			err = filepath.WalkDir(aliasRoot, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				relPath, err := filepath.Rel(aliasRoot, path)
				if err != nil {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				aliasFiles[relPath] = content
				return nil
			})
			if err != nil {
				t.Fatalf("failed to walk alias directory: %v", err)
			}

			// Compare file sets
			if len(canonicalFiles) != len(aliasFiles) {
				t.Fatalf("different number of files: canonical=%d, alias=%d", len(canonicalFiles), len(aliasFiles))
			}

			// Compare each file byte-for-byte
			for relPath, canonicalContent := range canonicalFiles {
				aliasContent, exists := aliasFiles[relPath]
				if !exists {
					t.Errorf("file %q missing in alias scaffold", relPath)
					continue
				}

				if !bytes.Equal(canonicalContent, aliasContent) {
					t.Errorf("file %q differs between canonical and alias scaffolds", relPath)
				}
			}
		})
	}
}

// init's card lookup used to ignore two of the three ways the resolver reports that a
// credential it should have is unavailable, because it gated on Source.External().
// displacedCredentialError carries no Source, so "you are logged in, just not to this
// deployment" read as "not logged in" and init scaffolded anonymously — which for a
// private agent surfaces as simply not found, the least actionable outcome available.
func TestInitReportsAWithheldOrUnreadableCredential(t *testing.T) {
	t.Run("credential withheld because the profile does not describe the target", func(t *testing.T) {
		restoreCLIState(t)
		defer isolateProfiles(t)()
		isolateAmbientState(t)

		// A profile holding a key for one deployment, and an override naming another.
		seedProfile(t, "acme", profiles.Profile{
			BaseURL:      "https://acme.example",
			DefaultOrgID: "org_1",
			Orgs:         map[string]profiles.OrgKey{"org_1": {OrgName: "Acme", ApiKey: "bk_acme"}},
		})
		clictx.Reset()
		t.Cleanup(clictx.Reset)
		clictx.Resolve(&clictx.Overrides{BackendURL: "https://elsewhere.example"})

		c := clictx.EffectiveCredential()
		if c.Err == nil {
			t.Fatal("premise: the resolver must withhold the stored key and say why")
		}
		if c.Source.External() {
			t.Fatal("premise: this error carries no Source, which is what init used to gate on")
		}
	})

	t.Run("unreadable credential store", func(t *testing.T) {
		restoreCLIState(t)
		defer isolateProfiles(t)()
		isolateAmbientState(t)

		boom := errors.New("credentials.json: permission denied")
		clictx.Reset()
		t.Cleanup(clictx.Reset)
		clictx.Resolve(&clictx.Overrides{
			StoredCredential: func() (string, bool, error) { return "", false, boom },
		})

		c := clictx.EffectiveCredential()
		if c.StoreErr == nil {
			t.Fatal("premise: the resolver must report the unreadable store")
		}
	})
}

// `init --backend-url` was compared against the offline half of the target resolution.
// BackendURL() stops before the remote tier, so in a build where Blocks Network's backend
// is learned through CDM it answers "" — and a flag naming exactly that deployment was
// classified as foreign, the stored credential withheld, and the private-card lookup done
// anonymously. The user's own private agent then comes back as not found.
func TestInitTreatsAFlagMatchingTheRemotelyResolvedTargetAsItsOwn(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const deployment = "https://backend.acme.example"

	// A CDM endpoint that names the deployment, and no local tier that does: this is the
	// shape where BackendURL() is "" and only EffectiveBackendURL() can answer.
	cdmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"api":{"baseUrl":%q},"pubnub":{"publishKey":"pub","subscribeKey":"sub"}}`, deployment)
	}))
	defer cdmSrv.Close()
	t.Setenv("BLOCKS_CDM_URL", cdmSrv.URL)
	cdm.Reset()

	clictx.Reset()
	t.Cleanup(clictx.Reset)
	clictx.Resolve(&clictx.Overrides{})

	if offline := clictx.BackendURL(); offline != "" {
		t.Fatalf("premise: no local tier may name the target, got %q", offline)
	}
	if effective, err := clictx.EffectiveBackendURL(); err != nil || effective != deployment {
		t.Fatalf("premise: the remote tier must name it, got %q/%v", effective, err)
	}

	orig := initBackendURL
	initBackendURL = deployment
	t.Cleanup(func() { initBackendURL = orig })

	access, err := resolveCardAccess()
	if err != nil {
		t.Fatalf("resolveCardAccess: %v", err)
	}
	if !access.resolvedTarget {
		t.Error("a --backend-url naming the effective target is not foreign; the stored credential must not be withheld")
	}
}
