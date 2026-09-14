package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

// setupDeployTest configures credentials, writes blocks.config.json and web/
// into a temp directory, and returns (tempDir, cleanup).
func setupDeployTest(t *testing.T, agents []string, deployTarget string) (string, func()) {
	t.Helper()

	dir := t.TempDir()

	webDir := filepath.Join(dir, "web")
	if err := os.Mkdir(webDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := map[string]interface{}{
		"templateVersion": "1.0.0",
		"agents":          agents,
		"backendBaseUrl":  "https://app.blocks.ai",
	}
	if deployTarget != "" {
		cfg["deployTarget"] = deployTarget
	}
	cfgData, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "blocks.config.json"), cfgData, 0644); err != nil {
		t.Fatal(err)
	}

	credCleanup := setupFakeCredentials(t)

	// Inject partner env-var tokens so the credential prompts are skipped.
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-cf-token")
	t.Setenv("VERCEL_TOKEN", "test-vercel-token")
	t.Setenv("NETLIFY_AUTH_TOKEN", "test-netlify-token")

	// Suppress the post-deploy card-update prompt in tests by default;
	// the dedicated card-update tests opt back in explicitly.
	prevSkip := deployNoCardUpdate
	deployNoCardUpdate = true
	prevPaths := deployCardPaths
	deployCardPaths = nil

	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	return dir, func() {
		os.Chdir(oldDir)
		credCleanup()
		deployNoCardUpdate = prevSkip
		deployCardPaths = prevPaths
	}
}

// stubAdapter registers a disk-source override that returns a fixed URL.
// Disk sources clobber built-ins per the precedence rule.
func stubAdapter(t *testing.T, name, returnURL string) {
	t.Helper()
	orig, hadOrig := deploy.Resolve(name)
	deploy.Register(deploy.Adapter{
		Name:       name,
		Source:     deploy.SourceDisk,
		Credential: deploy.CredentialFlowNone,
		Upload: func(ctx context.Context, creds *auth.ProviderCredentials, dir string) (string, error) {
			return returnURL, nil
		},
	})
	t.Cleanup(func() {
		// Restore by reset → if it was a built-in, Reset() puts it back; if
		// it was a custom name, it's gone again.
		deploy.Reset()
		if hadOrig && orig.Source == deploy.SourceDisk {
			deploy.Register(orig)
		}
	})
}

// TestRunDeploy_StaticUploadOnly verifies that runDeploy uploads static assets
// and exits without making any registry mutations.
func TestRunDeploy_StaticUploadOnly(t *testing.T) {
	registryCalled := false
	embeddedAuthCalled := false

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/registry/agents") {
			registryCalled = true
		}
		if strings.Contains(r.URL.Path, "/embedded-auth/") {
			embeddedAuthCalled = true
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
	stubAdapter(t, "cloudflare", "https://my-app.pages.dev")

	captureStdout(func() {
		if err := runDeploy(context.Background(), "cloudflare"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})

	if registryCalled {
		t.Error("runDeploy must not call /api/v1/registry/agents — per-origin allowlist mutation removed")
	}
	if embeddedAuthCalled {
		t.Error("runDeploy must not call /embedded-auth/* — config endpoints are gone")
	}
}

// TestRunDeploy_ConfigSaved verifies that blocks.config.json is updated with
// lastDeployedUrl and deployTarget after a successful deploy.
func TestRunDeploy_ConfigSaved(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")
	stubAdapter(t, "vercel", "https://my-project.vercel.app")

	captureStdout(func() {
		if err := runDeploy(context.Background(), "vercel"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})

	cfgData, err := os.ReadFile(filepath.Join(mustCwd(), "blocks.config.json"))
	if err != nil {
		t.Fatalf("read blocks.config.json: %v", err)
	}
	var cfgMap map[string]interface{}
	json.Unmarshal(cfgData, &cfgMap)

	if cfgMap["lastDeployedUrl"] != "https://my-project.vercel.app" {
		t.Errorf("lastDeployedUrl = %v, want https://my-project.vercel.app", cfgMap["lastDeployedUrl"])
	}
	if cfgMap["deployTarget"] != "vercel" {
		t.Errorf("deployTarget = %v, want vercel", cfgMap["deployTarget"])
	}
}

// TestRunDeploy_MissingWebDir verifies a clear error when web/ doesn't exist.
func TestRunDeploy_MissingWebDir(t *testing.T) {
	dir := t.TempDir()

	cfg := map[string]interface{}{
		"templateVersion": "1.0.0",
		"agents":          []string{"my_agent"},
		"backendBaseUrl":  "https://app.blocks.ai",
	}
	cfgData, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, "blocks.config.json"), cfgData, 0644)

	credCleanup := setupFakeCredentials(t)
	defer credCleanup()

	t.Setenv("CLOUDFLARE_API_TOKEN", "test-cf-token")

	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	stubAdapter(t, "cloudflare", "https://x.pages.dev")

	err := runDeploy(context.Background(), "cloudflare")
	if err == nil {
		t.Fatal("expected error when web/ is missing")
	}
	if !strings.Contains(err.Error(), "web/") {
		t.Errorf("error %q should mention web/ directory", err.Error())
	}
}

