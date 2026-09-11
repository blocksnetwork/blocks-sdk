package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/pflag"
)

// deploymentServer stands in for a Blocks deployment. It answers the two calls a
// login makes — discovery and org resolution — and records what it was asked for,
// so a test can tell which deployment a command actually talked to.
type deploymentServer struct {
	url string
	// authenticated is true once the server resolved an org for a key, which is the
	// step that makes it the deployment the login authenticated against.
	authenticated bool
	// authorizations holds the Authorization header of every request it received.
	authorizations []string
	// requestsTo counts requests per path.
	requestsTo map[string]int
}

func newDeploymentServer(t *testing.T, enterprise bool) *deploymentServer {
	t.Helper()
	d := &deploymentServer{requestsTo: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.requestsTo[r.URL.Path]++
		d.authorizations = append(d.authorizations, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/v1/cli-config":
			w.WriteHeader(http.StatusOK)
			if enterprise {
				w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
				return
			}
			w.Write([]byte(`{"enterprise":false,"productName":"Blocks Network"}`))
		case "/api/v1/registry/publish-context":
			d.authenticated = true
			w.WriteHeader(http.StatusOK)
			// agentCount > 0 keeps the first-publish org-name prompt out of the way.
			w.Write([]byte(`{"orgId":"org-1","orgName":"Resolved Org","agentCount":3}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	d.url = srv.URL
	return d
}

// isolateAmbientState neutralises everything outside the test that could name a
// deployment or a credential: remote config, the OAuth client id, and the
// developer's own environment.
func isolateAmbientState(t *testing.T) {
	t.Helper()
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	t.Setenv("BLOCKS_CLI_CLIENT_ID", "test-client-id")
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_API_KEY", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	cdm.Reset()
	t.Cleanup(cdm.Reset)
}

// forceTTY makes the session look interactive, which is what lets a test show that
// --no-input, not TTY state, is what suppresses a prompt.
func forceTTY(t *testing.T) {
	t.Helper()
	origIsTTY, origIsInteractive := isTTY, isInteractive
	isTTY = func() bool { return true }
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isTTY, isInteractive = origIsTTY, origIsInteractive })
}

// pipeStdin replaces os.Stdin with a pipe holding content. It exists so a test can
// give a prompt an answer it would accept, and then assert that the prompt never
// ran anyway.
func pipeStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })
}

// resetNoInput restores the --no-input flag state after the test, which is
// process-wide and otherwise leaks into every prompt test that runs after this one.
// What "the state" consists of is resetNoInputState's business — one owner, because
// a second list of the pieces is how a partial reset gets reintroduced.
func resetNoInput(t *testing.T) {
	t.Helper()
	t.Cleanup(resetNoInputState)
}

// envValue reads one assignment out of a .env file.
func envValue(t *testing.T, dir, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return v
		}
	}
	return ""
}

// The backend a login authenticates against and the backend it pins into .env are
// the same decision, so they must resolve to the same value. Here an ambient
// backend URL points at deployment B while the active profile records deployment
// A: the login must reach B and pin B, because a key minted at one deployment
// beside the URL of another is a credential every later command sends to the wrong
// place.
func TestLoginPinsTheDeploymentItAuthenticatedAgainst(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deploymentA := newDeploymentServer(t, false)
	deploymentB := newDeploymentServer(t, false)
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: deploymentA.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	t.Setenv(blocksBackendURLEnv, deploymentB.url)

	projectDir := t.TempDir()
	runLoginArgs(t, "login", "--api-key", "bk_minted_at_b", "--write-env", "--dir", projectDir)

	if deploymentA.authenticated {
		t.Error("the login authenticated against the profile's deployment, not the one it was pointed at")
	}
	if !deploymentB.authenticated {
		t.Fatal("the login never reached the deployment the ambient backend URL names")
	}

	pin := envValue(t, projectDir, blocksBackendURLEnv)
	if pin != deploymentB.url {
		t.Errorf(".env pins %q, want the deployment the login authenticated against (%q)", pin, deploymentB.url)
	}
	if got := envValue(t, projectDir, blocksAPIKeyEnv); got != "bk_minted_at_b" {
		t.Errorf(".env key = %q, want bk_minted_at_b", got)
	}
}

// An API key in the environment outranks the profile's cached key at request time,
// and the context banner has to agree with the request: sending the profile's key
// while the banner describes an ambient one leaves the user with no way to tell what
// the command acted as. Nothing in the banner may be attributed to the profile the
// ambient key displaced — which for an invitation means no organization at all, since
// the deployment authorizes a send by ownership of the named agent rather than by the
// organization the key belongs to.
func TestInviteSendsTheAmbientCredentialTheBannerDescribes(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deployment := newDeploymentServer(t, false)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      deployment.url,
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_profile"}},
	})
	t.Setenv(blocksBackendURLEnv, deployment.url)
	t.Setenv(blocksAPIKeyEnv, "bk_from_env")

	t.Cleanup(func() {
		inviteSendEmail = ""
		inviteSendCmd.Flags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
	})

	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"invite", "send", "my_agent", "--email", "invitee@example.com"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("invite send: %v", err)
		}
	})

	var sent string
	for _, auth := range deployment.authorizations {
		if auth != "" {
			sent = auth
		}
	}
	if sent != "Bearer bk_from_env" {
		t.Errorf("invite sent %q, want the ambient key the banner accounts for", sent)
	}
	if strings.Contains(out, "Org A") {
		t.Errorf("banner attributes the profile's org to a key from the environment:\n%s", out)
	}
	if strings.Contains(out, "organization") {
		t.Errorf("banner raises an organization that does not scope the invitation:\n%s", out)
	}
	if want := "[acme / agent my_agent → invitee@example.com]"; !strings.Contains(out, want) {
		t.Errorf("banner must name the deployment and the subject the invitation acts on (%s):\n%s", want, out)
	}
}

// --no-input means never prompt, whatever stdin happens to be. stdin here holds a
// valid answer to the billing prompt, so a publish that still prompts would sail
// through and POST; the flag must turn that into an error naming the flag instead.
func TestNoInputPublishFailsInsteadOfPromptingOnATTY(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	forceTTY(t)
	resetNoInput(t)

	deployment := newDeploymentServer(t, false)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      deployment.url,
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_profile"}},
	})
	t.Setenv(blocksBackendURLEnv, deployment.url)

	// "1" selects Free at the billing prompt: an answer a prompting publish accepts.
	pipeStdin(t, "1\n")

	cardPath := filepath.Join(writeValidProject(t), "agent-card.json")
	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

	var publishErr error
	captureStdout(func() {
		rootCmd.SetArgs([]string{"--no-input", "publish", cardPath, "--listing", "public"})
		publishErr = rootCmd.Execute()
	})

	if publishErr == nil {
		t.Fatal("publish must fail rather than prompt when --no-input is set")
	}
	if !strings.Contains(publishErr.Error(), "--billing-mode") {
		t.Errorf("error must name the flag that answers the prompt, got: %v", publishErr)
	}
	if n := deployment.requestsTo["/api/v1/registry/agents"]; n != 0 {
		t.Errorf("%d publish request(s) reached the registry; want 0", n)
	}
}

// The enterprise verdict gates request behaviour — it forces free billing and
// suppresses the marketplace prompts — so it may not be read off a profile an
// ambient backend URL has displaced. Here an enterprise profile is active while
// the request goes to a deployment that reports itself non-enterprise.
func TestEnterpriseIsFalseWhenAnAmbientBackendDisplacesTheProfile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deployment := newDeploymentServer(t, false)
	seedProfile(t, "umbrella.blocks.ai", profiles.Profile{
		BaseURL:      "https://umbrella.blocks.ai",
		Enterprise:   true,
		ProductName:  "Umbrella Corporation",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Engineering", ApiKey: "bk_enterprise"}},
	})
	t.Setenv(blocksBackendURLEnv, deployment.url)
	t.Setenv(blocksAPIKeyEnv, "bk_from_env")

	t.Chdir(writeValidProject(t))
	resolveCLIContext(t, publishCmd)

	if clictx.Enterprise() {
		t.Fatal("Enterprise() must describe the deployment being called, not the displaced profile")
	}
	if ov := enterpriseBillingOverride(clictx.Enterprise()); ov != nil {
		t.Errorf("billing was forced to %q on a deployment that has a marketplace", *ov)
	}

	prep, err := captureStdoutErr(t, func() (*publishPrep, error) { return preparePublish("blocks publish", nil) })
	if err != nil {
		t.Fatalf("preparePublish: %v", err)
	}
	if prep.enterprise {
		t.Error("publish would suppress its prompts as though the target were an enterprise deployment")
	}
}

// captureStdoutErr runs fn with stdout captured and returns its results, so a test
// can call a chatty helper without its output landing in the test log.
func captureStdoutErr[T any](t *testing.T, fn func() (T, error)) (T, error) {
	t.Helper()
	var out T
	var err error
	captureStdout(func() { out, err = fn() })
	return out, err
}

// stdin is consumable, so a key piped in with --api-key-stdin can be read exactly
// once. Both the context banner and the request itself use that key, and this pins
// that they share one read rather than the second finding an empty stdin.
func TestPipedApiKeyIsReadExactlyOncePerInvocation(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	forceTTY(t)

	deployment := newDeploymentServer(t, false)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      deployment.url,
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_profile"}},
	})
	t.Setenv(blocksBackendURLEnv, deployment.url)

	reads := 0
	origRead := readAPIKeyFromStdin
	readAPIKeyFromStdin = func() (string, error) {
		reads++
		return "bk_piped", nil
	}
	t.Cleanup(func() { readAPIKeyFromStdin = origRead })

	cardPath := filepath.Join(writeValidProject(t), "agent-card.json")
	resetPublishFlags()
	t.Cleanup(resetPublishFlags)

	out := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--api-key-stdin",
			"--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish: %v", err)
		}
	})

	if reads != 1 {
		t.Fatalf("stdin was read %d time(s), want exactly 1", reads)
	}
	if n := deployment.requestsTo["/api/v1/registry/agents"]; n != 1 {
		t.Fatalf("%d publish request(s), want 1", n)
	}
	var sent string
	for _, auth := range deployment.authorizations {
		if auth != "" {
			sent = auth
		}
	}
	if sent != "Bearer bk_piped" {
		t.Errorf("publish sent %q, want the piped key", sent)
	}
	// The banner consumed the same resolved credential: it could not name the org
	// behind a key the store has never seen.
	if !strings.Contains(out, "organization unknown") {
		t.Errorf("banner should describe the piped credential:\n%s", out)
	}
}

// Regression. BLOCKS_BACKEND_URL with no explicit deployment choice is the
// supported headless mechanism: it must decide both the backend the login reaches
// and the pin it leaves behind.
func TestAmbientBackendURLRemainsTheHeadlessMechanism(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const ambient = "https://blocks.enterprise.example"
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	t.Setenv(blocksBackendURLEnv, ambient)
	resolveCLIContext(t, loginCmd)

	choice := deploymentChoice{}
	if got := resolveLoginBackend(choice); got != ambient {
		t.Errorf("resolveLoginBackend = %q, want the ambient backend %q", got, ambient)
	}
	if got := loginBackendPin(choice); got != ambient {
		t.Errorf("loginBackendPin = %q, want the ambient backend %q", got, ambient)
	}
	if got := clictx.BackendURL(); got != ambient {
		t.Errorf("clictx.BackendURL() = %q, want %q", got, ambient)
	}
}

// Regression. An explicit Network request outranks every ambient input and leaves
// no pin, so a Network key can never end up beside an enterprise URL.
func TestExplicitNetworkIgnoresTheAmbientBackendAndLeavesNoPin(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	seedProfile(t, "umbrella.blocks.ai", profiles.Profile{
		BaseURL:    "https://umbrella.blocks.ai",
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	t.Setenv(blocksBackendURLEnv, "https://blocks.enterprise.example")
	resolveCLIContext(t, loginCmd)

	d := resolveLoginDeployment(deploymentChoice{explicitNetwork: true})
	if !d.network {
		t.Fatal("an explicit Network request must resolve to Blocks Network")
	}
	if d.url != "" {
		t.Errorf("explicit Network resolved to %q; ambient inputs must not reach it", d.url)
	}
	if got := loginBackendPin(deploymentChoice{explicitNetwork: true}); got != "" {
		t.Errorf("loginBackendPin = %q, want no pin for Blocks Network", got)
	}
}

// Regression. A name matching a local profile resolves to that profile's
// deployment and keeps the alias as the place the credential is stored, so a
// custom-domain customer does not accumulate a second profile named after the host.
func TestProfileAliasResolvesToItsBaseURLAndKeepsTheAliasName(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	const acmeURL = "https://blocks.acme.com"
	seedProfile(t, "acme", profiles.Profile{BaseURL: acmeURL, Orgs: map[string]profiles.OrgKey{}})

	instanceURL, matched := resolveInstanceArg("acme")
	if instanceURL != acmeURL {
		t.Errorf("resolveInstanceArg(\"acme\") url = %q, want %q", instanceURL, acmeURL)
	}
	if matched != "acme" {
		t.Errorf("resolveInstanceArg(\"acme\") matched profile = %q, want acme", matched)
	}
	choice := deploymentChoice{instanceURL: instanceURL, profileName: matched}
	if got := resolveProfileName(choice); got != "acme" {
		t.Errorf("resolveProfileName = %q, want acme (no profile named after the host)", got)
	}
	if got := loginBackendPin(choice); got != acmeURL {
		t.Errorf("loginBackendPin = %q, want the alias's own deployment %q", got, acmeURL)
	}
}
