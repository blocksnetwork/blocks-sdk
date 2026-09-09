package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// These cases are about one thing: which remote configuration answers for a login
// that names its instance outright. The OAuth client id lives in that configuration
// and is only reached when the named deployment's own discovery reports none, so
// every fixture here arranges exactly that — otherwise which endpoint was in effect
// leaves no observable trace at all.

const (
	// clientIDOfA is reported only by deployment A's own remote configuration. Seeing
	// it presented to some other deployment is the defect these cases pin.
	clientIDOfA = "oauth-client-of-deployment-a"
	// fallbackClientID is what this build resolves for itself once a foreign endpoint
	// has been dropped: the cached configuration under the isolated HOME.
	fallbackClientID = "oauth-client-of-this-build"
)

// deploymentServingItsOwnCDM answers discovery, org resolution and its own CDM
// endpoint. Serving all three from one origin is what lets a case pin BLOCKS_CDM_URL
// to cdm.EndpointFor(url) — the value `blocks login --write-env` writes — so the pin
// is the ordinary one a project directory carries rather than some unrelated host the
// root command's own decline would drop for a different reason.
//
// Its discovery deliberately reports no oauthClientId, because that is the case in
// which the CLI falls back to remote config for the client id.
type deploymentServingItsOwnCDM struct {
	url      string
	clientID string

	mu         sync.Mutex
	cdmFetches int
	paths      []string
}

