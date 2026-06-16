package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/config"
)

// writeAgentCard drops a minimal agent card at path.
func writeAgentCard(t *testing.T, path string, identity map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	card := map[string]any{
		"identity": identity,
	}
	data, _ := json.MarshalIndent(card, "", "  ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// runCardUpdateFromCwd is a test harness that runs the post-deploy card flow
// from `cwd` so resolveLocalCardPath's sibling fallback resolves predictably.
func runCardUpdateFromCwd(t *testing.T, cwd string, cfg *config.BlocksConfig, deployedURL string, overrides map[string]string, stdinText string) (string, string) {
	t.Helper()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	var stdout, stderr bytes.Buffer
	maybeUpdateLocalAgentCards(cfg, deployedURL, overrides, strings.NewReader(stdinText), &stdout, &stderr)
	return stdout.String(), stderr.String()
}

// TestCardUpdate_SiblingFound_PromptAccepted appends a webApp entry when the
// card exists in the sibling-of-cwd convention and the user accepts.
func TestCardUpdate_SiblingFound_PromptAccepted(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{
		"agentName":   "echo",
		"description": "test",
	})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}

	stdout, _ := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "y\n")

	if !strings.Contains(stdout, "Updated") {
		t.Errorf("stdout %q should mention 'Updated'", stdout)
	}

	raw, _ := os.ReadFile(cardPath)
	var card map[string]any
	json.Unmarshal(raw, &card)
	identity := card["identity"].(map[string]any)
	apps, _ := identity["webApps"].([]any)
	if len(apps) != 1 {
		t.Fatalf("webApps = %v, want 1 entry", apps)
	}
	got := apps[0].(map[string]any)
	if got["url"] != "https://myapp.pages.dev" {
		t.Errorf("webApps[0].url = %v", got["url"])
	}
	if got["label"] != "myapp" {
		t.Errorf("webApps[0].label = %v, want myapp", got["label"])
	}
}

// TestCardUpdate_InvalidURL_NotWritten guards the post-deploy card flow with
// the same shape rule the backend enforces on identity.webApps[].url. A
// misbehaving plugin/partner response (or a manual mistake) that yields a
// schema-invalid deployedURL must NOT be written into agent-card.json, or the
// next `blocks publish` fails validation. The updater warns and skips instead.
func TestCardUpdate_InvalidURL_NotWritten(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{
		"agentName":   "echo",
		"description": "test",
	})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}

	// `ftp://` is rejected by ValidateWebAppURL (must be https or http
	// loopback). Stdin "y" would accept a prompt — but no prompt should
	// fire because the URL is rejected before the per-agent walk.
	_, stderr := runCardUpdateFromCwd(t, project, cfg, "ftp://myapp.example", nil, "y\n")

	if !strings.Contains(stderr, "ftp://myapp.example") {
		t.Errorf("stderr should warn about the invalid URL; got %q", stderr)
	}

	raw, _ := os.ReadFile(cardPath)
	var card map[string]any
	json.Unmarshal(raw, &card)
	identity, _ := card["identity"].(map[string]any)
	if apps, ok := identity["webApps"].([]any); ok && len(apps) != 0 {
		t.Errorf("invalid URL must not be written to the card; got webApps=%v", apps)
	}
}

// TestCardUpdate_AlreadyPresent_NoPrompt skips a card whose webApps already
// contains the URL — re-deploys must be idempotent.
func TestCardUpdate_AlreadyPresent_NoPrompt(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{
		"agentName": "echo",
		"webApps": []any{
			map[string]any{"url": "https://myapp.pages.dev", "label": "existing"},
		},
	})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}

	// Empty stdin: a stray prompt would block; we assert no prompt fires.
	stdout, _ := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "")

	if strings.Contains(stdout, "Add") {
		t.Errorf("stdout should not prompt when URL is already present; got %q", stdout)
	}

	raw, _ := os.ReadFile(cardPath)
	var card map[string]any
	json.Unmarshal(raw, &card)
	identity := card["identity"].(map[string]any)
	apps, _ := identity["webApps"].([]any)
	if len(apps) != 1 {
		t.Errorf("webApps length should stay at 1 (idempotent), got %d", len(apps))
	}
}

