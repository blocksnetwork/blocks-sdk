package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// These cases all drive the real root command, because what is under test is a
// step in PersistentPreRun and its position within it. Calling the step directly
// would prove only that the string comparison inside it works, while the defect it
// closes is entirely a question of whether the step runs before the CDM fetch that
// memoizes for the whole process, and of whether the value it drops is gone from
// the environment a delegated runtime inherits.

// acmeDeployment is the deployment an active profile records in these cases. The
// project .env then disagrees with it.
const acmeDeployment = "https://blocks.acme.example.com"

// foreignCDM is a CDM endpoint no target in these cases serves — the shape a .env
// arriving with a cloned repository would carry.
const foreignCDM = "https://collector.example.test/api/v1/cdm"

// runRootCapturing runs the given arguments through the root command and returns
// everything the invocation wrote to either stream. Both are captured because the
// decline is emitted from a hook that runs ahead of every command body, so it goes
// to stderr while the notes it is worded after go to stdout.
//
// Version is pinned to the source-build value for the duration, because the
// post-run update check reaches the network for any other value.
func runRootCapturing(t *testing.T, args ...string) (string, error) {
	t.Helper()
	origVersion := Version
	Version = "dev"
	t.Cleanup(func() { Version = origVersion })
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	var err error
	out := captureStdoutStderr(func() { err = rootCmd.Execute() })
	return out, err
}

// pinnedCDMURL puts the test in a fresh project directory whose .env carries the
// given assignments, read by the same loader the root command runs at startup —
// the route a checked-in .env takes, and the only route that records the
// provenance the decline turns on.
func pinnedCDMURL(t *testing.T, assignments string) string {
	t.Helper()
	dir := writeProjectEnv(t, t.TempDir(), assignments)
	t.Chdir(dir)
	loadProjectEnv(t, cdm.URLEnv, blocksBackendURLEnv, blocksAPIKeyEnv)
	if envFileSource(cdm.URLEnv) == "" {
		t.Fatalf("premise: a value read from the project .env must carry its provenance")
	}
	return dir
}

// exportedCDMURL points this invocation at a CDM endpoint the way a shell or a CI
// job does: set in the environment with no project .env behind it. It starts from
// an empty project directory so no earlier case's provenance can be mistaken for
// this one's.
func exportedCDMURL(t *testing.T, url string) {
	t.Helper()
	t.Chdir(t.TempDir())
	loadProjectEnv(t, cdm.URLEnv, blocksBackendURLEnv, blocksAPIKeyEnv)
	t.Setenv(cdm.URLEnv, url)
	if src := envFileSource(cdm.URLEnv); src != "" {
		t.Fatalf("premise: an exported value must carry no .env provenance, got %q", src)
	}
}

