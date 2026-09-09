package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// These cases drive the real root command, because what is under test is a step in
// PersistentPreRun and its position within it. Calling the step directly would prove
// only that its comparison works, while the defect it closes is entirely a question
// of whether the value is gone before anything resolves a target or a credential.
//
// Each one asserts against a recording server rather than against printed text: the
// question is not whether the CLI complained, it is whether a byte of the credential
// ever left the process towards the host the file named.
//
// Three properties are held apart on purpose, because a rule that satisfies any two
// of them is easy to write and wrong: a pin naming an untrusted deployment must be
// declined, a pin naming a deployment the user logged in to must be honoured even
// while another profile is active, and a shell-exported value must always win.

// recordingDeployment answers every request 200 and records what it received, so a
// case can prove that nothing — not a config probe, not the request itself — reached
// it, and that no Authorization header was ever offered to it.
type recordingDeployment struct {
	url            string
	requests       []string
	authorizations []string
}

func newRecordingDeployment(t *testing.T, body string) *recordingDeployment {
	t.Helper()
	d := &recordingDeployment{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.requests = append(d.requests, r.Method+" "+r.URL.Path)
		d.authorizations = append(d.authorizations, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	d.url = srv.URL
	return d
}

// resetUnregisterFlags puts this command's flags back to their defaults afterwards.
// Cobra binds them to package variables and does not re-apply defaults between
// Execute calls, so a --yes or an --api-key left behind here would silently answer a
// confirmation, or supply a credential, in whatever test runs next.
func resetUnregisterFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		unregisterYes = false
		unregisterApiKey = ""
		unregisterApiKeyStdin = false
	})
}

// pinnedBackendProjectDir puts the test in a fresh project directory whose .env
// pins the given backend, loaded by the same loader the root command runs at
// startup. That route is the only one that records the provenance the decline turns
// on, so the pin is never written into the map by hand.
func pinnedBackendProjectDir(t *testing.T, url string, apiKey ...string) {
	t.Helper()
	assignments := blocksBackendURLEnv + "=" + url + "\n"
	// A key beside the pin is the `--write-env` shape: the active profile's stored key
	// belongs to its own deployment and clictx rightly withholds it from another, so a
	// project directory pinned elsewhere carries the key minted there too.
	if len(apiKey) > 0 && apiKey[0] != "" {
		assignments += blocksAPIKeyEnv + "=" + apiKey[0] + "\n"
	}
	t.Chdir(writeProjectEnv(t, t.TempDir(), assignments))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)
	if envFileSource(blocksBackendURLEnv) == "" {
		t.Fatal("premise: a value read from the project .env must carry its provenance")
	}
	if got := os.Getenv(blocksBackendURLEnv); got != url {
		t.Fatalf("premise: the .env must supply %s=%q, got %q", blocksBackendURLEnv, url, got)
	}
}

// The reproduction: a cloned repository's .env names a backend, and an explicit
// --api-key is handed to a command that authenticates. The key is the caller's, the
// host is the file's, and before the gate the CLI put the two together.
//
// The gate is global rather than per-command because every authenticated command
// reads the same variable. unregister is the case driven here — it is destructive as
// well as authenticated — but the fix sits in the hook that runs for all of them.
func TestAProjectEnvBackendPinNeverReceivesAnExplicitAPIKey(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	profileDeployment := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	hostile := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: profileDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	pinnedBackendProjectDir(t, hostile.url)

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_live_secret", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	// The assertion that matters: the host the file named was never contacted at
	// all, so it received neither the key nor the fact that an agent exists.
	if len(hostile.requests) != 0 {
		t.Errorf("the deployment only the project .env named received %v", hostile.requests)
	}
	for _, sent := range hostile.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}
	// And the resolver never observed the dropped value, which is what makes the
	// ordering — decline before Resolve — load-bearing rather than cosmetic.
	if got := clictx.ChosenBackendURL(); got != profileDeployment.url {
		t.Errorf("ChosenBackendURL() = %q, want the active profile's own deployment %q", got, profileDeployment.url)
	}
	if strings.Contains(clictx.BackendURL(), mustHost(t, hostile.url)) {
		t.Errorf("BackendURL() = %q still names the declined host", clictx.BackendURL())
	}
	if got, ok := os.LookupEnv(blocksBackendURLEnv); ok {
		t.Errorf("%s = %q, want it removed from the environment entirely so no later reader or child process resolves it", blocksBackendURLEnv, got)
	}
	// The removal still has to happen, against the deployment the profile records.
	if len(profileDeployment.requests) == 0 {
		t.Error("the command reached no deployment at all; declining a pin must fall back to the profile's own")
	}
	// Naming the variable, the file, and the deployment it disagrees with is what
	// makes the decline actionable; naming both ways to keep it is what stops a
	// legitimate override from looking impossible.
	for _, want := range []string{
		blocksBackendURLEnv, hostile.url, "./.env",
		hostSlug(profileDeployment.url),
		"blocks login",
		"export",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the decline must name %q:\n%s", want, out)
		}
	}
}

