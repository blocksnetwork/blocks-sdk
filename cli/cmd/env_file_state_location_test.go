package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// These cases cover the second half of what a project .env may not do: not only
// reconfigure the transport, but relocate the state the CLI trusts. The profile store
// is the CLI's record of which deployments the user deliberately logged in to, and
// every pin decline is decided against that record — so a file that can move the
// store can manufacture the evidence that makes a hostile pin look ordinary.
//
// They deliberately do not replace the store path functions with stubs of their own:
// the whole question is which path the production derivations produce, and a test that
// stubbed them would pass with the defect present. What they do instead is pin those
// variables to the production functions for their own duration — see productionPaths.

// productionContextsPath and productionCredentialPath hold the store path derivations
// as the packages ship them, captured during test-binary initialization, before any
// test can run.
//
// They exist because other tests in this package replace those variables and at least
// one pair of helpers restores them in the wrong order — a t.Cleanup that reinstates
// what a defer has already put back — leaving a stub behind that points into a temp
// directory the runtime has since deleted. A test whose subject *is* the production
// derivation cannot read the variable and hope: it has to hold the function itself.
var (
	productionContextsPath   = profiles.ContextsPathFunc
	productionCredentialPath = auth.CredentialPathFunc
)

// isolatedHome points the home directory — and so every store that falls back to it —
// at a fresh temp directory, and clears XDG_CONFIG_HOME so a project file's assignment
// of it is a live import attempt rather than something the loader skips because the
// environment already carries it. It returns the directory, which is where the CLI's
// real store is expected to stay.
//
// It also pins the two store path variables to the production derivations and puts
// back whatever it found afterwards, so neither a leaked stub nor this test decides
// what the other sees. Restoring the previous value rather than the production one is
// deliberate: repairing another test's leak here would hide it.
func isolatedHome(t *testing.T) string {
	t.Helper()
	priorContexts, priorCredential := profiles.ContextsPathFunc, auth.CredentialPathFunc
	profiles.ContextsPathFunc = productionContextsPath
	auth.CredentialPathFunc = productionCredentialPath
	t.Cleanup(func() {
		profiles.ContextsPathFunc = priorContexts
		auth.CredentialPathFunc = priorCredential
	})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	t.Setenv("XDG_CONFIG_HOME", "")
	os.Unsetenv("XDG_CONFIG_HOME")
	return home
}

// hostileProfileStore writes a contexts.json describing the given deployment into a
// directory a cloned repository could ship, and returns the value the repository's
// .env would set XDG_CONFIG_HOME to in order to make the CLI read it. The profile is
// well-formed on purpose: a malformed one would be rejected for the wrong reason.
func hostileProfileStore(t *testing.T, deploymentURL string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "blocks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	data, err := json.Marshal(profiles.Contexts{
		SchemaVersion: 3,
		Active:        "shipped-with-the-repository",
		Profiles: map[string]profiles.Profile{
			"shipped-with-the-repository": {
				BaseURL:      deploymentURL,
				DefaultOrgID: "org-attacker",
				Orgs:         map[string]profiles.OrgKey{"org-attacker": {OrgName: "Org", ApiKey: "bk_attacker"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal contexts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contexts.json"), data, 0600); err != nil {
		t.Fatalf("write contexts.json: %v", err)
	}
	return root
}

// The reproduction, one tier below the transport cases: XDG_CONFIG_HOME is read
// directly by the profile store, the legacy credential store and the deploy-plugin
// directory, so importing it from a project file hands a cloned repository the
// location of everything the CLI trusts.
func TestAProjectEnvCannotRelocateTheStoresTheCLITrusts(t *testing.T) {
	home := isolatedHome(t)
	hostileStore := hostileProfileStore(t, "https://backend.example.test")

	note := projectEnvWithoutImports(t, "XDG_CONFIG_HOME="+hostileStore+"\n", "XDG_CONFIG_HOME")

	if got := os.Getenv("XDG_CONFIG_HOME"); got != "" {
		t.Errorf("XDG_CONFIG_HOME = %q, want it never imported from a project .env", got)
	}
	// The property that matters is not the variable's value but the path the stores
	// derive from it, so both are asked of the real path functions.
	for name, pathFunc := range map[string]func() (string, error){
		"profile store":    profiles.ContextsPathFunc,
		"credential store": auth.CredentialPathFunc,
	} {
		path, err := pathFunc()
		if err != nil {
			t.Fatalf("%s path: %v", name, err)
		}
		if !strings.HasPrefix(path, home) {
			t.Errorf("%s resolved to %q, want it under the real home %q", name, path, home)
		}
		if strings.HasPrefix(path, hostileStore) {
			t.Errorf("%s resolved into the directory the project file named: %q", name, path)
		}
	}
	// And the profile the fake store contains is not visible, which is what stops it
	// from vouching for a deployment the user never logged in to. The store is read
	// through the same gate the root command reads it through, so the case cannot pass
	// by handing knownDeployment a store nothing in production would produce.
	store, err := loadProfileStore()
	if err != nil {
		t.Fatalf("load profile store: %v", err)
	}
	if knownDeployment(store, "https://backend.example.test") {
		t.Error("a profile store the project .env pointed at was read as if the user had logged in to it")
	}
	for _, want := range []string{"XDG_CONFIG_HOME", "./.env", "export"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
}

// The chain the two refusals close together, and the reason XDG_CONFIG_HOME belongs
// with the proxy variables rather than being treated as a lesser cousin of them: a
// repository that ships both a profile store naming its own backend and a pin naming
// the same backend needs no interception at all. The pin passes because the store the
// repository shipped says the user logged in there, and the credential follows.
func TestAShippedProfileStoreCannotVouchForAProjectEnvBackendPin(t *testing.T) {
	restoreCLIState(t)
	isolateAmbientState(t)
	resetUnregisterFlags(t)
	isolatedHome(t)

	hostile := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	trusted := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	// Seeded through the real store, which now lives under the isolated home.
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: trusted.url,
		Orgs:    map[string]profiles.OrgKey{},
	})

	hostileStore := hostileProfileStore(t, hostile.url)
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		"XDG_CONFIG_HOME="+hostileStore+"\n"+
			blocksBackendURLEnv+"="+hostile.url+"\n"))
	loadProjectEnv(t, "XDG_CONFIG_HOME", blocksBackendURLEnv, blocksAPIKeyEnv)

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_live_secret", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if len(hostile.requests) != 0 {
		t.Errorf("the deployment the cloned repository named received %v", hostile.requests)
	}
	for _, sent := range hostile.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}
	if got, ok := os.LookupEnv(blocksBackendURLEnv); ok {
		t.Errorf("%s = %q, want the pin declined even though the shipped store described it", blocksBackendURLEnv, got)
	}
	if got, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		t.Errorf("XDG_CONFIG_HOME = %q, want it refused", got)
	}
	if len(trusted.requests) == 0 {
		t.Error("the command reached no deployment at all; declining a pin must fall back to the profile's own")
	}
	// Only the pin decline is expected here: the refusal note is written by the load,
	// which in production happens in init() — before any command body, and so before
	// this capture starts. Its wording is asserted where the load itself is driven.
	for _, want := range []string{blocksBackendURLEnv, hostile.url} {
		if !strings.Contains(out, want) {
			t.Errorf("the output must name %q:\n%s", want, out)
		}
	}
}
