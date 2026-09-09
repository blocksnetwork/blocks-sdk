package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

// ambientTargetingVars are the environment variables this package's cases assume are
// absent: everything that can name the deployment a command reaches, the credential it
// sends, or the profile it reads them from. It is isolateAmbientState's own list plus
// BLOCKS_PROFILE, which selects the profile before any of the others are consulted.
//
// BLOCKS_CDM_URL is the one that made a floor necessary. It is a tier of the target
// precedence now — it names the deployment a profile recording none resolves to — so a
// developer with one exported in their shell watched stock-profile cases silently
// retarget and fail for a reason that had nothing to do with their change.
var ambientTargetingVars = []string{
	cdm.URLEnv,
	blocksBackendURLEnv,
	blocksAPIKeyEnv,
	blocksProfileEnv,
	blocksCLIClientIDEnv,
	blocksAppBaseURLEnv,
	blocksDashboardURLEnv,
}

// ambientLeftAtStartup describes anything still in effect after TestMain cleared it.
// It is recorded once, before any case runs, so the case that asserts the floor held
// does not depend on what ran before it — which under -shuffle is not knowable.
var ambientLeftAtStartup []string

// TestMain clears the ambient state above for every case in this package. It is a
// floor, not a replacement for isolateAmbientState: a case that needs a value set —
// or set to something specific — still sets it with t.Setenv, which restores per case
// and so composes with this.
//
// Two sources are covered, and the second is why the withdrawal goes through the
// CLI's own helper rather than os.Unsetenv. `go test` runs with the working directory
// set to the package directory, and root.go's init() loads ./.env from there before
// any test runs: a stray, gitignored cmd/.env was quietly supplying BLOCKS_API_KEY to
// every local run of this package, and one case had to be hardened against it by hand.
// dropProjectEnvValue takes the value, the loader's record of which file supplied it,
// and — through that record — the environment a delegated agent runtime would be
// handed, so no case can inherit any of the three.
func TestMain(m *testing.M) {
	for _, key := range ambientTargetingVars {
		dropProjectEnvValue(key)
		if v, ok := os.LookupEnv(key); ok {
			ambientLeftAtStartup = append(ambientLeftAtStartup, fmt.Sprintf("%s=%q is still set", key, v))
		}
		if src := envFileSource(key); src != "" {
			ambientLeftAtStartup = append(ambientLeftAtStartup, fmt.Sprintf("%s is still recorded as supplied by %s", key, src))
		}
	}
	os.Exit(m.Run())
}

// The floor itself: nothing a developer's shell — or a stray project .env in this
// directory — exported reaches a case, and a case that deliberately sets one of those
// variables still sees its own value.
func TestAmbientTargetingIsClearedBeforeTheSuiteRuns(t *testing.T) {
	for _, left := range ambientLeftAtStartup {
		t.Errorf("ambient state survived TestMain: %s", left)
	}

	t.Setenv(blocksAPIKeyEnv, "bk_set_by_this_case")
	if got := os.Getenv(blocksAPIKeyEnv); got != "bk_set_by_this_case" {
		t.Errorf("%s = %q; a case that sets one of these must still win", blocksAPIKeyEnv, got)
	}
}

// resolveCLIContext runs the same context resolution the root command performs in
// PersistentPreRun. Tests that call a helper directly — rather than driving the
// whole command — must call this after seeding the profile store and the
// environment, because the deployment origin, the credential and the enterprise
// verdict are all resolved once per invocation and read from there.
//
// cmd names the command whose --api-key / --api-key-stdin flags apply; pass
// rootCmd when the test exercises neither.
func resolveCLIContext(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	clictx.Reset()
	clictx.Resolve(effectiveOverrides(cmd))
	t.Cleanup(clictx.Reset)
}

// captureStdout redirects os.Stdout and returns whatever was printed.
//
// WARNING: This function mutates the process-wide os.Stdout. Tests using
// this helper must NOT use t.Parallel().
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(fmt.Sprintf("os.Pipe failed: %v", err))
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// writeValidProject creates a temp directory with a valid agent-card.json and handler.
func writeValidProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	card := map[string]interface{}{
		"identity": map[string]interface{}{
			"agentName":   "test_agent",
			"displayName": "test_agent",
			"description": "A test agent",
			"version":     "1.0.0",
			"provider":    map[string]interface{}{"organization": "TestOrg"},
		},
		"capabilities": map[string]interface{}{
			"taskKinds": []interface{}{"request"},
		},
		"tags": []interface{}{
			map[string]interface{}{
				"id":   "main",
				"name": "Main",
			},
		},
		"runtime": map[string]interface{}{
			"handler": "./handler.py",
		},
	}

	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-card.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVersionCommand(t *testing.T) {
	old := Version
	Version = "1.2.3-test"
	defer func() { Version = old }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"version"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(output, "1.2.3-test") {
		t.Errorf("version output = %q, want to contain %q", output, "1.2.3-test")
	}
}