// TestCardUpdate_CardNotFound_PrintsSnippet prints a copy-pasteable snippet
// when the agent card is not local.
func TestCardUpdate_CardNotFound_PrintsSnippet(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}
	_, stderr := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "")

	if !strings.Contains(stderr, "not found locally") {
		t.Errorf("stderr should print snippet for missing card; got %q", stderr)
	}
	if !strings.Contains(stderr, "https://myapp.pages.dev") {
		t.Errorf("snippet must include the URL; got %q", stderr)
	}
}

// TestCardUpdate_CardPathOverride uses --card-path to point to a non-sibling
// location.
func TestCardUpdate_CardPathOverride(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	customDir := filepath.Join(parent, "off-tree")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(customDir, "card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}
	overrides := map[string]string{"echo": cardPath}

	stdout, _ := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", overrides, "y\n")

	if !strings.Contains(stdout, "Updated") {
		t.Errorf("override path was not honored; stdout %q", stdout)
	}
	raw, _ := os.ReadFile(cardPath)
	if !strings.Contains(string(raw), "https://myapp.pages.dev") {
		t.Errorf("override card not updated: %s", raw)
	}
}

// TestCardUpdate_AgentCardPathsFromConfig honors cfg.AgentCardPaths.
func TestCardUpdate_AgentCardPathsFromConfig(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	customDir := filepath.Join(parent, "configured")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(customDir, "card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	cfg := &config.BlocksConfig{
		Agents:         []string{"echo"},
		AgentCardPaths: map[string]string{"echo": cardPath},
	}
	stdout, _ := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "y\n")
	if !strings.Contains(stdout, "Updated") {
		t.Errorf("agentCardPaths config was not honored; stdout %q", stdout)
	}
}

// TestCardUpdate_PromptDeclined leaves the file untouched.
func TestCardUpdate_PromptDeclined(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	before, _ := os.ReadFile(cardPath)

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}
	runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "n\n")

	after, _ := os.ReadFile(cardPath)
	if !bytes.Equal(before, after) {
		t.Errorf("card was modified after 'n' answer:\nbefore: %s\nafter: %s", before, after)
	}
}

// TestCardUpdate_EOFSkipsUpdate is the impl_07 F7 regression gate: a deploy
// running in CI (or any pipeline) with no interactive stdin must NOT silently
// modify agent-card.json. The reader returns io.EOF immediately, and the
// previous code path treated empty input as "yes, edit the file."
func TestCardUpdate_EOFSkipsUpdate(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	before, _ := os.ReadFile(cardPath)

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}
	// Empty string → reader hits EOF on the first ReadString call.
	stdout, stderr := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "")

	after, _ := os.ReadFile(cardPath)
	if !bytes.Equal(before, after) {
		t.Errorf("card was modified despite EOF stdin:\nbefore: %s\nafter: %s", before, after)
	}
	if !strings.Contains(stderr, "skipping card update") {
		t.Errorf("expected 'skipping card update' on stderr, got:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "https://myapp.pages.dev") {
		t.Errorf("expected copy-pasteable snippet on stderr, got:\nstderr=%s", stderr)
	}
}

