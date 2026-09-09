package cmd

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// restoreCLIState puts back every package-level override these tests move: the
// profile selection, the login flags and prompt mode, the shared stdin scanner,
// the TTY answers, branding, and the resolved context. Without it a test that
// forces isTTY, selects a profile, or leaves a flag set changes the outcome of
// whatever runs next in the same binary.
func restoreCLIState(t *testing.T) {
	t.Helper()
	origRootProfile := rootProfile
	origScanner := stdinScanner
	origIsTTY := isTTY
	origIsInteractive := isInteractive
	origOpenBrowser := openBrowserFunc
	t.Cleanup(func() {
		rootProfile = origRootProfile
		profiles.SetActiveOverride("")
		stdinScanner = origScanner
		isTTY = origIsTTY
		isInteractive = origIsInteractive
		openBrowserFunc = origOpenBrowser
		branding.Reset()
		resetLoginFlags()
		clictx.Reset()
		clictx.Resolve(nil)
	})
}

// writeProjectEnv creates a project .env in dir and returns the directory.
func writeProjectEnv(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	return dir
}

// loadProjectEnv runs the same .env load the root command runs at startup and
// restores the named keys afterwards, so a test can exercise the real "project
// .env pins a deployment" path without leaking the pin into the rest of the
// binary. The keys are cleared first because the load deliberately skips a
// variable that is already set, and a developer's own .env may have set it.
//
// It also restores the record of which variables came from a .env, since the load
// replaces it: leaving one test's provenance in place would make the next test's
// notes name a file it never read.
func loadProjectEnv(t *testing.T, keys ...string) {
	t.Helper()
	prior := make(map[string]*string, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			prior[k] = &v
		} else {
			prior[k] = nil
		}
		os.Unsetenv(k)
	}
	priorSources := maps.Clone(envFileKeys)
	t.Cleanup(func() {
		for k, v := range prior {
			if v == nil {
				os.Unsetenv(k)
				continue
			}
			os.Setenv(k, *v)
		}
		envFileKeys = priorSources
	})
	loadEnvFile(".env")
}

// pinnedProjectDir puts the test in a fresh project directory whose .env carries
// the given assignments, and loads it exactly as the root command does at startup
// — so the pin reaches the resolver by the same route a user's leftover .env does,
// and its provenance is recorded the same way.
func pinnedProjectDir(t *testing.T, env string) string {
	t.Helper()
	dir := writeProjectEnv(t, t.TempDir(), env)
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)
	return dir
}

// seedProfile writes one profile into the isolated store and makes it active.
func seedProfile(t *testing.T, name string, p profiles.Profile) {
	t.Helper()
	if err := profiles.Upsert(name, p, true); err != nil {
		t.Fatalf("seed profile %s: %v", name, err)
	}
}

// deploymentAProfile is an enterprise profile for one deployment, holding a key
// for that deployment's org. Tests pair it with a project .env that points
// somewhere else entirely.
func deploymentAProfile() profiles.Profile {
	return profiles.Profile{
		BaseURL:      "https://a.blocks.example",
		Enterprise:   true,
		ProductName:  "A Corp",
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_a"}},
	}
}

func TestBannerIgnoresTheActiveProfileWhenTheProjectEnvPointsElsewhere(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	seedProfile(t, "deployment-a", deploymentAProfile())

	// What `blocks login --write-env` leaves in another project: a pinned backend
	// and a key minted there. Both outrank the active profile at request time, and
	// the pin is honoured because a profile for that deployment exists — the user
	// logged in to it — even though a different profile is active.
	seedProfile(t, "deployment-b", profiles.Profile{
		BaseURL: "https://b.blocks.example",
		Orgs:    map[string]profiles.OrgKey{},
	})
	profiles.SetActive("deployment-a")
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		"BLOCKS_BACKEND_URL=https://b.blocks.example\nBLOCKS_API_KEY=bk_b\n"))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)

	_ = rootCmd.PersistentPreRunE(unregisterCmd, nil)

	banner := clictx.Banner()
	if strings.Contains(banner, "Org A") {
		t.Errorf("banner %q attributes the active profile's org to a key from another deployment", banner)
	}
	if strings.Contains(banner, "deployment-a") || strings.Contains(banner, "a.blocks.example") {
		t.Errorf("banner %q names the deployment the profile records, not the one being called", banner)
	}
	if !strings.Contains(banner, "b.blocks.example") {
		t.Errorf("banner %q should name the pinned backend the request will reach", banner)
	}
}

