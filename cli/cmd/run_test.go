package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
)

func TestDetectProjectType(t *testing.T) {
	tests := []struct {
		name     string
		handler  string
		files    []string
		expected string
	}{
		{"ts extension", "handler.ts", nil, "node"},
		{"js extension", "handler.js", nil, "node"},
		{"py extension", "handler.py", nil, "python"},
		{"uppercase TS", "HANDLER.TS", nil, "node"},
		{"uppercase PY", "HANDLER.PY", nil, "python"},
		{"fallback package.json", "", []string{"package.json"}, "node"},
		{"fallback pyproject.toml", "", []string{"pyproject.toml"}, "python"},
		{"extension priority over files", "handler.ts", []string{"pyproject.toml"}, "node"},
		{"unknown no files", "", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			got := detectProjectType(dir, tt.handler)
			if got != tt.expected {
				t.Errorf("detectProjectType(%q, %q) = %q, want %q", dir, tt.handler, got, tt.expected)
			}
		})
	}
}

func TestDetectProjectTypeRunPyNotDetected(t *testing.T) {
	// run.py alone should NOT trigger Python detection (blocks run replaces it)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.py"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectProjectType(dir, "")
	if got != "" {
		t.Errorf("detectProjectType with only run.py = %q, want empty string", got)
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()

	// Existing file
	existing := filepath.Join(dir, "exists.txt")
	if err := os.WriteFile(existing, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(existing) {
		t.Error("fileExists should return true for existing file")
	}

	// Non-existent file
	if fileExists(filepath.Join(dir, "nope.txt")) {
		t.Error("fileExists should return false for non-existent file")
	}

	// Directory (os.Stat succeeds for directories)
	if !fileExists(dir) {
		t.Error("fileExists should return true for directory")
	}
}

func TestFindVenvPythonInCurrentDir(t *testing.T) {
	dir := t.TempDir()
	createFakeVenv(t, dir)

	got := findVenvPython(dir)
	expected := expectedVenvPath(dir)
	if got != expected {
		t.Errorf("findVenvPython(%q) = %q, want %q", dir, got, expected)
	}
}

func TestFindVenvPythonInParentDir(t *testing.T) {
	// .venv is in the parent; cwd is a subdirectory
	parent := t.TempDir()
	createFakeVenv(t, parent)

	child := filepath.Join(parent, "examples", "python", "echo")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}

	got := findVenvPython(child)
	expected := expectedVenvPath(parent)
	if got != expected {
		t.Errorf("findVenvPython(%q) = %q, want %q", child, got, expected)
	}
}

func TestFindVenvPythonNotFound(t *testing.T) {
	dir := t.TempDir()
	got := findVenvPython(dir)
	if got != "" {
		t.Errorf("findVenvPython(%q) = %q, want empty string", dir, got)
	}
}

func TestVenvInterpreterPath(t *testing.T) {
	got := venvInterpreterPath("/some/project")
	if runtime.GOOS == "windows" {
		expected := filepath.Join("/some/project", ".venv", "Scripts", "python.exe")
		if got != expected {
			t.Errorf("venvInterpreterPath = %q, want %q", got, expected)
		}
	} else {
		expected := filepath.Join("/some/project", ".venv", "bin", "python")
		if got != expected {
			t.Errorf("venvInterpreterPath = %q, want %q", got, expected)
		}
	}
}

// createFakeVenv creates a fake .venv directory with a python interpreter
// placeholder at the expected platform-specific location.
func createFakeVenv(t *testing.T, dir string) {
	t.Helper()
	var interpPath string
	if runtime.GOOS == "windows" {
		interpPath = filepath.Join(dir, ".venv", "Scripts", "python.exe")
	} else {
		interpPath = filepath.Join(dir, ".venv", "bin", "python")
	}
	if err := os.MkdirAll(filepath.Dir(interpPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(interpPath, []byte("#!/usr/bin/env python3\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

// expectedVenvPath returns the expected interpreter path for a given dir.
func expectedVenvPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, ".venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, ".venv", "bin", "python")
}

func TestWithCLIVersionAppendsWhenMissing(t *testing.T) {
	old := Version
	Version = "0.9.0-test"
	defer func() { Version = old }()

	env := []string{"HOME=/home/user", "PATH=/usr/bin"}
	result := withCLIVersion(env)

	found := false
	for _, e := range result {
		if e == "BLOCKS_CLI_VERSION=0.9.0-test" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("withCLIVersion did not append BLOCKS_CLI_VERSION; env = %v", result)
	}
	// Original entries must still be present.
	if len(result) != 3 {
		t.Errorf("expected 3 env entries, got %d", len(result))
	}
}

func TestWithCLIVersionReplacesExisting(t *testing.T) {
	old := Version
	Version = "1.0.0"
	defer func() { Version = old }()

	env := []string{"HOME=/home/user", "BLOCKS_CLI_VERSION=old-version", "PATH=/usr/bin"}
	result := withCLIVersion(env)

	count := 0
	for _, e := range result {
		if len(e) >= len("BLOCKS_CLI_VERSION=") && e[:len("BLOCKS_CLI_VERSION=")] == "BLOCKS_CLI_VERSION=" {
			count++
			if e != "BLOCKS_CLI_VERSION=1.0.0" {
				t.Errorf("expected BLOCKS_CLI_VERSION=1.0.0, got %s", e)
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 BLOCKS_CLI_VERSION entry, got %d", count)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 env entries (no growth), got %d", len(result))
	}
}

func TestWithCLIVersionUsesCurrentVersion(t *testing.T) {
	old := Version
	Version = "dev"
	defer func() { Version = old }()

	result := withCLIVersion(nil)
	if len(result) != 1 || result[0] != "BLOCKS_CLI_VERSION=dev" {
		t.Errorf("unexpected result for nil env: %v", result)
	}
}

func TestCLIVersionEnvKeyConstant(t *testing.T) {
	if cliVersionEnvKey != "BLOCKS_CLI_VERSION" {
		t.Errorf("cliVersionEnvKey = %q, want %q", cliVersionEnvKey, "BLOCKS_CLI_VERSION")
	}
}

func TestProtocolVersionFormat(t *testing.T) {
	v := registry.ProtocolVersion
	if v != "2026-05-01" {
		t.Errorf("ProtocolVersion = %q, want %q", v, "2026-05-01")
	}
	// Verify YYYY-MM-DD format
	if len(v) != 10 {
		t.Errorf("ProtocolVersion length = %d, want 10", len(v))
	}
	if v[4] != '-' || v[7] != '-' {
		t.Errorf("ProtocolVersion not in YYYY-MM-DD format: %q", v)
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestTargetRuntimeEnvInjectsCdmAndBackend(t *testing.T) {
	const deployment = "https://blocks.acme.example.com"
	env := withTargetRuntimeEnv([]string{"HOME=/home/u"}, deployment+"/")

	if !hasEnv(env, cdm.URLEnv+"="+cdm.EndpointFor(deployment)) {
		t.Errorf("missing %s; env = %v", cdm.URLEnv, env)
	}
	if !hasEnv(env, blocksBackendURLEnv+"="+deployment) {
		t.Errorf("missing %s; env = %v", blocksBackendURLEnv, env)
	}
}

func TestTargetRuntimeEnvNoopForEmptyOrigin(t *testing.T) {
	for i, backendURL := range []string{"", "   "} {
		env := withTargetRuntimeEnv([]string{"HOME=/home/u"}, backendURL)
		if len(env) != 1 {
			t.Errorf("case %d: expected no injection, got %v", i, env)
		}
	}
}

func TestWithAPIKeyInjectsResolvedKey(t *testing.T) {
	env := withAPIKey([]string{"HOME=/home/u"}, "bk_resolved")
	if !hasEnv(env, "BLOCKS_API_KEY=bk_resolved") {
		t.Errorf("missing injected BLOCKS_API_KEY; env = %v", env)
	}
}

func TestWithAPIKeyNoopForEmptyKey(t *testing.T) {
	env := withAPIKey([]string{"HOME=/home/u"}, "")
	if len(env) != 1 {
		t.Errorf("expected no injection for empty key, got %v", env)
	}
}

func TestWithAPIKeyDoesNotOverrideExisting(t *testing.T) {
	env := withAPIKey([]string{"BLOCKS_API_KEY=bk_explicit"}, "bk_resolved")
	if !hasEnv(env, "BLOCKS_API_KEY=bk_explicit") {
		t.Errorf("explicit env should win; env = %v", env)
	}
	if hasEnv(env, "BLOCKS_API_KEY=bk_resolved") {
		t.Errorf("injected over an existing value; env = %v", env)
	}
}

func TestTargetRuntimeEnvDoesNotOverrideExisting(t *testing.T) {
	const deployment = "https://blocks.acme.example.com"
	env := withTargetRuntimeEnv(
		[]string{cdm.URLEnv + "=https://override.example.test/cdm", blocksBackendURLEnv + "=https://override.example.test"},
		deployment,
	)
	if !hasEnv(env, cdm.URLEnv+"=https://override.example.test/cdm") || !hasEnv(env, blocksBackendURLEnv+"=https://override.example.test") {
		t.Errorf("explicit env should win; env = %v", env)
	}
	for _, e := range env {
		if e == cdm.URLEnv+"="+cdm.EndpointFor(deployment) {
			t.Errorf("injected over an existing value; env = %v", env)
		}
	}
}

func TestSetEnvDefaultReplacesEmptyValue(t *testing.T) {
	// Test 4: setEnvDefault should replace empty value and return exactly one entry
	env := []string{"HOME=/home/user", "BLOCKS_API_KEY=", "PATH=/usr/bin"}
	result := setEnvDefault(env, "BLOCKS_API_KEY", "bk_resolved")

	count := 0
	hasResolved := false
	for _, e := range result {
		if len(e) >= len("BLOCKS_API_KEY=") && e[:len("BLOCKS_API_KEY=")] == "BLOCKS_API_KEY=" {
			count++
			if e == "BLOCKS_API_KEY=bk_resolved" {
				hasResolved = true
			}
		}
	}

	if count != 1 {
		t.Errorf("expected exactly 1 BLOCKS_API_KEY entry, got %d in %v", count, result)
	}
	if !hasResolved {
		t.Errorf("expected BLOCKS_API_KEY=bk_resolved, not found in %v", result)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 env entries (no growth), got %d", len(result))
	}
}

func TestSetEnvDefaultPreservesNonEmptyValue(t *testing.T) {
	// Test 5: setEnvDefault should not override explicit non-empty values
	env := []string{"BLOCKS_API_KEY=bk_explicit"}
	result := setEnvDefault(env, "BLOCKS_API_KEY", "bk_resolved")

	if !hasEnv(result, "BLOCKS_API_KEY=bk_explicit") {
		t.Errorf("explicit env should win; env = %v", result)
	}
	if hasEnv(result, "BLOCKS_API_KEY=bk_resolved") {
		t.Errorf("injected over an existing value; env = %v", result)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 env entry (no growth), got %d", len(result))
	}
}

func TestSetEnvDefaultAppendsWhenAbsent(t *testing.T) {
	// Test 6: setEnvDefault should append when key is absent
	env := []string{"HOME=/home/user", "PATH=/usr/bin"}
	result := setEnvDefault(env, "BLOCKS_API_KEY", "bk_resolved")

	if !hasEnv(result, "BLOCKS_API_KEY=bk_resolved") {
		t.Errorf("missing injected BLOCKS_API_KEY; env = %v", result)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 env entries, got %d", len(result))
	}
}

// --- buildChildEnv: the pointers must describe the effective target ----------
//
// These cases drive buildChildEnv itself rather than a stand-in, because the
// defect they guard is in the gate that decides *which* deployment's pointers to
// derive — a re-implementation of the injection would pass either way.

// childEnvValue returns the value of key in a child env slice, and whether it was
// present at all.
func childEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):], true
		}
	}
	return "", false
}

// enterpriseDiscoveryServer stands in for a deployment's unauthenticated
// cli-config endpoint, which is the only way the CLI can tell whether a backend
// it was merely pointed at is an enterprise instance.
func enterpriseDiscoveryServer(t *testing.T, enterprise bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli-config" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"enterprise":%t,"productName":"Acme AI Hub"}`, enterprise)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// brokenDiscoveryServer stands in for a deployment whose cli-config request
// cannot answer the enterprise question at all — a gateway in front of it, a
// login page where JSON was expected, or the backend itself failing. The
// deployment is still the one this run targets.
func brokenDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// closedPortURL is an origin nothing is listening on: a deployment that is simply
// unreachable from where the CLI runs.
func closedPortURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// assertTargetPointers checks that the child env carries the CDM endpoint and the
// backend origin of deployment, and nothing of otherDeployment — the two halves of
// "the runtime resolves its keysets from the deployment the CLI registers against".
// The expected endpoint is derived with cdm.EndpointFor so the test cannot encode a
// second spelling of the route.
func assertTargetPointers(t *testing.T, env []string, deployment, otherDeployment string) {
	t.Helper()
	// The child env is the whole process environment, so failures name the two
	// variables under test rather than dumping it.
	got, ok := childEnvValue(env, cdm.URLEnv)
	if !ok || got == "" {
		t.Fatalf("no %s injected — the agent would fall back to the public CDM", cdm.URLEnv)
	}
	if want := cdm.EndpointFor(deployment); got != want {
		t.Errorf("%s = %q, want %q", cdm.URLEnv, got, want)
	}
	if backend, _ := childEnvValue(env, blocksBackendURLEnv); backend != deployment {
		t.Errorf("%s = %q, want %q", blocksBackendURLEnv, backend, deployment)
	}
	if otherDeployment == "" {
		return
	}
	for _, e := range env {
		if strings.Contains(e, otherDeployment) {
			t.Errorf("a deployment this run will not touch leaked into the child env: %q", e)
		}
	}
}

// seedRunContexts points the profile store at a temp contexts.json and clears the
// ambient values buildChildEnv reads, so each case starts from a known target.
// Callers set BLOCKS_BACKEND_URL afterwards to displace the profile.
func seedRunContexts(t *testing.T, contexts string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(contexts), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() {
		profiles.ContextsPathFunc = orig
		profiles.SetActiveOverride("")
	})
	t.Setenv(blocksAPIKeyEnv, "")
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(cdm.URLEnv, "")
}

// enterpriseDeploymentURL is the deployment the enterprise profile below
// describes, and therefore the one that must NOT reach the child whenever an
// override displaces that profile.
const enterpriseDeploymentURL = "https://blocks.acme.example.com"

// enterpriseContexts is an active enterprise profile for a deployment other than
// whichever one a test overrides to.
const enterpriseContexts = `{
  "schema_version": 3,
  "active": "acme",
  "profiles": {
    "acme": {
      "base_url": "` + enterpriseDeploymentURL + `",
      "enterprise": true,
      "product_name": "Acme AI Hub",
      "default_org_id": "org_1",
      "orgs": {"org_1": {"org_id": "org_1", "org_name": "Acme", "api_key": "bk_acme"}}
    }
  }
}`

// stockContexts is an active profile that records no deployment at all: nothing
// points the invocation anywhere, which is the stock Blocks Network case.
const stockContexts = `{
  "schema_version": 3,
  "active": "blocks-network",
  "profiles": {"blocks-network": {"orgs": {}}}
}`

func TestBuildChildEnvUsesOverrideDeploymentNotProfile(t *testing.T) {
	// An enterprise profile is active, but BLOCKS_BACKEND_URL points at a
	// different enterprise deployment. The delegated agent must resolve the
	// override's keyset and register against the override's backend.
	override := enterpriseDiscoveryServer(t, true)
	seedRunContexts(t, enterpriseContexts)
	t.Setenv(blocksBackendURLEnv, override.URL)
	resolveCLIContext(t, rootCmd)

	assertTargetPointers(t, buildChildEnv(), override.URL, enterpriseDeploymentURL)
}

func TestBuildChildEnvUsesProfileWhenItIsTheTarget(t *testing.T) {
	seedRunContexts(t, enterpriseContexts)
	resolveCLIContext(t, rootCmd)

	assertTargetPointers(t, buildChildEnv(), enterpriseDeploymentURL, "")
}

func TestBuildChildEnvPointsAtANonEnterpriseTargetDeployment(t *testing.T) {
	// A deployment that reports itself as non-enterprise — a local or staging
	// backend — still serves the keysets the runtime has to resolve from it, so it
	// gets the same pointers an enterprise one does. Gating on the enterprise
	// verdict instead of on "the user picked this deployment" would send the agent
	// to the public CDM while the CLI registered it here, and would disagree with
	// the pin `blocks login --write-env` writes for the very same target.
	override := enterpriseDiscoveryServer(t, false)
	seedRunContexts(t, enterpriseContexts)
	t.Setenv(blocksBackendURLEnv, override.URL)
	resolveCLIContext(t, rootCmd)

	// The displaced profile's own deployment must not reach the child either: that
	// is the state the target-derived origin exists to prevent.
	assertTargetPointers(t, buildChildEnv(), override.URL, enterpriseDeploymentURL)
}

func TestBuildChildEnvPointsAtATargetWhoseDiscoveryFails(t *testing.T) {
	// Discovery can fail for reasons that say nothing about the deployment: a
	// gateway in front of it, an HTML login page, a backend erroring. The user still
	// named this deployment, so the pointers still describe it — an unanswerable
	// enterprise question must not silently redirect the runtime to Blocks Network.
	for _, tc := range []struct {
		name string
		url  func(t *testing.T) string
	}{
		{"discovery errors", func(t *testing.T) string { return brokenDiscoveryServer(t).URL }},
		{"deployment unreachable", closedPortURL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.url(t)
			seedRunContexts(t, enterpriseContexts)
			t.Setenv(blocksBackendURLEnv, target)
			resolveCLIContext(t, rootCmd)

			assertTargetPointers(t, buildChildEnv(), target, enterpriseDeploymentURL)
		})
	}
}

func TestBuildChildEnvInjectsNothingForStockNetwork(t *testing.T) {
	// Nothing points this invocation at a deployment, so the runtime's built-in
	// defaults are the correct answer and pinning anything would freeze one the
	// runtime is meant to resolve for itself.
	seedRunContexts(t, stockContexts)
	resolveCLIContext(t, rootCmd)

	env := buildChildEnv()

	for _, key := range []string{cdm.URLEnv, blocksBackendURLEnv} {
		if got, _ := childEnvValue(env, key); got != "" {
			t.Errorf("%s = %q, want nothing injected for a target the user did not pick", key, got)
		}
	}
}

func TestBuildChildEnvKeepsExplicitCdmURL(t *testing.T) {
	// An explicitly-set value outranks anything the CLI injects.
	seedRunContexts(t, enterpriseContexts)
	t.Setenv(cdm.URLEnv, "https://explicit.example.test/config.json")
	resolveCLIContext(t, rootCmd)

	env := buildChildEnv()

	if got, _ := childEnvValue(env, cdm.URLEnv); got != "https://explicit.example.test/config.json" {
		t.Errorf("%s = %q, want the explicitly-set value to win", cdm.URLEnv, got)
	}
}

func TestWithAPIKeyHandlesEmptyEnvVar(t *testing.T) {
	// Test 7: End-to-end component test - withAPIKey should handle empty env var
	env := []string{"BLOCKS_API_KEY="}
	result := withAPIKey(env, "bk_test_key")

	count := 0
	hasKey := false
	for _, e := range result {
		if len(e) >= len("BLOCKS_API_KEY=") && e[:len("BLOCKS_API_KEY=")] == "BLOCKS_API_KEY=" {
			count++
			if e == "BLOCKS_API_KEY=bk_test_key" {
				hasKey = true
			}
		}
	}

	if count != 1 {
		t.Errorf("expected exactly 1 BLOCKS_API_KEY entry, got %d in %v", count, result)
	}
	if !hasKey {
		t.Errorf("expected BLOCKS_API_KEY=bk_test_key, not found in %v", result)
	}
}