// TestRunDeploy_NoTargetNoConfig errors when neither arg nor config provides a target.
func TestRunDeploy_NoTargetNoConfig(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()

	err := runDeploy(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when no target is set")
	}
	if !strings.Contains(err.Error(), "no deploy target") {
		t.Errorf("error %q should mention 'no deploy target'", err.Error())
	}
}

// TestRunDeploy_UnknownTargetRejected verifies typo-style targets fail with a hint.
func TestRunDeploy_UnknownTargetRejected(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()

	err := runDeploy(context.Background(), "bogus_target_99")
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error %q should mention 'unsupported'", err.Error())
	}
}

// TestRunDeploy_FallsBackToConfigDefault uses blocks.config.json:deployTarget
// when no positional arg is given.
func TestRunDeploy_FallsBackToConfigDefault(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "netlify")
	defer cleanup()

	stubAdapter(t, "netlify", "https://defaulted.netlify.app")

	captureStdout(func() {
		if err := runDeploy(context.Background(), ""); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})

	cfgData, _ := os.ReadFile(filepath.Join(mustCwd(), "blocks.config.json"))
	var cfgMap map[string]interface{}
	json.Unmarshal(cfgData, &cfgMap)
	if cfgMap["lastDeployedUrl"] != "https://defaulted.netlify.app" {
		t.Errorf("lastDeployedUrl = %v", cfgMap["lastDeployedUrl"])
	}
}

func TestRunDeploy_WarnsOnBackendMismatch(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()

	// Bake a backend into the project config.
	writeConfigBackend(t, dir, "https://blocks.acme.com")

	// Active profile now points elsewhere (the profile-switch scenario).
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.other.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_BACKEND_URL", "") // force profile-based resolution

	stubAdapter(t, "cloudflare", "https://my-app.pages.dev")

	out := captureStdoutStderr(func() {
		if err := runDeploy(context.Background(), "cloudflare"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})
	if !strings.Contains(out, "blocks.acme.com") || !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("expected a divergence warning naming the baked backend; got:\n%s", out)
	}
}

func TestRunDeploy_NoWarnWhenBackendMatches(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()
	writeConfigBackend(t, dir, "https://blocks.acme.com")

	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_BACKEND_URL", "https://blocks.acme.com")

	stubAdapter(t, "cloudflare", "https://my-app.pages.dev")
	out := captureStdoutStderr(func() {
		if err := runDeploy(context.Background(), "cloudflare"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})
	if strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("did not expect a warning when backend matches; got:\n%s", out)
	}
}

// captureStdoutStderr redirects both os.Stdout and os.Stderr and returns the
// combined output. The divergence warning is written to stderr (it is a
// warning), so the mismatch assertions must observe both streams.
//
// WARNING: mutates process-wide os.Stdout/os.Stderr; callers must NOT use
// t.Parallel().
func captureStdoutStderr(fn func()) string {
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	os.Stderr = w

	fn()

	w.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr

	var buf strings.Builder
	io.Copy(&buf, r)
	return buf.String()
}

func TestRunDeploy_RejectsConfigMissingBackend(t *testing.T) {
	// A config scaffolded before this change (no backendBaseUrl) must fail
	// validation loudly rather than silently deploying to a default backend.
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()
	// Overwrite the helper's (now valid) config with a legacy one lacking the field.
	writeLegacyConfig(t, dir)

	stubAdapter(t, "cloudflare", "https://my-app.pages.dev")
	err := runDeploy(context.Background(), "cloudflare")
	if err == nil || !strings.Contains(err.Error(), "backendBaseUrl") {
		t.Fatalf("expected a backendBaseUrl validation error, got: %v", err)
	}
}

