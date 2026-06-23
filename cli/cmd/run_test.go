package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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

func TestEnterpriseRuntimeEnvInjectsCdmAndBackend(t *testing.T) {
	p := &profiles.Profile{Enterprise: true, BaseURL: "https://blocks.acme.com/"}
	env := withEnterpriseRuntimeEnv([]string{"HOME=/home/u"}, p)

	if !hasEnv(env, "BLOCKS_CDM_URL=https://blocks.acme.com/api/v1/cdm") {
		t.Errorf("missing BLOCKS_CDM_URL; env = %v", env)
	}
	if !hasEnv(env, "BLOCKS_BACKEND_URL=https://blocks.acme.com") {
		t.Errorf("missing BLOCKS_BACKEND_URL; env = %v", env)
	}
}

func TestEnterpriseRuntimeEnvNoopForNetworkProfile(t *testing.T) {
	cases := []*profiles.Profile{
		nil,
		{Enterprise: false, BaseURL: "https://blocks.acme.com"}, // not enterprise
		{Enterprise: true, BaseURL: ""},                         // no base url
	}
	for i, p := range cases {
		env := withEnterpriseRuntimeEnv([]string{"HOME=/home/u"}, p)
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

func TestEnterpriseRuntimeEnvDoesNotOverrideExisting(t *testing.T) {
	p := &profiles.Profile{Enterprise: true, BaseURL: "https://blocks.acme.com"}
	env := withEnterpriseRuntimeEnv(
		[]string{"BLOCKS_CDM_URL=https://override/cdm", "BLOCKS_BACKEND_URL=https://override"},
		p,
	)
	if !hasEnv(env, "BLOCKS_CDM_URL=https://override/cdm") || !hasEnv(env, "BLOCKS_BACKEND_URL=https://override") {
		t.Errorf("explicit env should win; env = %v", env)
	}
	for _, e := range env {
		if e == "BLOCKS_CDM_URL=https://blocks.acme.com/api/v1/cdm" {
			t.Errorf("injected over an existing value; env = %v", env)
		}
	}
}