// TestParseCardPathFlags happy-paths and malformed flags.
func TestParseCardPathFlags(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		got, err := parseCardPathFlags([]string{"echo=../echo/agent-card.json", "translator=/abs/path.json"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["echo"] != "../echo/agent-card.json" || got["translator"] != "/abs/path.json" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("no equals", func(t *testing.T) {
		_, err := parseCardPathFlags([]string{"justaname"})
		if err == nil {
			t.Error("expected error for malformed flag")
		}
	})
	t.Run("empty key", func(t *testing.T) {
		_, err := parseCardPathFlags([]string{"=foo"})
		if err == nil {
			t.Error("expected error for empty key")
		}
	})
	t.Run("empty value", func(t *testing.T) {
		_, err := parseCardPathFlags([]string{"k="})
		if err == nil {
			t.Error("expected error for empty value")
		}
	})
}

// TestCardUpdate_NoCardUpdateFlagSkipsEntirely exercises runDeploy with the
// --no-card-update flag set: even with a card on disk, no prompt fires.
func TestCardUpdate_NoCardUpdateFlagSkipsEntirely(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	webDir := filepath.Join(project, "web")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(webDir, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	cfgJSON := map[string]any{
		"templateVersion": "1.0.0",
		"agents":          []string{"echo"},
	}
	cfgData, _ := json.Marshal(cfgJSON)
	os.WriteFile(filepath.Join(project, "blocks.config.json"), cfgData, 0644)

	credCleanup := setupFakeCredentials(t)
	defer credCleanup()
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	oldDir, _ := os.Getwd()
	os.Chdir(project)
	t.Cleanup(func() { os.Chdir(oldDir) })

	deployNoCardUpdate = true
	t.Cleanup(func() { deployNoCardUpdate = false })

	stubAdapter(t, "cloudflare", "https://nocardupdate.pages.dev")

	before, _ := os.ReadFile(cardPath)
	captureStdout(func() {
		if err := runDeploy(t.Context(), "cloudflare"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})
	after, _ := os.ReadFile(cardPath)
	if !bytes.Equal(before, after) {
		t.Errorf("--no-card-update was set but card was modified")
	}
}

// TestCardUpdate_WebAppsAtCap_NotWritten guards the 25-item webApps cap
// (schemas/agent-card.schema.json maxItems:25). Appending a 26th entry would
// produce a card that fails the next `blocks publish`. The updater warns and
// skips instead.
func TestCardUpdate_WebAppsAtCap_NotWritten(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")

	existing := make([]any, 25)
	for i := range existing {
		existing[i] = map[string]any{
			"url":   "https://app-" + strconv.Itoa(i) + ".example.com",
			"label": "app",
		}
	}
	writeAgentCard(t, cardPath, map[string]any{
		"agentName": "echo",
		"webApps":   existing,
	})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}

	_, stderr := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "y\n")

	if !strings.Contains(stderr, "25") {
		t.Errorf("stderr should warn about the webApps cap; got %q", stderr)
	}

	raw, _ := os.ReadFile(cardPath)
	var card map[string]any
	json.Unmarshal(raw, &card)
	identity, _ := card["identity"].(map[string]any)
	apps, _ := identity["webApps"].([]any)
	if len(apps) != 25 {
		t.Errorf("at-cap card must not grow; got %d webApps, want 25", len(apps))
	}
}

// TestCardUpdate_LabelTooLong_NotWritten guards the 80-char label max
// (schemas/agent-card.schema.json webApps[].label maxLength:80). A cwd
// directory name longer than 80 chars would otherwise produce an
// unpublishable card. The updater warns and skips.
func TestCardUpdate_LabelTooLong_NotWritten(t *testing.T) {
	parent := t.TempDir()
	longName := strings.Repeat("a", 81)
	project := filepath.Join(parent, longName)
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{
		"agentName": "echo",
	})

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}

	_, stderr := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "y\n")

	if !strings.Contains(stderr, "label") {
		t.Errorf("stderr should warn about the over-length label; got %q", stderr)
	}

	raw, _ := os.ReadFile(cardPath)
	var card map[string]any
	json.Unmarshal(raw, &card)
	identity, _ := card["identity"].(map[string]any)
	if apps, ok := identity["webApps"].([]any); ok && len(apps) != 0 {
		t.Errorf("over-length label must not be written; got webApps=%v", apps)
	}
}