// writeConfigBackend rewrites only the backendBaseUrl of the blocks.config.json
// in dir, preserving whatever agents/templateVersion/deployTarget the caller's
// setup already wrote (rather than hardcoding an agent list that could drift).
func writeConfigBackend(t *testing.T, dir, backend string) {
	t.Helper()
	path := filepath.Join(dir, "blocks.config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["backendBaseUrl"] = backend
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		t.Fatal(err)
	}
}

// writeLegacyConfig writes a blocks.config.json WITHOUT backendBaseUrl,
// simulating a project scaffolded before this change.
func writeLegacyConfig(t *testing.T, dir string) {
	t.Helper()
	cfg := map[string]interface{}{
		"templateVersion": "1.0.0",
		"agents":          []string{"echo2"},
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "blocks.config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureDeployCredentials_SourceAware verifies the source-aware dispatch:
// a disk-source adapter named "cloudflare" must use the generic plugin
// credential path, NOT the built-in CloudflareFlow. The previous name-only
// switch silently ran CloudflareFlow against the override, defeating the
// override.
func TestEnsureDeployCredentials_SourceAware(t *testing.T) {
	// Disk plugin named "cloudflare" with CredentialFlowNone → generic path
	// returns an empty-token ProviderCredentials. CloudflareFlow would have
	// either looked up a stored token or prompted (which would block on
	// missing stdin), neither of which happens here.
	a := deploy.Adapter{
		Name:       "cloudflare",
		Source:     deploy.SourceDisk,
		Credential: deploy.CredentialFlowNone,
	}
	creds, err := ensureDeployCredentials(context.Background(), a)
	if err != nil {
		t.Fatalf("ensureDeployCredentials: %v", err)
	}
	if creds == nil {
		t.Fatal("creds = nil; expected generic plugin credentials struct")
	}
	if creds.Provider != "cloudflare" {
		t.Errorf("creds.Provider = %q, want %q", creds.Provider, "cloudflare")
	}
	if creds.AccessToken != "" {
		t.Errorf("creds.AccessToken = %q; generic-none path should leave it empty", creds.AccessToken)
	}
}

// runDeployArgv drives `blocks deploy` the way a caller does — through argv, so
// the --no-input plumbing in PersistentPreRun is exercised rather than simulated —
// with stdin already at EOF so an ungated read fails the test instead of hanging
// it. It returns the command's error.
//
// Every flag it touches is reset on cleanup: cobra does not re-apply defaults on a
// second Execute(), so a test that passes --no-input would otherwise leave every
// later test in the package running with it set.
func runDeployArgv(t *testing.T, args ...string) error {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close() // reads return EOF immediately
	origStdin := os.Stdin
	os.Stdin = r
	origScanner := stdinScanner
	stdinScanner = nil
	t.Cleanup(func() {
		os.Stdin = origStdin
		stdinScanner = origScanner
		r.Close()
		rootCmd.SetArgs(nil)
		rootNoInput = false
		setNoInputMode(false)
		wizard.SetNoInputMode(false)
		deployList = false
		deployNoCardUpdate = false
		deployCardPaths = nil
	})
	rootCmd.SetArgs(append([]string{"deploy"}, args...))
	return rootCmd.Execute()
}

// recordingAdapter registers a disk-source adapter and reports whether its Upload
// ever ran, so a test can tell "refused before deploying" from "deployed, then
// complained".
func recordingAdapter(t *testing.T, name, returnURL string) *bool {
	t.Helper()
	called := false
	deploy.Register(deploy.Adapter{
		Name:       name,
		Source:     deploy.SourceDisk,
		Credential: deploy.CredentialFlowNone,
		Upload: func(ctx context.Context, creds *auth.ProviderCredentials, dir string) (string, error) {
			called = true
			return returnURL, nil
		},
	})
	t.Cleanup(deploy.Reset)
	return &called
}

// setupDivergentBackend makes the bundle's baked backend disagree with the active
// one, which is the condition the "Continue deploying anyway?" confirmation guards.
func setupDivergentBackend(t *testing.T, dir string) {
	t.Helper()
	writeConfigBackend(t, dir, "https://blocks.acme.com")
	restore := isolateProfiles(t)
	t.Cleanup(restore)
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.other.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_BACKEND_URL", "") // force profile-based resolution
}

// TestDeployNoInput_BackendDivergenceRefused covers the confirmation at the
// divergence warning. On a terminal it used to read stdin even under --no-input,
// so a caller who asked never to be prompted could block there.
func TestDeployNoInput_BackendDivergenceRefused(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()
	setupDivergentBackend(t, dir)

	origIsTTY := isTTY
	isTTY = func() bool { return true } // the case that used to block
	t.Cleanup(func() { isTTY = origIsTTY })

	uploaded := recordingAdapter(t, "cloudflare", "https://my-app.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-input", "--no-card-update")
	})
	if err == nil {
		t.Fatalf("expected --no-input to refuse the divergence confirmation; got nil error, output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--no-input") {
		t.Errorf("error %q should name --no-input", err.Error())
	}
	if !strings.Contains(err.Error(), "blocks init") {
		t.Errorf("error %q should name the command that answers it ('blocks init ... --backend-url')", err.Error())
	}
	if *uploaded {
		t.Error("deploy must be refused before the upload, not after it")
	}
}

// TestDeployNoInput_BackendDivergencePlainNonTTYStillDeploys is the regression
// gate for the same site: without --no-input, a non-terminal session keeps taking
// its documented default of warning and continuing.
func TestDeployNoInput_BackendDivergencePlainNonTTYStillDeploys(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()
	setupDivergentBackend(t, dir)

	uploaded := recordingAdapter(t, "cloudflare", "https://my-app.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-card-update")
	})
	if err != nil {
		t.Fatalf("plain non-TTY deploy must continue past the divergence warning; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("documented default off a terminal is to warn and continue; upload did not run")
	}
	if !strings.Contains(strings.ToLower(out), "warning") {
		t.Errorf("expected the divergence warning to still be printed; got:\n%s", out)
	}
}

