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
		BackendBaseUrl:  "https://app.blocks.ai",
	})
}

func TestValidate_ValidMultiAgent(t *testing.T) {
	mustValidate(t, &BlocksConfig{
		TemplateVersion: "0.2.1",
		Agents:          []string{"alpha", "beta", "gamma"},
		BackendBaseUrl:  "https://app.blocks.ai",
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
				BackendBaseUrl:  "https://app.blocks.ai",
			})
		})
	}
}

func TestValidate_ValidEmptyDeployTarget(t *testing.T) {
	mustValidate(t, &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"myagent"},
		DeployTarget:    "",
		BackendBaseUrl:  "https://app.blocks.ai",
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
				BackendBaseUrl:  "https://app.blocks.ai",
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
		BackendBaseUrl:  "https://app.blocks.ai",
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

func TestValidate_ValidWithBackendBaseUrl(t *testing.T) {
	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"echo2"},
		BackendBaseUrl:  "https://blocks.acme.com",
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidate_RejectsBadBackendBaseUrl(t *testing.T) {
	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"echo2"},
		BackendBaseUrl:  "not a url",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for non-URL backendBaseUrl")
	}
}

func TestValidate_RejectsMissingBackendBaseUrl(t *testing.T) {
	// backendBaseUrl is REQUIRED. A config scaffolded before this change (no
	// field) must fail loudly rather than silently default the backend.
	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"echo2"},
		// BackendBaseUrl intentionally empty
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing backendBaseUrl")
	}
	if !strings.Contains(err.Error(), "backendBaseUrl") {
		t.Errorf("error should name backendBaseUrl; got: %v", err)
	}
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

func TestValidateBackendBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"https ok", "https://app.blocks.ai", false},
		{"https with port ok", "https://blocks.acme.com:8443", false},
		{"https with trailing slash ok", "https://blocks.ai/", false},
		{"http loopback localhost ok", "http://localhost:3001", false},
		{"http loopback 127.0.0.1 ok", "http://127.0.0.1:3001", false},
		{"http loopback ipv6 ok", "http://[::1]:3001", false},
		{"http loopback with multiple trailing slashes ok", "http://localhost:3001///", false},
		{"http loopback uppercase host ok", "http://LOCALHOST:3001", false},
		{"https with surrounding whitespace ok", "  https://blocks.ai  ", false},
		{"http non-loopback rejected", "http://staging.example.com", true},
		{"ftp rejected", "ftp://host.example.com", true},
		{"mailto rejected", "mailto:ops@example.com", true},
		{"garbage rejected", "not a url", true},
		{"empty rejected", "", true},
		{"scheme-relative rejected", "//example.com/path", true},
		{"https hostless port-only authority rejected", "https://:8080", true},
		{"https empty authority rejected", "https://", true},
		{"http loopback with userinfo ok", "http://user:pass@localhost:3001", false},
		{"scheme-relative-ish opaque URL rejected (CLI stricter than widget)", "https:example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBackendBaseURL(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateBackendBaseURL(%q) = nil, want error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateBackendBaseURL(%q) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestValidate_RejectsNonHTTPSBackendBaseUrl(t *testing.T) {
	// A parseable-but-cleartext non-loopback backend must be rejected: the
	// widget would throw on it at browser sign-in, so init/dev/deploy must not
	// let it be baked in. Webapps have no backward-compat obligation here.
	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"echo2"},
		BackendBaseUrl:  "http://staging.example.com",
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for http:// non-loopback backendBaseUrl")
	}
	if !strings.Contains(err.Error(), "backendBaseUrl") {
		t.Errorf("error should name backendBaseUrl; got: %v", err)
	}
}

func TestValidate_AcceptsLoopbackHTTPBackendBaseUrl(t *testing.T) {
	cfg := &BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          []string{"echo2"},
		BackendBaseUrl:  "http://localhost:3001",
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected loopback http backendBaseUrl to be valid, got: %v", err)
	}
}

func TestTrimURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no trailing slash unchanged", "https://blocks.ai", "https://blocks.ai"},
		{"single trailing slash stripped", "https://blocks.ai/", "https://blocks.ai"},
		{"multiple trailing slashes stripped", "https://blocks.ai///", "https://blocks.ai"},
		{"surrounding whitespace trimmed", "  https://blocks.ai/  ", "https://blocks.ai"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimURL(tc.in); got != tc.want {
				t.Fatalf("TrimURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestValidateBackendBaseURL_ParityWithWidget pins the inputs where CLI parity
// with the embed-auth widget (blocks-sdk/embed-auth/src/config.ts) is subtle,
// so a future change to either validator that reintroduces divergence trips a
// test. wantErr reflects what `new URL(...)` + the https/loopback rule decide
// in the browser (confirmed against Node's WHATWG URL implementation).
func TestValidateBackendBaseURL_ParityWithWidget(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool // what the widget's new URL()+scheme rule yields
	}{
		{"hostless port-only authority: browser throws", "https://:8080", true},
		{"empty authority: browser throws", "https://", true},
		{"userinfo is allowed by both", "https://user:pass@example.com", false},
		{"uppercase host allowed by both (widget lowercases)", "http://LOCALHOST:3001", false},
		{"ipv6 loopback allowed by both", "http://[::1]:3001", false},
		{"non-loopback http rejected by both", "http://staging.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBackendBaseURL(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateBackendBaseURL(%q) = nil, want error (widget rejects it)", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateBackendBaseURL(%q) = %v, want nil (widget accepts it)", tc.in, err)
			}
		})
	}
}