// recordingCDM serves a well-formed CDM payload and counts the requests it
// receives, so a case can assert that nothing in the invocation fetched it. The
// payload is valid on purpose: a server that only errored would let a fetch that
// did happen pass unnoticed.
func recordingCDM(t *testing.T, baseURL string) (url string, hits *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cdm.Config{Api: cdm.ApiConfig{BaseURL: baseURL, ClientID: "foreign-client"}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// seedAcmeProfile makes a deployment the active profile's own, so the effective
// target is named and a .env can be shown to disagree with it.
func seedAcmeProfile(t *testing.T) {
	t.Helper()
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      acmeDeployment,
		DefaultOrgID: "org-acme",
		Orgs:         map[string]profiles.OrgKey{"org-acme": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
}

// A BLOCKS_CDM_URL only a project .env supplies is not intent: that file arrives
// with a cloned repository. The CDM payload names the API origin a login falls
// back to, so following one that disagrees with the deployment being targeted
// hands the credential to whoever wrote the file. It is dropped, and the user is
// told what was dropped and how to ask for it deliberately.
func TestAProjectEnvCDMEndpointTheTargetDoesNotServeIsDeclined(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedAcmeProfile(t)
	pinnedCDMURL(t, cdm.URLEnv+"="+foreignCDM+"\n")

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	if got, ok := os.LookupEnv(cdm.URLEnv); ok {
		t.Errorf("%s = %q, want it removed from the environment entirely", cdm.URLEnv, got)
	}
	// Naming the variable, the file, and the deployment it disagrees with is what
	// makes the decline actionable; naming both ways to keep it is what stops a
	// legitimate override from looking impossible.
	for _, want := range []string{
		cdm.URLEnv, foreignCDM, "./.env",
		hostSlug(acmeDeployment),
		blocksBackendURLEnv,
		"export",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the decline must name %q:\n%s", want, out)
		}
	}
}

// The complement, and the reason this stays narrow: `blocks login --write-env`
// writes this variable into the project .env itself. In its own project directory
// that value names the CDM endpoint of the very deployment being targeted, so it
// displaces nothing and is followed without comment.
func TestAProjectEnvCDMEndpointForTheTargetItselfIsHonoured(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedAcmeProfile(t)
	want := cdm.EndpointFor(acmeDeployment)
	pinnedCDMURL(t, cdm.URLEnv+"="+want+"\n")

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	if got := os.Getenv(cdm.URLEnv); got != want {
		t.Errorf("%s = %q, want the target's own CDM endpoint %q preserved", cdm.URLEnv, got, want)
	}
	if strings.Contains(out, "Not using") {
		t.Errorf("a pin naming the target's own CDM endpoint must pass without comment:\n%s", out)
	}
}

// A value the shell or the CI job exported is the supported way to point a run at
// a specific CDM endpoint, and the CLI can tell it apart from a file's because the
// env loader never overwrites what the environment already carried. It wins even
// when it disagrees with the target — that disagreement is often the whole point.
func TestAnExportedCDMEndpointStillWinsOverTheTarget(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedAcmeProfile(t)
	exportedCDMURL(t, foreignCDM)

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	if got := os.Getenv(cdm.URLEnv); got != foreignCDM {
		t.Errorf("%s = %q, want the exported value %q left alone", cdm.URLEnv, got, foreignCDM)
	}
	if strings.Contains(out, "Not using") {
		t.Errorf("an exported value must not be declined:\n%s", out)
	}
}

// Stock Blocks Network has no per-deployment CDM endpoint — its config comes from
// the built-in default — so there is no file-sourced value that could legitimately
// name it, and every one is declined.
//
// This case also fixes the ordering, which is the whole fix: the config is fetched
// once and memoized for the process, so a decline that ran after the first fetch
// would change nothing. It drives a command that does resolve its origin through
// the CDM and asserts the endpoint the .env named was never contacted, while the
// origin the command used came from the local config file the CDM layer falls back
// to when the variable is absent — which is also the proof that this fix leaves
// that fallback path alone.
func TestAProjectEnvCDMEndpointIsDeclinedForStockNetworkBeforeAnythingFetchesIt(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	// A profile that records no deployment: nothing points this invocation
	// anywhere, which is the stock Blocks Network case.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	const localOrigin = "https://local-config.example.test"
	seedLocalCDMConfig(t, localOrigin)
	foreign, hits := recordingCDM(t, "https://collector.example.test")
	pinnedCDMURL(t, cdm.URLEnv+"="+foreign+"\n")

	var opened string
	openBrowserFunc = func(u string) error { opened = u; return nil }

	out, err := runRootCapturing(t, "dashboard")
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}

	if *hits != 0 {
		t.Errorf("the endpoint only the project .env named was fetched %d time(s); the decline must run before the config is resolved and memoized", *hits)
	}
	if got, ok := os.LookupEnv(cdm.URLEnv); ok {
		t.Errorf("%s = %q, want it removed for a target with no CDM endpoint of its own", cdm.URLEnv, got)
	}
	if !strings.HasPrefix(opened, localOrigin) {
		t.Errorf("dashboard opened %q, want an origin resolved from the local config file at %q", opened, localOrigin)
	}
	for _, want := range []string{cdm.URLEnv, foreign, "./.env", branding.Default()} {
		if !strings.Contains(out, want) {
			t.Errorf("the decline must name %q:\n%s", want, out)
		}
	}
}

// The variable is read twice over. Dropping it from this process is only half the
// fix: the environment handed to the delegated agent runtime is built from
// os.Environ(), so a value left in place would be resolved by the agent even on a
// command that never fetches the config itself.
func TestADeclinedCDMEndpointNeverReachesTheDelegatedRuntime(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deployment := newDeploymentServer(t, true)
	seedProfile(t, "enterprise", profiles.Profile{
		BaseURL:      deployment.url,
		Enterprise:   true,
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Org", ApiKey: "bk_ent"}},
	})
	pinnedCDMURL(t, cdm.URLEnv+"="+foreignCDM+"\n")

	if _, err := runRootCapturing(t, "version"); err != nil {
		t.Fatalf("version: %v", err)
	}

	env := buildChildEnv()
	for _, e := range env {
		if strings.Contains(e, foreignCDM) {
			t.Errorf("the declined endpoint was handed to the delegated runtime: %q", e)
		}
	}
	// Injecting nothing at all would be its own defect — the agent would fall back
	// to the public config and resolve the wrong keysets — so the child must still
	// receive the target deployment's own endpoint.
	if got, _ := childEnvValue(env, cdm.URLEnv); got != cdm.EndpointFor(deployment.url) {
		t.Errorf("%s = %q, want the target deployment's own CDM endpoint %q", cdm.URLEnv, got, cdm.EndpointFor(deployment.url))
	}
}

// seedLocalCDMConfig writes the on-disk config the CDM layer falls back to when no
// endpoint is named, and points the home directory at it so the real fallback runs
// without reaching the network.
func seedLocalCDMConfig(t *testing.T, apiBaseURL string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".blocks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.Marshal(cdm.Config{Api: cdm.ApiConfig{BaseURL: apiBaseURL, ClientID: "local-client"}})
	if err != nil {
		t.Fatalf("marshal local config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatalf("write local config: %v", err)
	}
}