// TestDeployNoInput_TargetFromConfig covers the deploy-target picker. --no-input
// takes the documented non-interactive branch — the target this project already
// recorded — rather than asking or failing.
func TestDeployNoInput_TargetFromConfig(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "netlify")
	defer cleanup()

	origIsTTY := isTTY
	isTTY = func() bool { return true } // the branch that reaches the picker
	t.Cleanup(func() { isTTY = origIsTTY })

	uploaded := recordingAdapter(t, "netlify", "https://defaulted.netlify.app")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "--no-input", "--no-card-update")
	})
	if err != nil {
		t.Fatalf("--no-input should resolve the target from blocks.config.json; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("expected the saved deployTarget to be deployed")
	}
}

// TestDeployNoInput_NoTargetNamesTheArgument covers the same picker with nothing
// to fall back on: the refusal has to name what supplies the target.
func TestDeployNoInput_NoTargetNamesTheArgument(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()

	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() { isTTY = origIsTTY })

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "--no-input", "--no-card-update")
	})
	if err == nil {
		t.Fatalf("expected an error when --no-input and no target is resolvable; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no deploy target") || !strings.Contains(err.Error(), "deployTarget") {
		t.Errorf("error %q should name the positional argument and deployTarget", err.Error())
	}
}

// TestDeployNoInput_PluginTokenNamesEnvVar covers plugin credential entry: an
// on-disk target with an api-token flow prompts for the token when its env var is
// unset, and --no-input has to name that variable instead of reading stdin.
func TestDeployNoInput_PluginTokenNamesEnvVar(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()
	t.Setenv("MY_PLUGIN_TOKEN", "")

	deploy.Register(deploy.Adapter{
		Name:             "myplugin",
		Source:           deploy.SourceDisk,
		Credential:       deploy.CredentialFlowAPIToken,
		CredentialEnvVar: "MY_PLUGIN_TOKEN",
		Upload: func(ctx context.Context, creds *auth.ProviderCredentials, dir string) (string, error) {
			return "https://plugin.example.com", nil
		},
	})
	t.Cleanup(deploy.Reset)

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "myplugin", "--no-input", "--no-card-update")
	})
	if err == nil {
		t.Fatalf("expected --no-input to refuse the plugin token prompt; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--no-input") || !strings.Contains(err.Error(), "MY_PLUGIN_TOKEN") {
		t.Errorf("error %q should name --no-input and the plugin's credentialEnvVar", err.Error())
	}
}

