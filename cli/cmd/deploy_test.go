package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
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

// TestConfirmYesNo verifies the shared Y/n prompt: only an explicit "n"/"no"
// declines; empty input (Enter), EOF, and unrecognized answers default to yes.
func TestConfirmYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},          // EOF → continue (default-yes)
		{"\n", true},        // blank line → continue
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
