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
//
// There is no error-returning variant any more, and there cannot be: the card walk
// runs after a non-idempotent upload, so it reports what it skipped and never hands a
// failure back to the deploy. Its outcomes are read off stdout and stderr.
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

// TestCardUpdate_NoInputNamesTheFlag covers the post-deploy card confirmation:
// --no-input must not read stdin, must leave the card untouched, and must name both
// the flag that withdrew the question and the flag that answers it.
//
// The premise changed with the exit code. This case used to require the skip to be
// returned as an error, which the deploy then reported — turning a completed upload
// and a written config into a failed command. Everything it asserted still holds
// (nothing read, nothing written, both flags named); what it no longer accepts is the
// error, because a note is the only form a post-upload skip may take.
func TestCardUpdate_NoInputNamesTheFlag(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	agentDir := filepath.Join(parent, "echo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(agentDir, "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})
	before, _ := os.ReadFile(cardPath)

	setNoInputMode(true)
	t.Cleanup(func() { setNoInputMode(false) })

	cfg := &config.BlocksConfig{Agents: []string{"echo"}}
	// "y\n" would accept the prompt if one were still read.
	stdout, stderr := runCardUpdateFromCwd(t, project, cfg, "https://myapp.pages.dev", nil, "y\n")
	if strings.Contains(stdout, "[Y/n]") {
		t.Errorf("no confirmation may be printed under --no-input; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "--no-input") || !strings.Contains(stderr, "--no-card-update") {
		t.Errorf("the note %q should name --no-input and the flag that answers it (--no-card-update)", stderr)
	}
	if !strings.Contains(stderr, "NOT updated") {
		t.Errorf("the note must say the card was not updated; got %q", stderr)
	}
	after, _ := os.ReadFile(cardPath)
	if !bytes.Equal(before, after) {
		t.Errorf("card must not be written under --no-input:\nbefore: %s\nafter: %s", before, after)
	}
}

// TestCardUpdate_NoInputSkippedByFlag is the other half of the contract: passing
// the flag the refusal names makes the invocation succeed, because --no-card-update
// suppresses the prompt before the updater is ever called.
func TestCardUpdate_NoInputSkippedByFlag(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"echo"}, "")
	defer cleanup()

	uploaded := recordingAdapter(t, "cloudflare", "https://myapp.pages.dev")
	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-input", "--no-card-update")
	})
	if err != nil {
		t.Fatalf("--no-input with --no-card-update must deploy without asking; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("expected the deploy to run")
	}
}

// TestCardUpdate_NoInputThroughArgv drives the same skip end to end, so the
// --no-input plumbing (root flag → prompt primitives → this prompt) is covered
// rather than the package variable alone. Stdin is at EOF, so an ungated read
// would silently skip the update and the deploy would succeed.
//
// The premise changed with the exit code, and this is the case that says why. The skip
// used to be returned as an error, so `blocks deploy --no-input` uploaded, wrote the
// config, and then exited nonzero: automation that retries on nonzero deploys a second
// time, paying for a second upload, and the developer reads a failure for a deployment
// that is live. So the exit code is asserted as zero here, alongside everything the
// case already required — the upload ran, the config was written, the card was not, and
// the flag that suppresses the question is named.
func TestCardUpdate_NoInputThroughArgv(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo"}, "")
	defer cleanup()
	deployNoCardUpdate = false // setupDeployTest suppresses the prompt; this test is about it

	cardPath := filepath.Join(t.TempDir(), "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})
	before, _ := os.ReadFile(cardPath)

	uploaded := recordingAdapter(t, "cloudflare", "https://myapp.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-input", "--card-path", "echo="+cardPath)
	})
	if err != nil {
		t.Fatalf("a deploy that uploaded must not exit nonzero because a post-deploy question could not be asked; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("the skip is about the card prompt only; the deploy itself should have run")
	}
	if !strings.Contains(out, "Deployed: https://myapp.pages.dev") {
		t.Errorf("the deploy must still be reported as done:\n%s", out)
	}
	if !strings.Contains(out, "NOT updated") || !strings.Contains(out, "--no-card-update") {
		t.Errorf("the run must say the card was not updated and name --no-card-update:\n%s", out)
	}
	if after, _ := os.ReadFile(cardPath); !bytes.Equal(before, after) {
		t.Errorf("card must not be written under --no-input:\nbefore: %s\nafter: %s", before, after)
	}
	// The upload succeeded, so the config write that follows it must still have
	// happened — the skip is not a rollback.
	raw, readErr := os.ReadFile(filepath.Join(dir, "blocks.config.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "https://myapp.pages.dev") {
		t.Errorf("lastDeployedUrl should have been saved before the card prompt; got %s", raw)
	}
}

// The declared form of the same intent says nothing about it. --no-card-update is how a
// caller states that the card is not to be touched, and a note explaining a question
// that was never going to be asked is noise in every later run of that pipeline.
func TestCardUpdate_NoInputWithNoCardUpdateSaysNothingAboutTheCard(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"echo"}, "")
	defer cleanup()

	cardPath := filepath.Join(t.TempDir(), "agent-card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": "echo"})

	uploaded := recordingAdapter(t, "cloudflare", "https://myapp.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-input", "--no-card-update", "--card-path", "echo="+cardPath)
	})
	if err != nil {
		t.Fatalf("--no-input with --no-card-update must deploy without asking; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("expected the deploy to run")
	}
	if strings.Contains(out, "agent card") || strings.Contains(out, "NOT updated") {
		t.Errorf("--no-card-update already stated the intent; the run must not explain the skip:\n%s", out)
	}
}