// TestDeployNoInput_PluginTokenPlainNonTTYUnchanged is the regression gate: with
// no --no-input the prompt still runs and still fails on unreadable stdin with its
// own wording, so nothing about the plain path moved.
func TestDeployNoInput_PluginTokenPlainNonTTYUnchanged(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()
	t.Setenv("MY_PLUGIN_TOKEN", "")

	deploy.Register(deploy.Adapter{
		Name:             "myplugin",
		Source:           deploy.SourceDisk,
		Credential:       deploy.CredentialFlowAPIToken,
		CredentialEnvVar: "MY_PLUGIN_TOKEN",
		Upload: func(ctx context.Context, creds *auth.ProviderCredentials, dir string) (string, error) {
			return "https://plugin.example.com", nil
		},
	})
	t.Cleanup(deploy.Reset)

	var err error
	captureStdoutStderr(func() {
		err = runDeployArgv(t, "myplugin", "--no-card-update")
	})
	if err == nil {
		t.Fatal("expected the token read to fail on EOF stdin")
	}
	if !strings.Contains(err.Error(), "read token") {
		t.Errorf("error %q should be the unchanged read failure", err.Error())
	}
	if strings.Contains(err.Error(), "--no-input") {
		t.Errorf("error %q must not mention --no-input when the flag was not passed", err.Error())
	}
}

// TestDeployNoInput_PartnerTokenNamesEnvVar covers the built-in partner token
// prompt reached through `blocks deploy`, which reads stdin only after its env var
// and stored credential come up empty.
func TestDeployNoInput_PartnerTokenNamesEnvVar(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()
	// setupDeployTest injects partner tokens to skip these prompts; this test is
	// about what happens when there is none.
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	deploy.Reset() // built-in cloudflare, so the partner flow (not the plugin path) runs
	t.Cleanup(deploy.Reset)

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-input", "--no-card-update")
	})
	if err == nil {
		t.Fatalf("expected --no-input to refuse the Cloudflare token prompt; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--no-input") || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Errorf("error %q should name --no-input and CLOUDFLARE_API_TOKEN", err.Error())
	}
}

// TestDeployNoInput_PartnerTokenPlainNonTTYUnchanged is the regression gate for
// the partner prompt: without the flag it still reads stdin and still reports its
// own read failure.
func TestDeployNoInput_PartnerTokenPlainNonTTYUnchanged(t *testing.T) {
	_, cleanup := setupDeployTest(t, []string{"my_agent"}, "")
	defer cleanup()
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	deploy.Reset()
	t.Cleanup(deploy.Reset)

	var err error
	captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-card-update")
	})
	if err == nil {
		t.Fatal("expected the partner token read to fail on EOF stdin")
	}
	if strings.Contains(err.Error(), "--no-input") {
		t.Errorf("error %q must not mention --no-input when the flag was not passed", err.Error())
	}
}