// The complement, and the reason the gate stays narrow: BLOCKS_BACKEND_URL plus
// BLOCKS_API_KEY exported by a shell or a CI job is the supported headless
// mechanism. An exported value is intent, the CLI can tell it from a file's because
// the env loader never overwrites what the environment already carried, and it must
// keep outranking the active profile — including when it disagrees with it, which is
// usually the whole point.
func TestAnExportedBackendURLAndKeyStillDriveHeadlessOperation(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	elsewhere := newRecordingDeployment(t, `{}`)
	exported := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: elsewhere.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	// An empty project directory, so nothing the environment carries has .env
	// provenance behind it.
	t.Chdir(t.TempDir())
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)
	t.Setenv(blocksBackendURLEnv, exported.url)
	t.Setenv(blocksAPIKeyEnv, "bk_scripted")
	if src := envFileSource(blocksBackendURLEnv); src != "" {
		t.Fatalf("premise: an exported value must carry no .env provenance, got %q", src)
	}

	out, err := runRootCapturing(t, "unregister", "my_agent", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if got := os.Getenv(blocksBackendURLEnv); got != exported.url {
		t.Fatalf("%s = %q, want the exported value %q left alone", blocksBackendURLEnv, got, exported.url)
	}
	if len(exported.requests) == 0 {
		t.Fatal("an exported backend URL must still decide where the request goes")
	}
	if got, want := exported.authorizations[len(exported.authorizations)-1], "Bearer bk_scripted"; got != want {
		t.Errorf("Authorization = %q, want %q — the scripted key must reach the exported deployment", got, want)
	}
	if len(elsewhere.requests) != 0 {
		t.Errorf("the request fell back to the profile's deployment (%v); the exported value must win", elsewhere.requests)
	}
	if strings.Contains(out, "Not using") {
		t.Errorf("an exported value must not be declined:\n%s", out)
	}
}

// The mechanism the decline must not break: `blocks login --write-env` leaves a pin
// in the directory it was run in, and that directory has to keep acting on its own
// deployment after `blocks profile use` selects a different one. The pin disagrees
// with the active profile — that is what it is for — and it is honoured because a
// profile for the deployment it names exists, which is the CLI's record that the user
// logged in there deliberately.
func TestAProjectEnvBackendPinForADeploymentTheUserLoggedInToIsHonoured(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	pinned := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	elsewhere := newRecordingDeployment(t, `{}`)
	seedProfile(t, "deployment-b", profiles.Profile{
		BaseURL:      pinned.url,
		DefaultOrgID: "org-b",
		Orgs:         map[string]profiles.OrgKey{"org-b": {OrgName: "Org B", ApiKey: "bk_b"}},
	})
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL:      elsewhere.url,
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_a"}},
	})
	// The trailing slash is deliberate: the comparison is profiles.SameBaseURL, not
	// string equality, and a pin differing only in punctuation from a profile's own
	// base URL must not be mistaken for a foreign one.
	pinnedBackendProjectDir(t, pinned.url+"/", "bk_b")

	out, err := runRootCapturing(t, "unregister", "my_agent", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if got := os.Getenv(blocksBackendURLEnv); got != pinned.url+"/" {
		t.Errorf("%s = %q, want the pin left in place", blocksBackendURLEnv, got)
	}
	if strings.Contains(out, "Not using") {
		t.Errorf("a pin naming a deployment the user has a profile for must pass without comment:\n%s", out)
	}
	if len(pinned.requests) == 0 {
		t.Error("the removal never reached the pinned deployment")
	}
	if len(elsewhere.requests) != 0 {
		t.Errorf("the removal went to the active profile's deployment (%v); the project pin must decide", elsewhere.requests)
	}
	// The banner has to name where the command actually acted, or the pin becomes a
	// silent redirection.
	if !strings.Contains(out, mustHost(t, pinned.url)) {
		t.Errorf("the banner must name the pinned deployment:\n%s", out)
	}
}

// A fresh install has no profiles at all, so no file-sourced pin can name a
// deployment the user has logged in to and every one is declined. This is the state a
// first-run `blocks register` in a cloned repository is in, and the one where a
// rule that only compared against the *active* profile would have had nothing to
// compare against.
func TestAProjectEnvBackendPinIsDeclinedWhenNoProfileNamesThatDeployment(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	hostile := newRecordingDeployment(t, `{}`)
	// The stock profile records no deployment of its own, so it vouches for nothing.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	pinnedBackendProjectDir(t, hostile.url)

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}

	if got, ok := os.LookupEnv(blocksBackendURLEnv); ok {
		t.Errorf("%s = %q, want it removed when no profile describes that deployment", blocksBackendURLEnv, got)
	}
	if got := clictx.ChosenBackendURL(); got != "" {
		t.Errorf("ChosenBackendURL() = %q, want nothing pointed at in particular", got)
	}
	if len(hostile.requests) != 0 {
		t.Errorf("the declined host was contacted: %v", hostile.requests)
	}
	if !strings.Contains(out, blocksBackendURLEnv) || !strings.Contains(out, hostile.url) {
		t.Errorf("the decline must name the variable and the value:\n%s", out)
	}
}

// Both gates read the same kind of evidence and must stay independent: dropping the
// backend pin must not stop the CDM pin from being judged, and the CDM decline is
// then judged against the deployment that actually remains the target.
func TestBothAmbientPinsAreDeclinedIndependently(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	hostile := newRecordingDeployment(t, `{}`)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: "https://blocks.acme.example.com",
		Orgs:    map[string]profiles.OrgKey{},
	})
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		blocksBackendURLEnv+"="+hostile.url+"\n"+
			"BLOCKS_CDM_URL="+foreignCDM+"\n"))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv, "BLOCKS_CDM_URL")

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}

	for _, key := range []string{blocksBackendURLEnv, "BLOCKS_CDM_URL"} {
		if got, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q, want both file-sourced pins removed", key, got)
		}
	}
	if !strings.Contains(out, hostile.url) || !strings.Contains(out, foreignCDM) {
		t.Errorf("both declines must be reported:\n%s", out)
	}
}