func newDeploymentServingItsOwnCDM(t *testing.T, clientID string) *deploymentServingItsOwnCDM {
	t.Helper()
	d := &deploymentServingItsOwnCDM{clientID: clientID}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.paths = append(d.paths, r.URL.Path)
		if r.URL.Path == cdmEndpointPath {
			d.cdmFetches++
		}
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case cdmEndpointPath:
			json.NewEncoder(w).Encode(cdm.Config{
				Playground: cdm.Keyset{PublishKey: "demo", SubscribeKey: "demo"},
				Network:    cdm.Keyset{PublishKey: "demo", SubscribeKey: "demo"},
				Api:        cdm.ApiConfig{BaseURL: d.url, ClientID: d.clientID},
			})
		case "/api/v1/cli-config":
			w.Write([]byte(`{"enterprise":true,"productName":"A Corp"}`))
		case "/api/v1/registry/publish-context":
			// agentCount > 0 keeps the first-publish org-name prompt out of the way.
			w.Write([]byte(`{"orgId":"org-a","orgName":"Org A","agentCount":3}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	// Resolved before the server starts serving, so the handler never reads a field
	// the test goroutine is still writing.
	d.url = "http://" + srv.Listener.Addr().String()
	srv.Start()
	t.Cleanup(srv.Close)
	if srv.URL != d.url {
		t.Fatalf("server URL = %q, want %q", srv.URL, d.url)
	}
	return d
}

// cdmEndpointPath is the route a deployment serves its own CDM payload on, derived
// from cdm.EndpointFor rather than spelled out, so this fixture cannot disagree with
// the production mapping it stands in for.
var cdmEndpointPath = cdm.EndpointFor("https://x")[len("https://x"):]

func (d *deploymentServingItsOwnCDM) fetchesOfItsCDM() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cdmFetches
}

func (d *deploymentServingItsOwnCDM) requested() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.paths...)
}

// buildDefaultRemoteConfigAt lays down this build's own cached remote configuration
// under an isolated HOME. That is the tier cdm.Get falls back to with no
// BLOCKS_CDM_URL set, so it is what answers once a foreign endpoint has been dropped
// — and isolating HOME is what keeps the fallback away from the developer's own
// cache, and so away from the real deployment it names.
func buildDefaultRemoteConfigAt(t *testing.T, apiBaseURL, clientID string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	dir := filepath.Join(home, ".blocks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body, err := json.Marshal(cdm.Config{
		Playground: cdm.Keyset{PublishKey: "demo", SubscribeKey: "demo"},
		Network:    cdm.Keyset{PublishKey: "demo", SubscribeKey: "demo"},
		Api:        cdm.ApiConfig{BaseURL: apiBaseURL, ClientID: clientID},
	})
	if err != nil {
		t.Fatalf("marshal remote config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	cdm.Reset()
	t.Cleanup(cdm.Reset)
}

// pinnedProjectFor puts the test in the directory `blocks login <a> --write-env`
// leaves behind: a project .env pinning that deployment and its own CDM endpoint,
// read by the same loader the root command runs at startup so both values carry the
// provenance the pin declines turn on, and a saved profile for it so both pins are
// honoured rather than declined as foreign.
func pinnedProjectFor(t *testing.T, a *deploymentServingItsOwnCDM) {
	t.Helper()
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL:    a.url,
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	// Emptied so the client id has to be resolved from remote config, which is the
	// only tier that can differ between deployments.
	t.Setenv("BLOCKS_CLI_CLIENT_ID", "")
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv(blocksAPIKeyEnv, "")

	dir := writeProjectEnv(t, t.TempDir(),
		blocksBackendURLEnv+"="+a.url+"\n"+cdm.URLEnv+"="+cdm.EndpointFor(a.url)+"\n")
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv, cdm.URLEnv)

	if envFileSource(cdm.URLEnv) == "" {
		t.Fatalf("premise: a value read from the project .env must carry its provenance")
	}
	if got := os.Getenv(cdm.URLEnv); got != cdm.EndpointFor(a.url) {
		t.Fatalf("premise: %s = %q, want deployment A's own endpoint %q", cdm.URLEnv, got, cdm.EndpointFor(a.url))
	}
	if got := os.Getenv(blocksBackendURLEnv); got != a.url {
		t.Fatalf("premise: %s = %q, want deployment A %q", blocksBackendURLEnv, got, a.url)
	}
}

// resolvedLoginClientID runs the three steps loginToProfile runs, in its order:
// resolve the backend for the choice, discover it, then resolve the OAuth client id
// from what discovery reported. Going through the production functions is the point —
// the client id a login presents is whatever this sequence yields.
func resolvedLoginClientID(t *testing.T, choice deploymentChoice, wantBackend string) string {
	t.Helper()
	var clientID string
	var err error
	captureStdoutStderr(func() {
		backend := resolveLoginBackend(choice)
		if backend != wantBackend {
			err = fmt.Errorf("resolveLoginBackend = %q, want %q", backend, wantBackend)
			return
		}
		var disco *cliconfig.Config
		if disco, err = cliconfig.Fetch(backend); err != nil {
			return
		}
		if disco.OAuthClientID != "" {
			err = fmt.Errorf("premise: the target must report no oauthClientId of its own, got %q", disco.OAuthClientID)
			return
		}
		clientID = resolveLoginClientID(disco)
	})
	if err != nil {
		t.Fatal(err)
	}
	return clientID
}

// A project directory pinned to deployment A carries A's own CDM endpoint in its
// .env, and both pins are honoured for every command run there. `blocks login <B>`
// names B outright, so nothing ambient may still describe the target — but the CDM
// endpoint still did: the payload it serves carries the OAuth client id the login
// falls back to when the named deployment's discovery reports none, so B was handed
// A's. The --network path already closed the same hole; this is the other half of it.
//
// The assertion is at the socket, on A's CDM endpoint, because that endpoint is the
// only route to A's client id — a login that never fetches it cannot be using it.
func TestAnInstanceLoginIgnoresACDMEndpointPinnedForAnotherDeployment(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	a := newDeploymentServingItsOwnCDM(t, clientIDOfA)
	b := newDeploymentServer(t, true)
	buildDefaultRemoteConfigAt(t, "https://api.blocks.example", fallbackClientID)
	pinnedProjectFor(t, a)

	runLoginArgs(t, "login", b.url, "--api-key", "bk_minted_at_b", "--no-write-env")

	if !b.authenticated {
		t.Fatalf("the login never authenticated against the instance it named; deployment A was asked for %v", a.requested())
	}
	if got := a.fetchesOfItsCDM(); got != 0 {
		t.Errorf("deployment A's CDM endpoint was fetched %d time(s); a named instance must not resolve its OAuth client id through another deployment", got)
	}
	if got, ok := os.LookupEnv(cdm.URLEnv); ok {
		t.Errorf("%s = %q, want it removed from the environment entirely — a delegated runtime inherits it", cdm.URLEnv, got)
	}
	profile := mustHost(t, b.url)
	if got := orgKeyOf(t, mustLoadProfiles(t), profile); got != "bk_minted_at_b" {
		t.Errorf("profile %s key = %q, want the key minted at the named instance", profile, got)
	}
}

// The same case asserted on the value rather than at the socket: the client id the
// login would present to the instance it named must be this build's own, never the
// one belonging to the deployment the directory happens to be pinned to.
func TestAnInstanceLoginDoesNotPresentAnotherDeploymentsOAuthClientID(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	a := newDeploymentServingItsOwnCDM(t, clientIDOfA)
	b := newDeploymentServer(t, true)
	buildDefaultRemoteConfigAt(t, "https://api.blocks.example", fallbackClientID)
	pinnedProjectFor(t, a)
	resolveCLIContext(t, loginCmd)

	got := resolvedLoginClientID(t, deploymentChoice{instanceURL: b.url}, b.url)

	if got == clientIDOfA {
		t.Fatalf("the login presents deployment A's OAuth client id (%q) to the instance it named", got)
	}
	if got != fallbackClientID {
		t.Errorf("client id = %q, want this build's own %q", got, fallbackClientID)
	}
	if n := a.fetchesOfItsCDM(); n != 0 {
		t.Errorf("deployment A's CDM endpoint was fetched %d time(s)", n)
	}
}

// The legitimate case the drop must not break: the pin and the instance argument name
// the same deployment. That is `blocks login <A> --write-env` re-run in its own
// project directory, where the pinned endpoint is A's own and does answer for the
// target — so it has to be followed, and left in place for the runtime that inherits
// it.
func TestAnInstanceLoginStillFollowsTheCDMPinForItsOwnDeployment(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	a := newDeploymentServingItsOwnCDM(t, clientIDOfA)
	buildDefaultRemoteConfigAt(t, "https://api.blocks.example", fallbackClientID)
	pinnedProjectFor(t, a)
	resolveCLIContext(t, loginCmd)

	got := resolvedLoginClientID(t, deploymentChoice{instanceURL: a.url}, a.url)

	if got != clientIDOfA {
		t.Errorf("client id = %q, want deployment A's own %q — a pin naming the deployment being logged in to must be followed", got, clientIDOfA)
	}
	if n := a.fetchesOfItsCDM(); n == 0 {
		t.Error("the pinned endpoint was never fetched, so the pin was not followed")
	}
	if want := cdm.EndpointFor(a.url); os.Getenv(cdm.URLEnv) != want {
		t.Errorf("%s = %q, want the pin left in place (%q)", cdm.URLEnv, os.Getenv(cdm.URLEnv), want)
	}
}