func TestCheckCommandMissingFile(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	rootCmd.SetArgs([]string{"check"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error for missing agent-card.json")
	}
}

func TestCheckCommandWithPath(t *testing.T) {
	dir := writeValidProject(t)

	cardPath := filepath.Join(dir, "agent-card.json")
	rootCmd.SetArgs([]string{"check", cardPath})

	// Suppress stdout from check output
	output := captureStdout(func() {
		if err := rootCmd.Execute(); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})
	_ = output
}

func TestInitCommandNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myagent", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	agentDir := filepath.Join(dir, "myagent")
	if _, err := os.Stat(agentDir); err != nil {
		t.Error("expected myagent directory to be created")
	}
	if _, err := os.Stat(filepath.Join(agentDir, "agent-card.json")); err != nil {
		t.Error("expected agent-card.json to be created")
	}
}

func TestInitCommandNonInteractiveNode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myagent", "--yes", "-l", "node"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	agentDir := filepath.Join(dir, "myagent")
	if _, err := os.Stat(filepath.Join(agentDir, "handler.ts")); err != nil {
		t.Error("expected handler.ts to be created")
	}
	if _, err := os.Stat(filepath.Join(agentDir, "package.json")); err != nil {
		t.Error("expected package.json to be created")
	}
}

func TestInitCommandMissingNameNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing name in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "agent name is required") {
		t.Errorf("expected 'agent name is required' error, got: %v", err)
	}
}

func TestInitCommandInvalidLanguage(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "myagent", "--yes", "-l", "ruby"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("expected 'unsupported language' error, got: %v", err)
	}
}

func TestInitCommandDirectoryAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	// Pre-create the directory
	if err := os.Mkdir(filepath.Join(dir, "myagent"), 0755); err != nil {
		t.Fatal(err)
	}

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "myagent", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for existing directory")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestInitCommandConsumerNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initMode = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myconsumer", "--mode", "consumer", "--language", "node", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	consumerDir := filepath.Join(dir, "myconsumer")
	if _, err := os.Stat(filepath.Join(consumerDir, "index.ts")); err != nil {
		t.Error("expected index.ts in consumer project")
	}
	if _, err := os.Stat(filepath.Join(consumerDir, "agent-card.json")); err == nil {
		t.Error("consumer project should not contain agent-card.json")
	}
}

func TestInitCommandConsumerPythonNonInteractiveDescription(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initMode = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myconsumer", "--mode", "consumer", "--language", "python", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, "myconsumer", "pyproject.toml"))
	if err != nil {
		t.Fatalf("read pyproject.toml: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `description = "myconsumer consumer"`) {
		t.Errorf("pyproject.toml should describe the project as a consumer, got:\n%s", content)
	}
	if strings.Contains(content, `description = "myconsumer agent"`) {
		t.Errorf("pyproject.toml leaked provider default description:\n%s", content)
	}
}

// The two line-ending cases assert what the loader parsed rather than what it exported,
// because the CLI's own process imports only the variables it consumes: an ordinary
// application variable is recorded for the delegated agent instead of being set here.
// See env_file_process_boundary_test.go for that boundary; these two are only about \r\n.
func assertParsedFromEnvFile(t *testing.T, want map[string]string) {
	t.Helper()
	for key, value := range want {
		canonical := canonicalEnvKey(key)
		if _, ok := projectEnvValues[canonical]; !ok {
			t.Errorf("%s was not parsed out of the file at all", key)
			continue
		}
		if got := projectEnvValueFor(canonical); got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

// restoreEnvFileRecord puts back both halves of the loader's record, cloned rather than
// aliased because the loader clears them in place.
func restoreEnvFileRecord(t *testing.T) {
	t.Helper()
	priorKeys, priorValues := maps.Clone(envFileKeys), maps.Clone(projectEnvValues)
	t.Cleanup(func() { envFileKeys, projectEnvValues = priorKeys, priorValues })
}

func TestLoadEnvFileCRLF(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FOO=bar\r\nBAZ=qux\r\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("FOO")
	os.Unsetenv("BAZ")
	defer os.Unsetenv("FOO")
	defer os.Unsetenv("BAZ")
	restoreEnvFileRecord(t)

	loadEnvFile(envPath)

	assertParsedFromEnvFile(t, map[string]string{"FOO": "bar", "BAZ": "qux"})
}

func TestLoadEnvFileLF(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("A=1\nB=2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("A")
	os.Unsetenv("B")
	defer os.Unsetenv("A")
	defer os.Unsetenv("B")
	restoreEnvFileRecord(t)

	loadEnvFile(envPath)

	assertParsedFromEnvFile(t, map[string]string{"A": "1", "B": "2"})
}

func TestLoadEnvFileSkipsExistingVars(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("EXISTING=new_value\r\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("EXISTING", "old_value")
	defer os.Unsetenv("EXISTING")

	loadEnvFile(envPath)

	if got := os.Getenv("EXISTING"); got != "old_value" {
		t.Errorf("EXISTING = %q, want %q (should not override)", got, "old_value")
	}
}

func TestInitCommandInvalidMode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initMode = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "x", "--mode", "bogus", "--yes"})
		err := rootCmd.Execute()
		if err == nil {
			t.Fatal("expected error for unknown --mode value")
		}
		if !strings.Contains(err.Error(), "invalid --mode") {
			t.Errorf("error = %q, want 'invalid --mode' wording", err.Error())
		}
	})

	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("directory should not be created on invalid --mode")
	}
}

