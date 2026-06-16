package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ----- helpers -----

func writeConfig(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "blocks.config.json")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	return path
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "blocks_config_test_*")
	if err != nil {
		t.Fatalf("tempDir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// ----- Load tests -----

func TestLoad_ValidConfig(t *testing.T) {
	dir := tempDir(t)
	path := writeConfig(t, dir, `{
		"templateVersion": "1.0.0",
		"agents": ["translator"],
		"deployTarget": "cloudflare",
		"lastDeployedUrl": "https://my-site.pages.dev"
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TemplateVersion != "1.0.0" {
		t.Errorf("TemplateVersion: want 1.0.0, got %q", cfg.TemplateVersion)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "translator" {
		t.Errorf("Agents: want [translator], got %v", cfg.Agents)
	}
	if cfg.DeployTarget != "cloudflare" {
		t.Errorf("DeployTarget: want cloudflare, got %q", cfg.DeployTarget)
	}
	if cfg.LastDeployedUrl != "https://my-site.pages.dev" {
		t.Errorf("LastDeployedUrl: unexpected %q", cfg.LastDeployedUrl)
	}
}

func TestLoad_MinimalConfig(t *testing.T) {
	dir := tempDir(t)
	path := writeConfig(t, dir, `{"templateVersion":"0.1.0","agents":["chat"]}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DeployTarget != "" {
		t.Errorf("expected empty DeployTarget, got %q", cfg.DeployTarget)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/blocks.config.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := tempDir(t)
	path := writeConfig(t, dir, `{not valid json`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ----- Save tests -----

func TestSave_RoundTrip(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "blocks.config.json")

	orig := &BlocksConfig{
		TemplateVersion: "1.2.3",
		Agents:          []string{"agentA", "agentB"},
		DeployTarget:    "vercel",
		LastDeployedUrl: "https://vercel.app",
	}

	if err := Save(path, orig); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}

	if loaded.TemplateVersion != orig.TemplateVersion {
		t.Errorf("TemplateVersion mismatch: %q vs %q", loaded.TemplateVersion, orig.TemplateVersion)
	}
	if len(loaded.Agents) != len(orig.Agents) {
		t.Fatalf("Agents len mismatch: %d vs %d", len(loaded.Agents), len(orig.Agents))
	}
	for i := range orig.Agents {
		if loaded.Agents[i] != orig.Agents[i] {
			t.Errorf("Agents[%d]: %q vs %q", i, loaded.Agents[i], orig.Agents[i])
		}
	}
	if loaded.DeployTarget != orig.DeployTarget {
		t.Errorf("DeployTarget mismatch")
	}
	if loaded.LastDeployedUrl != orig.LastDeployedUrl {
		t.Errorf("LastDeployedUrl mismatch")
	}
}

func TestSave_EmptyDeployTarget_OmittedFromJSON(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "blocks.config.json")

	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"myagent"},
		// DeployTarget intentionally empty
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// The JSON must NOT contain "deployTarget" when unset.
	if strings.Contains(string(raw), "deployTarget") {
		t.Errorf("serialised JSON must not contain 'deployTarget' when unset; got:\n%s", raw)
	}

	// Also assert the key is truly absent at the parsed level.
	var generic map[string]interface{}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := generic["deployTarget"]; ok {
		t.Error("parsed JSON map must not contain 'deployTarget' key when unset")
	}
}

func TestSave_EmptyLastDeployedUrl_OmittedFromJSON(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "blocks.config.json")

	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"myagent"},
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "lastDeployedUrl") {
		t.Errorf("serialised JSON must not contain 'lastDeployedUrl' when unset; got:\n%s", raw)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	// Verify the file ends up at the requested path and is readable.
	dir := tempDir(t)
	path := filepath.Join(dir, "sub", "blocks.config.json")

	cfg := &BlocksConfig{
		TemplateVersion: "0.0.1",
		Agents:          []string{"bot"},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save into nested dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not found after Save: %v", err)
	}
}

// ----- Validate tests -----

// mustValidate asserts that Validate returns no error.
func mustValidate(t *testing.T, cfg *BlocksConfig) {
	t.Helper()
	if err := Validate(cfg); err != nil {
		t.Errorf("expected valid, got error: %v", err)
	}
}

// mustReject asserts that Validate returns an error containing fragment.
func mustReject(t *testing.T, cfg *BlocksConfig, fragment string) {
	t.Helper()
	err := Validate(cfg)
	if err == nil {
		t.Errorf("expected validation error containing %q, got nil", fragment)
		return
	}
	if fragment != "" && !strings.Contains(err.Error(), fragment) {
		t.Errorf("expected error to contain %q, got: %v", fragment, err)
	}
}

func TestValidate_ValidSingleAgent(t *testing.T) {
	mustValidate(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"translator"},
	})
}

func TestValidate_ValidMultiAgent(t *testing.T) {
	mustValidate(t, &BlocksConfig{
		TemplateVersion: "0.2.1",
		Agents:          []string{"alpha", "beta", "gamma"},
	})
}