// A malformed --card-path is settled before the upload. It is a mistake in the
// invocation, knowable without deploying, and the alternative — discovering it in the
// card walk — is the same defect this file's --no-input cases describe: a completed,
// billable upload reported as a failed command.
func TestCardUpdate_MalformedCardPathIsRefusedBeforeTheUpload(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"echo"}, "")
	defer cleanup()

	uploaded := recordingAdapter(t, "cloudflare", "https://myapp.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--card-path", "justaname")
	})
	if err == nil {
		t.Fatalf("expected a malformed --card-path to be refused; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--card-path") {
		t.Errorf("error %q should name the flag it could not parse", err.Error())
	}
	if *uploaded {
		t.Error("a flag the CLI could not parse must be refused before anything is uploaded")
	}
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

	// The complement of the --no-input cases: an interactive run still asks, and still
	// writes when the answer is yes.
	if !strings.Contains(stdout, "Add https://myapp.pages.dev to identity.webApps") {
		t.Errorf("stdout %q should carry the confirmation", stdout)
	}
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

// TestCardUpdate_EOFSkipsUpdate is the regression gate: a deploy
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
		"backendBaseUrl":  "https://app.blocks.ai",
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

// The line that reports a written card told the user to re-run 'blocks publish' from the
// agent's directory, with the agent name interpolated into it. That name comes from
// blocks.config.json, so a project can choose it, and a line the CLI formats as
// something to run may not be built from a value a project chooses: it is a line that
// gets pasted into a shell, and no display-escaping helper makes `;`, `&&`, backticks or
// $(...) inert there — they are ordinary printable characters.
//
// The scan reuses commandLines, this package's one definition of "reads as something to
// run", and the path and agent are still reported so the user can see which file moved.
func TestTheUpdatedCardLineOffersNoPastableCommandBuiltFromTheAgentName(t *testing.T) {
	const hostileAgent = "echo;$(id)"

	parent := t.TempDir()
	project := filepath.Join(parent, "myapp")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	cardPath := filepath.Join(parent, "card.json")
	writeAgentCard(t, cardPath, map[string]any{"agentName": hostileAgent})

	cfg := &config.BlocksConfig{Agents: []string{hostileAgent}}
	stdout, stderr := runCardUpdateFromCwd(t, project, cfg,
		"https://myapp.pages.dev", map[string]string{hostileAgent: cardPath}, "y\n")

	if !strings.Contains(stdout, "Updated") {
		t.Fatalf("premise: the card must have been written; stdout=%s stderr=%s", stdout, stderr)
	}
	assertNoInjectedValueInCommandLines(t, stdout, hostileAgent)
	if !strings.Contains(stdout, cardPath) {
		t.Errorf("the file that changed must still be reported:\n%s", stdout)
	}
}