// TestConfirmYesNo verifies the shared Y/n prompt: only an explicit "n"/"no"
// declines; empty input (Enter), EOF, and unrecognized answers default to yes.
func TestConfirmYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},   // EOF → continue (default-yes)
		{"\n", true}, // blank line → continue
		{"y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{"no\n", false},
		{"N\n", false},
		{"No\n", false},
		{"garbage\n", true}, // unrecognized → continue
	}
	for _, tc := range cases {
		var got bool
		// captureStdout swallows the printed prompt; we assert the return.
		captureStdout(func() {
			got = confirmYesNo(strings.NewReader(tc.in), "Continue? (Y/n): ")
		})
		if got != tc.want {
			t.Errorf("confirmYesNo(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// The backend URL the divergence warning reports is attacker-influenceable twice over:
// the bundle's own comes from blocks.config.json, and the active one from a profile or a
// project .env. Both are values `blocks deploy` has to report and neither may appear
// inside the retarget instruction, because a line presented as a command is a line that
// gets pasted into a shell — and termsafe.Text says nothing about that, since `;`, `&&`,
// backticks and $(...) pass through it as the ordinary printable characters they are.
//
// The scan reuses commandLines, this package's one definition of "reads as something to
// run". The second assertion is narrower on purpose: the pre-fix warning spread the
// remedy over two lines — "To retarget, re-run" then "'blocks init … --backend-url <the
// active backend>'" — which a reader pastes as one command whether or not a marker set
// notices it, so the instruction is held to carrying no value at all.
func TestTheDivergenceWarningOffersNoPastableCommandBuiltFromTheBackendURL(t *testing.T) {
	dir, cleanup := setupDeployTest(t, []string{"echo2"}, "")
	defer cleanup()

	const injectedActiveBackend = "https://blocks.acme.example/x;$(id)"
	writeConfigBackend(t, dir, "https://blocks.other.example")
	defer isolateProfiles(t)()
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: injectedActiveBackend, Orgs: map[string]profiles.OrgKey{},
	}, true); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Setenv("BLOCKS_BACKEND_URL", "") // force profile-based resolution

	uploaded := recordingAdapter(t, "cloudflare", "https://my-app.pages.dev")

	var err error
	out := captureStdoutStderr(func() {
		err = runDeployArgv(t, "cloudflare", "--no-card-update")
	})
	if err != nil {
		t.Fatalf("a non-terminal deploy must warn and continue; got %v, output:\n%s", err, out)
	}
	if !*uploaded {
		t.Error("premise: the deploy this warning accompanies must have run")
	}

	assertNoInjectedValueInCommandLines(t, out, injectedActiveBackend)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "blocks init") && strings.Contains(line, injectedActiveBackend) {
			t.Errorf("the retarget instruction carries the active backend URL:\n%s", line)
		}
	}
	// The divergence must still be reported, and both backends named, or the warning
	// would be safe by saying nothing.
	if !strings.Contains(out, injectedActiveBackend) {
		t.Errorf("the warning must still name the active backend:\n%s", out)
	}
	if !strings.Contains(out, "https://blocks.other.example") {
		t.Errorf("the warning must still name the backend the bundle was built for:\n%s", out)
	}
	if !strings.Contains(out, "blocks init") {
		t.Errorf("the remedy must still name the command that rebuilds web/:\n%s", out)
	}
}

// The Project banner names the directory being deployed, and a directory
// name can carry control or bidi characters — this is a line the operator
// reads to confirm what is about to be published, so the path is termsafe'd
// like every other externally-influenced value.
func TestRunDeploy_ProjectBannerTermsafesHostilePath(t *testing.T) {
	// The hostile name needs control characters, which are illegal in
	// Windows filenames — the test cannot construct its fixture there.
	if runtime.GOOS == "windows" {
		t.Skip("control characters are illegal in Windows filenames, so the hostile directory cannot be created here")
	}
	root := t.TempDir()
	// ESC and the RLO bidi character are both legal in a directory name.
	hostile := filepath.Join(root, "pwn\x1b[2K‮proj")
	webDir := filepath.Join(hostile, "web")
	if err := os.MkdirAll(webDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{
		"templateVersion": "1.0.0",
		"agents":          []string{"echo2"},
		"backendBaseUrl":  "https://app.blocks.ai",
	}
	cfgData, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(hostile, "blocks.config.json"), cfgData, 0644); err != nil {
		t.Fatal(err)
	}

	setupFakeCredentials(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-cf-token")
	prevSkip := deployNoCardUpdate
	deployNoCardUpdate = true
	t.Cleanup(func() { deployNoCardUpdate = prevSkip })

	oldDir, _ := os.Getwd()
	if err := os.Chdir(hostile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })

	stubAdapter(t, "cloudflare", "https://my-app.pages.dev")

	out := captureStdoutStderr(func() {
		if err := runDeploy(context.Background(), "cloudflare"); err != nil {
			t.Fatalf("runDeploy: %v", err)
		}
	})
	if strings.Contains(out, "\x1b[2K") {
		t.Errorf("Project banner emitted a raw control sequence from the hostile directory name:\n%q", out)
	}
	if !strings.Contains(out, `pwn\x1b[2K`) {
		t.Errorf("Project banner did not name the project in escaped form:\n%q", out)
	}
	if !strings.Contains(out, "\\u202e") {
		t.Errorf("Project banner did not escape the bidi character:\n%q", out)
	}
}