func TestValidate_ValidWithDeployTarget(t *testing.T) {
	// Built-ins plus a representative on-disk-plugin-shaped name. The
	// config layer is shape-only — actual existence is checked at
	// `blocks deploy` time against the live adapter registry.
	for _, target := range []string{"cloudflare", "vercel", "netlify", "railway", "fly", "my-staging", "deploy_v2"} {
		t.Run(target, func(t *testing.T) {
			mustValidate(t, &BlocksConfig{
				TemplateVersion: "1.0.0",
				Agents:          []string{"myagent"},
				DeployTarget:    target,
			})
		})
	}
}

func TestValidate_ValidEmptyDeployTarget(t *testing.T) {
	mustValidate(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"myagent"},
		DeployTarget:    "",
	})
}

func TestValidate_RejectsEmptyAgents(t *testing.T) {
	mustReject(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{},
	}, "at least one entry")
}

func TestValidate_RejectsNilAgents(t *testing.T) {
	mustReject(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          nil,
	}, "at least one entry")
}

func TestValidate_RejectsAgentCountExceeds25(t *testing.T) {
	agents := make([]string, 26)
	for i := range agents {
		agents[i] = fmt.Sprintf("agent%d", i)
	}
	mustReject(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          agents,
	}, "at most 25")
}

func TestValidate_RejectsAgentWithSlash(t *testing.T) {
	mustReject(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"acme/translator"},
	}, "bare agent name")
}

func TestValidate_RejectsAgentWithInvalidChars(t *testing.T) {
	// Hyphens, spaces, dots, and special chars are not in ^[a-zA-Z0-9_]+$.
	for _, bad := range []string{"my-agent", "my agent", "my.agent", "agent!"} {
		t.Run(bad, func(t *testing.T) {
			mustReject(t, &BlocksConfig{
				TemplateVersion: "1.0.0",
				Agents:          []string{bad},
			}, "^[a-zA-Z0-9_]+$")
		})
	}
}

func TestValidate_AcceptsValidAgentNames(t *testing.T) {
	for _, good := range []string{"agent", "Agent123", "my_agent", "a", "ABC_123"} {
		t.Run(good, func(t *testing.T) {
			mustValidate(t, &BlocksConfig{
				TemplateVersion: "1.0.0",
				Agents:          []string{good},
			})
		})
	}
}

func TestValidate_RejectsBadDeployTarget(t *testing.T) {
	// Shape-only gate now: any well-formed slug passes. Reject only on
	// pattern violations (uppercase, leading hyphen, whitespace, slash, etc.).
	for _, bad := range []string{"AWS", "-railway", " railway", "rail/way", "rail.way"} {
		t.Run(bad, func(t *testing.T) {
			mustReject(t, &BlocksConfig{
				TemplateVersion: "1.0.0",
				Agents:          []string{"agent"},
				DeployTarget:    bad,
			}, "^[a-z0-9][a-z0-9_-]*$")
		})
	}
}

// Regression: a custom plugin target persists across save/load. Prior to
// impl_07 follow-up F2 the validator hardcoded {cloudflare,vercel,netlify}
// so a first `blocks deploy railway` saved `deployTarget: "railway"`, and
// the next deploy failed at config validation on reload.
func TestValidate_PluginDeployTargetPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocks.config.json")
	original := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"agent"},
		DeployTarget:    "railway",
	}
	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Validate(loaded); err != nil {
		t.Fatalf("Validate after reload: %v", err)
	}
	if loaded.DeployTarget != "railway" {
		t.Errorf("DeployTarget = %q, want %q", loaded.DeployTarget, "railway")
	}
}

func TestValidate_RejectsMissingTemplateVersion(t *testing.T) {
	mustReject(t, &BlocksConfig{
		TemplateVersion: "",
		Agents:          []string{"agent"},
	}, "templateVersion is required")
}

func TestValidate_RejectsNonSemverTemplateVersion(t *testing.T) {
	for _, bad := range []string{"1.0", "v1.0.0", "latest", "1"} {
		t.Run(bad, func(t *testing.T) {
			mustReject(t, &BlocksConfig{
				TemplateVersion: bad,
				Agents:          []string{"agent"},
			}, `\d+\.\d+\.\d+`)
		})
	}
}

func TestValidate_RejectsDuplicateAgents(t *testing.T) {
	mustReject(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"agent", "agent"},
	}, "unique")
}

func TestValidate_CollectsMultipleErrors(t *testing.T) {
	// Multiple violations should all appear in the single returned error.
	err := Validate(&BlocksConfig{
		TemplateVersion: "",
		Agents:          []string{},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "templateVersion") {
		t.Errorf("missing templateVersion error in: %v", msg)
	}
	if !strings.Contains(msg, "agents") {
		t.Errorf("missing agents error in: %v", msg)
	}
}