// seedEnterpriseProfileForTest points the profile store at a temp
// contexts.json holding one enterprise profile and resolves clictx from it.
// It also takes a snapshot of process-wide state and restores it on cleanup
// so tests using it are isolated from each other.
//
// baseURL overrides the profile's recorded deployment. A test that points the
// command at a local server MUST pass that server's URL, or the profile records
// one deployment while the request goes to another — and the context banner then
// correctly refuses to describe the request in the profile's terms.
func seedEnterpriseProfileForTest(t *testing.T, baseURL ...string) {
	t.Helper()
	deployment := "https://umbrella.blocks.ai"
	if len(baseURL) > 0 && baseURL[0] != "" {
		deployment = baseURL[0]
	}
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3,
      "active": "umbrella.blocks.ai",
      "profiles": {
        "umbrella.blocks.ai": {
          "base_url": "`+deployment+`",
          "enterprise": true,
          "product_name": "Umbrella Corporation",
          "default_org_id": "org-1",
          "orgs": {"org-1": {"org_name": "Engineering", "api_key": "bk_test"}}
        }
      }
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}

	// Clear any ambient API key so the profile's own cached key is the one the
	// command sends. A key from the environment outranks the profile at request
	// time, so leaving a developer's local .env in play would silently change what
	// the command authenticates as. Tests that want an ambient key set it after
	// this call.
	t.Setenv(blocksAPIKeyEnv, "")

	// Snapshot global state that tests might mutate
	origContextsPathFunc := profiles.ContextsPathFunc
	origRootProfile := rootProfile
	// Note: stdinScanner, isInteractive, branding are already handled by specific tests

	profiles.ContextsPathFunc = func() (string, error) { return path, nil }

	t.Cleanup(func() {
		profiles.ContextsPathFunc = origContextsPathFunc
		rootProfile = origRootProfile
		profiles.SetActiveOverride("")
		clictx.Reset()
	})

	clictx.Resolve(nil)
}

// The writer and this loader are two halves of one rule, so they are tested as one: a
// value written by `blocks login --write-env` has to reach the next command as the value
// the deployment sent, whatever bytes it contained. The writer quotes anything that is
// not plainly inert, and this is the read that has to take those quotes off — a
// credential handed back with a quote still attached authenticates nowhere and explains
// nothing.
//
// The cases are the values a deployment could hand the writer that mean something to a
// shell or to a dotenv parser: the same list internal/auth proves inert under
// `source .env`.
func TestLoadEnvFileReadsBackTheValueTheWriterWrote(t *testing.T) {
	for _, value := range []string{
		"bk_live$(id)",
		"bk_live`id`",
		"bk_live; touch pwned",
		"bk_live # still part of the key",
		"bk_live with spaces",
	} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			if err := auth.ApplyEnvAt(dir, auth.EnvMutation{Key: blocksAPIKeyEnv, Value: value}); err != nil {
				t.Fatalf("ApplyEnvAt: %v", err)
			}
			t.Chdir(dir)
			loadProjectEnv(t, blocksAPIKeyEnv)

			if got := os.Getenv(blocksAPIKeyEnv); got != value {
				written, _ := os.ReadFile(filepath.Join(dir, ".env"))
				t.Errorf("%s = %q, want %q; the file was:\n%s", blocksAPIKeyEnv, got, value, string(written))
			}
		})
	}
}

// A quoted value in a .env nobody here wrote is imported as the value inside the quotes,
// because that is what the dotenv loaders in the scaffolded agents do with the same file.
// A hand-quoted key used to reach the agent as the key and reach this CLI with its quotes
// attached: one of the two authenticated and neither said why.
func TestLoadEnvFileStripsQuotesTheAgentLoadersStrip(t *testing.T) {
	dir := writeProjectEnv(t, t.TempDir(),
		blocksAPIKeyEnv+"='bk_single'\n"+blocksBackendURLEnv+"=\"https://blocks.acme.example\"\n")
	t.Chdir(dir)
	loadProjectEnv(t, blocksAPIKeyEnv, blocksBackendURLEnv)

	for _, tc := range []struct{ key, want string }{
		{blocksAPIKeyEnv, "bk_single"},
		{blocksBackendURLEnv, "https://blocks.acme.example"},
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}