func TestUnregisterDeletesFromThePinnedDeploymentAndTheBannerAgrees(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	var gotMethod, gotHost string
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotHost = r.Method, r.Host
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"agentName":"my_agent","status":"deleted"}`))
	}))
	t.Cleanup(srvB.Close)

	seedProfile(t, "deployment-a", deploymentAProfile())
	// A profile for the pinned deployment exists, so the project pin is honoured:
	// this is the `blocks login --write-env` shape, where a project directory keeps
	// targeting the deployment it was set up for after `blocks profile use` selects
	// another.
	seedProfile(t, "deployment-b", profiles.Profile{BaseURL: srvB.URL, Orgs: map[string]profiles.OrgKey{}})
	profiles.SetActive("deployment-a")
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		"BLOCKS_BACKEND_URL="+srvB.URL+"\nBLOCKS_API_KEY=bk_b\n"))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)

	_ = rootCmd.PersistentPreRunE(unregisterCmd, nil)
	unregisterYes = true
	t.Cleanup(func() { unregisterYes = false })

	out := captureStdout(func() {
		if err := runUnregister(t.Context(), []string{"my_agent"}); err != nil {
			t.Fatalf("runUnregister: %v", err)
		}
	})

	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE — the removal never reached the pinned deployment", gotMethod)
	}
	wantHost := mustHost(t, srvB.URL)
	if gotHost != wantHost {
		t.Errorf("DELETE host = %q, want %q", gotHost, wantHost)
	}
	if !strings.Contains(out, wantHost) {
		t.Errorf("banner must name the deployment the DELETE went to (%s):\n%s", wantHost, out)
	}
	if strings.Contains(out, "Org A") || strings.Contains(out, "deployment-a") {
		t.Errorf("banner names a deployment/org the removal did not use:\n%s", out)
	}
}

func TestLoginWithProfileAliasStoresUnderTheAlias(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	// The test server stands in for the alias's custom domain: the alias must be
	// reachable, since login fails fast on an instance it cannot reach.
	srv := loginTestServer(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: srv.URL, Orgs: map[string]profiles.OrgKey{}})

	runLoginArgs(t, "login", "acme", "--api-key", "bk_alias", "--no-write-env")

	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if got := orgKeyOf(t, store, "acme"); got != "bk_alias" {
		t.Errorf("profile acme key = %q, want bk_alias — the login did not write back into the alias", got)
	}
	hostName := mustHost(t, srv.URL)
	if _, ok := store.Profiles[hostName]; ok {
		t.Errorf("a duplicate profile %q was created; the alias must be reused", hostName)
	}
	if store.Active != "acme" {
		t.Errorf("active profile = %q, want acme", store.Active)
	}
}

func TestLoginProfileFlagStillWinsOverAMatchedAlias(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	srv := loginTestServer(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: srv.URL, Orgs: map[string]profiles.OrgKey{}})

	runLoginArgs(t, "login", "--profile", "foo", "acme", "--api-key", "bk_foo", "--no-write-env")

	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if got := orgKeyOf(t, store, "foo"); got != "bk_foo" {
		t.Errorf("profile foo key = %q, want bk_foo — an explicit --profile must win", got)
	}
	if _, ok := store.Profiles["acme"].Orgs["org-login"]; ok {
		t.Error("the matched alias must not receive the credential when --profile names another profile")
	}
}

func TestLoginNetworkFlagStoresUnderTheDefaultProfile(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	// An enterprise profile is active and no instance is named: only --network
	// says where this login belongs.
	srv := loginTestServer(t)
	seedProfile(t, "umbrella.blocks.ai", profiles.Profile{
		BaseURL:      "https://umbrella.blocks.ai",
		Enterprise:   true,
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Engineering", ApiKey: "bk_enterprise"}},
	})
	// Blocks Network's own remote config, pointed at the test server. It cannot be
	// pointed there with BLOCKS_CDM_URL: an explicit Network login drops that
	// variable rather than let it name the deployment it authenticates against.
	networkRemoteConfigAt(t, srv.URL)

	runLoginArgs(t, "login", "--network", "--api-key", "bk_net", "--no-write-env")

	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if got := orgKeyOf(t, store, profiles.DefaultProfile); got != "bk_net" {
		t.Errorf("profile %s key = %q, want bk_net", profiles.DefaultProfile, got)
	}
	if got := store.Profiles["umbrella.blocks.ai"].Orgs["org-1"].ApiKey; got != "bk_enterprise" {
		t.Errorf("the enterprise profile's key changed to %q; --network must not write into it", got)
	}
}

func TestLoginShortNameNamesTheProfileAfterTheHost(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	// No local profile is named "acme", so the argument expands to the default
	// deployment scheme and the host names the destination profile.
	instanceURL, matched := resolveInstanceArg("acme")
	if want := "https://acme.blocks.ai"; instanceURL != want {
		t.Fatalf("resolveInstanceArg(\"acme\") url = %q, want %q", instanceURL, want)
	}
	if matched != "" {
		t.Errorf("resolveInstanceArg(\"acme\") matched profile = %q, want none", matched)
	}
	got := resolveProfileName(deploymentChoice{instanceURL: instanceURL, profileName: matched})
	if want := "acme.blocks.ai"; got != want {
		t.Fatalf("resolveProfileName = %q, want %q", got, want)
	}
}

func TestLoginWithoutAMatchingProfileStoresUnderTheHostSlugWithBaseURL(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	srv := loginTestServer(t)

	runLoginArgs(t, "login", srv.URL, "--api-key", "bk_host", "--no-write-env")

	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	hostName := mustHost(t, srv.URL)
	p, ok := store.Profiles[hostName]
	if !ok {
		t.Fatalf("no profile named after the host %q; have %v", hostName, profileNames(store))
	}
	if got := orgKeyOf(t, store, hostName); got != "bk_host" {
		t.Errorf("profile %s key = %q, want bk_host", hostName, got)
	}
	if p.BaseURL != srv.URL {
		t.Errorf("profile %s base_url = %q, want %q", hostName, p.BaseURL, srv.URL)
	}
}

// runLoginArgs runs the login command through the root command, so the argument
// resolution, profile naming and persistence all run exactly as in production.
func runLoginArgs(t *testing.T, args ...string) {
	t.Helper()
	resetLoginFlags()
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	captureStdout(func() {
		if err := rootCmd.Execute(); err != nil {
			t.Errorf("%v failed: %v", args, err)
		}
	})
}

// isolateCredentials points the legacy credentials.json at a temp file so the
// developer's real one is neither read (profile-store migration) nor written.
func isolateCredentials(t *testing.T) {
	t.Helper()
	orig := auth.CredentialPathFunc
	path := filepath.Join(t.TempDir(), "credentials.json")
	auth.CredentialPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { auth.CredentialPathFunc = orig })
}

// orgKeyOf returns the named profile's default-org API key.
func orgKeyOf(t *testing.T, store *profiles.Contexts, name string) string {
	t.Helper()
	p, ok := store.Profiles[name]
	if !ok {
		t.Fatalf("no profile %q; have %v", name, profileNames(store))
	}
	k, ok := p.DefaultOrgKey()
	if !ok {
		t.Fatalf("profile %q has no default org key", name)
	}
	return k.ApiKey
}

func profileNames(store *profiles.Contexts) []string {
	names := make([]string, 0, len(store.Profiles))
	for n := range store.Profiles {
		names = append(names, n)
	}
	return names
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		t.Fatalf("cannot parse host out of %q: %v", rawURL, err)
	}
	return u.Host
}
