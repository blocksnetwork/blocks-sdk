package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

// cdmDocument is the remote-config payload, naming apiBaseURL as the API origin.
func cdmDocument(apiBaseURL string) string {
	return `{"playground":{"publishKey":"demo","subscribeKey":"demo"},` +
		`"network":{"publishKey":"demo","subscribeKey":"demo"},` +
		`"api":{"baseUrl":"` + apiBaseURL + `","clientId":"test-client-id"}}`
}

// networkRemoteConfigAt makes Blocks Network's own remote config resolve to url.
//
// It writes the cached config document under an isolated HOME, which is the tier
// cdm.Get falls back to with no BLOCKS_CDM_URL set — and it has to be that tier,
// because an explicit Network login drops that variable, so pointing it at a test
// server would no longer describe what the CLI does. Isolating HOME is what keeps
// such a test away from the developer's own cached config, and so away from the real
// deployment it names.
func networkRemoteConfigAt(t *testing.T, url string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	dir := filepath.Join(home, ".blocks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cdmDocument(url)), 0600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	cdm.Reset()
	t.Cleanup(cdm.Reset)
}

// cdmEndpointServing stands in for a deployment's own CDM endpoint, reporting
// apiBaseURL as the API origin. It counts fetches, so a test can assert that a login
// never consulted it at all.
func cdmEndpointServing(t *testing.T, apiBaseURL string, fetches *atomic.Int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(cdmDocument(apiBaseURL)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// testServerWithDiscovery returns a test server that serves enterprise discovery
func testServerWithDiscovery(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli-config":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"enterprise": true,
				"productName": "PubNub",
				"oauthClientId": "enterprise-client-id",
				"dashboardBaseUrl": "https://dashboard.enterprise.example"
			}`))
		case "/api/v1/registry/publish-context":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"orgId":"org-test","orgName":"Test Org","agentCount":0}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupEnterpriseInstancePrompt puts the deployment prompt in the interactive
// shape reached after the user has chosen an Enterprise instance, answering it
// with the given lines.
func setupEnterpriseInstancePrompt(t *testing.T, answers string) {
	t.Helper()
	origScanner := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader(answers))
	origNoInput := noInputMode
	noInputMode = false
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() {
		stdinScanner = origScanner
		noInputMode = origNoInput
		isTTY = origIsTTY
	})
	t.Cleanup(resetLoginFlags)
	resetLoginFlags()
}

// Test that explicit Network choice via prompt ignores BLOCKS_BACKEND_URL environment variable
func TestExplicitNetworkChoiceIgnoresEnvBackendURL(t *testing.T) {
	defer isolateProfiles(t)()

	enterpriseURL := "https://blocks.enterprise.example"
	networkURL := "https://api.blocks.ai"

	networkRemoteConfigAt(t, networkURL)
	t.Setenv("BLOCKS_BACKEND_URL", enterpriseURL)

	// Test the prompt case (option 0 = Network)
	choice := deploymentChoice{instanceURL: "", explicitNetwork: true} // simulates user picking Network
	resolved := resolveLoginBackend(choice)

	if resolved == enterpriseURL {
		t.Errorf("explicit Network choice should not use BLOCKS_BACKEND_URL=%s, but got %s", enterpriseURL, resolved)
	}
	if resolved != networkURL {
		t.Errorf("Expected Network URL %s, got %s", networkURL, resolved)
	}
}

// Test that --network flag ignores BLOCKS_BACKEND_URL environment variable
func TestNetworkFlagIgnoresEnvBackendURL(t *testing.T) {
	defer isolateProfiles(t)()

	enterpriseURL := "https://blocks.enterprise.example"
	networkURL := "https://api.blocks.ai"

	networkRemoteConfigAt(t, networkURL)
	t.Setenv("BLOCKS_BACKEND_URL", enterpriseURL)

	// Test the --network flag case
	choice := deploymentChoice{instanceURL: "", explicitNetwork: true} // simulates --network flag
	resolved := resolveLoginBackend(choice)

	if resolved == enterpriseURL {
		t.Errorf("--network flag should not use BLOCKS_BACKEND_URL=%s, but got %s", enterpriseURL, resolved)
	}
	if resolved != networkURL {
		t.Errorf("Expected Network URL %s, got %s", networkURL, resolved)
	}
}

// Test headless mechanism still works: BLOCKS_BACKEND_URL set, no explicit Network, non-TTY → resolved backend IS that URL
func TestHeadlessMechanism_EnvVarSetNoExplicitChoice_BackendIsThatURL(t *testing.T) {
	defer isolateProfiles(t)()

	enterpriseURL := "https://blocks.enterprise.example"

	t.Setenv("BLOCKS_BACKEND_URL", enterpriseURL)

	// Test non-explicit case (no flag, no prompt)
	resolveCLIContext(t, loginCmd)
	choice := deploymentChoice{instanceURL: "", explicitNetwork: false}
	resolved := resolveLoginBackend(choice)

	if resolved != enterpriseURL {
		t.Errorf("Headless mechanism broken: non-explicit choice should use BLOCKS_BACKEND_URL=%s, but got %s", enterpriseURL, resolved)
	}
}

// Test that empty instanceURL prevents profile from gaining enterprise metadata
func TestEmptyInstanceURLPreventsEnterpriseMetadata(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	// Nothing points this login anywhere: no instance argument, no ambient backend
	// URL, and a profile that records no deployment. Resolving the context here is
	// what makes that the premise rather than whatever ran before.
	resolveCLIContext(t, loginCmd)

	// Create a clean profile
	p := &profiles.Profile{}
	choice := deploymentChoice{instanceURL: "", explicitNetwork: false}

	// Mock discovery config
	disco := &cliconfig.Config{
		Enterprise:       true,
		ProductName:      "PubNub",
		OAuthClientID:    "enterprise-client-id",
		DashboardBaseURL: "https://dashboard.enterprise.example",
	}

	mergeDiscovery(p, choice, disco)

	// Profile should NOT have enterprise metadata when instanceURL is empty and not explicit Network
	if p.Enterprise {
		t.Error("profile should not gain Enterprise=true when instanceURL is empty")
	}
	if p.ProductName != "" {
		t.Errorf("profile should not gain ProductName, got %q", p.ProductName)
	}
	if p.OAuthClientID != "" {
		t.Errorf("profile should not gain OAuthClientID, got %q", p.OAuthClientID)
	}
}

// Test that explicit Network choice clears stale enterprise metadata from profile
func TestExplicitNetworkClearsEnterpriseMetadata(t *testing.T) {
	defer isolateProfiles(t)()

	// Create a profile corrupted with enterprise metadata
	p := &profiles.Profile{
		Enterprise:       true,
		ProductName:      "PubNub",
		OAuthClientID:    "stale-client-id",
		DashboardBaseURL: "https://stale.dashboard.url",
	}
	choice := deploymentChoice{instanceURL: "", explicitNetwork: true}

	mergeDiscovery(p, choice, nil)

	// Profile should be cleaned of enterprise metadata
	if p.Enterprise {
		t.Error("explicit Network should clear Enterprise=true")
	}
	if p.ProductName != "" {
		t.Errorf("explicit Network should clear ProductName, still has %q", p.ProductName)
	}
	if p.OAuthClientID != "" {
		t.Errorf("explicit Network should clear OAuthClientID, still has %q", p.OAuthClientID)
	}
	if p.DashboardBaseURL != "" {
		t.Errorf("explicit Network should clear DashboardBaseURL, still has %q", p.DashboardBaseURL)
	}
}

// An ambient BLOCKS_BACKEND_URL is where the login went, so it is the pin that
// belongs beside the key it minted — even though the key lands in a profile that
// records no BaseURL of its own. Reading the profile here would delete the pin
// that sent the login to that backend and leave the key resolving to Network.
func TestMaybeWriteEnvKeepsThePinTheLoginFollowed(t *testing.T) {
	defer isolateProfiles(t)()
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	enterpriseURL := "https://blocks.enterprise.example"
	os.WriteFile(envFile, []byte("BLOCKS_BACKEND_URL="+enterpriseURL+"\n"), 0644)
	t.Setenv("BLOCKS_BACKEND_URL", enterpriseURL)

	// The active profile records no deployment: no instance was named, so
	// discovery had nothing to write there.
	store := &profiles.Contexts{
		SchemaVersion: 3,
		Active:        "blocks-network",
		Profiles: map[string]profiles.Profile{
			"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
		},
	}
	profiles.Save(store)

	origLoginWriteEnv := loginWriteEnv
	origLoginDir := loginDir
	loginWriteEnv = true
	loginDir = tmpDir
	defer func() {
		loginWriteEnv = origLoginWriteEnv
		loginDir = origLoginDir
	}()

	resolveCLIContext(t, loginCmd)
	if err := maybeWriteEnv("bk_enterprise_test_key", deploymentChoice{}); err != nil {
		t.Fatalf("maybeWriteEnv failed: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("Failed to read .env: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY=bk_enterprise_test_key") {
		t.Error(".env should contain the API key")
	}
	if !strings.Contains(content, "BLOCKS_BACKEND_URL="+enterpriseURL) {
		t.Errorf(".env must keep the backend the login used, got:\n%s", content)
	}
}

// The complement: when the login definitively targeted Network, a leftover pin
// from an earlier deployment must go, or the file would pair a Network key with
// an enterprise URL.
func TestMaybeWriteEnvRemovesAStalePinWhenNetworkWasChosen(t *testing.T) {
	defer isolateProfiles(t)()
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	os.WriteFile(envFile, []byte("BLOCKS_BACKEND_URL=https://stale.enterprise.url\n"), 0644)
	t.Setenv("BLOCKS_BACKEND_URL", "https://stale.enterprise.url")

	store := &profiles.Contexts{
		SchemaVersion: 3,
		Active:        "blocks-network",
		Profiles: map[string]profiles.Profile{
			"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
		},
	}
	profiles.Save(store)

	origLoginWriteEnv := loginWriteEnv
	origLoginDir := loginDir
	loginWriteEnv = true
	loginDir = tmpDir
	defer func() {
		loginWriteEnv = origLoginWriteEnv
		loginDir = origLoginDir
	}()

	resolveCLIContext(t, loginCmd)
	if err := maybeWriteEnv("bk_network_test_key", deploymentChoice{explicitNetwork: true}); err != nil {
		t.Fatalf("maybeWriteEnv failed: %v", err)
	}

	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("Failed to read .env: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY=bk_network_test_key") {
		t.Error(".env should contain the API key")
	}
	if strings.Contains(content, "BLOCKS_BACKEND_URL") {
		t.Errorf(".env should not keep a stale pin when Network was chosen, got:\n%s", content)
	}
}

// Test regression guard: profile HAS BaseURL → both keys written
func TestRegression_ProfileHasBaseURL_BothKeysWritten(t *testing.T) {
	defer isolateProfiles(t)()
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	enterpriseURL := "https://blocks.enterprise.example"

	// Create profile with BaseURL
	store := &profiles.Contexts{
		SchemaVersion: 3,
		Active:        "enterprise",
		Profiles: map[string]profiles.Profile{
			"enterprise": {BaseURL: enterpriseURL, Orgs: map[string]profiles.OrgKey{}},
		},
	}
	profiles.Save(store)

	// Mock the login flags to trigger env writing
	origLoginWriteEnv := loginWriteEnv
	origLoginDir := loginDir
	loginWriteEnv = true
	loginDir = tmpDir
	defer func() {
		loginWriteEnv = origLoginWriteEnv
		loginDir = origLoginDir
	}()

	// Call maybeWriteEnv
	resolveCLIContext(t, loginCmd)
	err := maybeWriteEnv("bk_enterprise_test_key", deploymentChoice{})
	if err != nil {
		t.Fatalf("maybeWriteEnv failed: %v", err)
	}

	// Verify .env contents
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("Failed to read .env: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY=bk_enterprise_test_key") {
		t.Error("Regression: .env should contain API key")
	}
	if !strings.Contains(content, "BLOCKS_BACKEND_URL="+enterpriseURL) {
		t.Error("Regression: .env should contain BLOCKS_BACKEND_URL when profile has BaseURL")
	}
}

func TestResolveInstanceArg(t *testing.T) {
	// A local profile named "acme" pointing at a custom domain, to prove rule 2
	// beats the subdomain convention.
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3,
      "active": "blocks-network",
      "profiles": {
        "blocks-network": {"orgs": {}},
        "acme": {"base_url": "https://blocks.acme.com", "enterprise": true, "orgs": {}}
      }
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"rule 1: explicit https URL passes through", "https://umbrella.blocks.ai", "https://umbrella.blocks.ai"},
		{"rule 1: trailing slash trimmed", "https://umbrella.blocks.ai/", "https://umbrella.blocks.ai"},
		{"rule 1: http scheme respected", "http://localhost:3001", "http://localhost:3001"},
		{"rule 2: local profile name wins over subdomain expansion", "acme", "https://blocks.acme.com"},
		{"rule 3: bare host gets a scheme", "blocks.acme.com", "https://blocks.acme.com"},
		{"rule 4: short name expands to the default scheme", "umbrella", "https://umbrella.blocks.ai"},
		{"empty stays empty", "", ""},
		{"whitespace only stays empty", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := resolveInstanceArg(tc.in)
			if got != tc.want {
				t.Fatalf("resolveInstanceArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Rules 3 and 4 build a URL by prefixing a scheme, so an argument that is not a
// host must never reach them: "https://" + "acme/blocks.example.com" is a URL whose
// HOST is acme, and the login would discover, authenticate and send its API key
// there. Discovery accepts a 404 as reachable, so nothing later would notice.
func TestResolveInstanceArgRefusesArgumentsThatAreNotHosts(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	cases := []struct {
		name string
		in   string
	}{
		{"path turns the first label into the host", "acme/blocks.example.com"},
		{"path on a short name", "acme/bar"},
		{"userinfo moves the authority past the @", "acme@collector.example.com"},
		{"query truncates the authority", "acme?x=1"},
		{"fragment truncates the authority", "acme#frag"},
		{"backslash is read as a separator by some parsers", "acme\\blocks.example.com"},
		{"whitespace cannot be part of a host", "acme corp"},
		{"whitespace in a bare host", "acme corp.example.com"},
		{"a short name cannot carry a port", "acme:3001"},
		{"leading hyphen is not a DNS label", "-acme"},
		{"trailing hyphen is not a DNS label", "acme-"},
		{"underscore is not a DNS label", "acme_corp"},
		{"a label is at most 63 characters", strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := resolveInstanceArg(tc.in)
			if got != "" || matched != "" {
				t.Fatalf("resolveInstanceArg(%q) = (%q, %q); want it refused", tc.in, got, matched)
			}
		})
	}
}

// A scheme is not evidence that the authority is the host it appears to be. Every
// argument here parses, and every one of them names an authority other than the one
// it reads as — or a scheme that would put a credential on the wire in clear text.
func TestResolveInstanceArgRefusesURLsThatAreNotDeploymentOrigins(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	cases := []struct {
		name string
		in   string
	}{
		{"userinfo moves the authority past the @", "https://blocks.acme.example@collector.attacker.example"},
		{"userinfo with a password", "https://user:pass@collector.attacker.example"},
		{"an empty userinfo still relocates nothing but reads as a host", "https://blocks.acme.example@/path"},
		{"http to a host that is not this machine", "http://blocks.acme.example"},
		{"http to a public IP address", "http://203.0.113.10:3001"},
		{"a scheme that is not http(s)", "ftp://blocks.acme.example"},
		{"a scheme carrying script", "javascript://blocks.acme.example/%0aalert(1)"},
		{"no host at all", "https://"},
		{"no host, only a path", "https:///api/v1"},
		{"a bracketed host that is not an address", "https://[x]"},
		{"a bracketed host spelled to look like an address", "https://[gggg::1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := resolveInstanceArg(tc.in)
			if got != "" || matched != "" {
				t.Fatalf("resolveInstanceArg(%q) = (%q, %q); want it refused", tc.in, got, matched)
			}
		})
	}
}

// The complement: the schemed forms a real deployment has must survive the check.
func TestResolveInstanceArgStillAcceptsEveryDeploymentURL(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	for _, in := range []string{
		"https://blocks.acme.example",
		"https://blocks.acme.example:8443",
		"https://blocks.acme.example/blocks",
		"https://127.0.0.1:3001",
		"http://localhost:3001",
		"http://127.0.0.1:3001",
		"http://[::1]:3001",
	} {
		t.Run(in, func(t *testing.T) {
			if got, _ := resolveInstanceArg(in); got != in {
				t.Fatalf("resolveInstanceArg(%q) = %q, want it used as given", in, got)
			}
		})
	}
}

// End to end, at the socket: `blocks login https://<trusted>@<host>` reads as a login
// to <trusted> and resolves to <host>, so the assertion is that <host> received
// nothing at all — no request, and no Authorization header carrying the key.
func TestLoginRefusesAURLWhoseAuthorityIsNotTheHostItNames(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	unintended := newDeploymentServer(t, false)
	// http, so that an unguarded login really would complete against this server:
	// its host is loopback, which is the one host a plain-http URL may name.
	arg := "http://blocks.acme.example@" + mustHost(t, unintended.url)
	if got := mustHost(t, arg); got != mustHost(t, unintended.url) {
		t.Fatalf("premise: %q should resolve to host %q, got %q", arg, mustHost(t, unintended.url), got)
	}

	const secret = "bk_live_secret"
	_, err := runLoginCapturing(t, "login", arg, "--api-key", secret, "--no-write-env")

	if len(unintended.requestsTo) != 0 {
		t.Errorf("the login reached the host hidden behind the userinfo: %v", unintended.requestsTo)
	}
	for _, sent := range unintended.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}

	if err == nil {
		t.Fatal("a URL whose authority is not the host it names must fail the login")
	}
	for _, want := range []string{arg, "does not name a deployment", "user@host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	for name, p := range mustLoadProfiles(t).Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused argument must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// The forms that must keep resolving, so the validation cannot quietly refuse a
// deployment a customer really has.
func TestResolveInstanceArgStillAcceptsEveryDocumentedForm(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	cases := map[string]string{
		"acme":                        "https://acme." + defaultInstanceDomain,
		"acme-corp":                   "https://acme-corp." + defaultInstanceDomain,
		"acme2":                       "https://acme2." + defaultInstanceDomain,
		"blocks.acme.example.com":     "https://blocks.acme.example.com",
		"127.0.0.1:3001":              "https://127.0.0.1:3001",
		"https://blocks.acme.example": "https://blocks.acme.example",
		"http://localhost:3001":       "http://localhost:3001",
		strings.Repeat("a", 63):       "https://" + strings.Repeat("a", 63) + "." + defaultInstanceDomain,
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got, _ := resolveInstanceArg(in); got != want {
				t.Fatalf("resolveInstanceArg(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

// End to end: the refusal has to stop the login, not be swallowed into "no
// deployment named" and fall through to a login somewhere else. The error names
// every accepted form, and no credential is stored.
func TestLoginRefusesAnInstanceArgumentThatIsNotAHost(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const arg = "collector.attacker.example/blocks.acme.example"
	_, err := runLoginCapturing(t, "login", arg, "--api-key", "bk_live_secret", "--no-write-env")
	if err == nil {
		t.Fatal("an argument that is not a host must fail the login")
	}
	for _, want := range []string{arg, "does not name a deployment", "short name", "bare host", "full URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}

	store, loadErr := profiles.Load()
	if loadErr != nil {
		t.Fatalf("profiles.Load: %v", loadErr)
	}
	for name, p := range store.Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused argument must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// The domain half of a short name expansion is injected at build time, so it gets
// the same scrutiny as the half the user types: it is appended to a host and then
// prefixed with a scheme, and a suffix carrying userinfo, a path or a port
// relocates the resulting authority exactly as a bad argument would.
func TestIsDNSSuffix(t *testing.T) {
	// A suffix is only usable if a short name still fits in front of it: the
	// expansion is <label>.<suffix>, so the shortest label and its dot take two of
	// the 253 characters a hostname may have. 63 + 1 + 63 + 1 + 63 + 1 + 59 = 251 is
	// the longest suffix that leaves room; one more character leaves none.
	maxLength := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 59),
	}, ".")

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"the shipped default", "blocks.ai", true},
		{"a three-label suffix", "blocks.example.com", true},
		{"a single label is a suffix", "example", true},
		{"hyphens and digits inside a label", "acme-2.example.test", true},
		{"251 characters still leaves room for a one-character label", maxLength, true},
		{"252 characters leaves room for the dot but not the label", maxLength + "e", false},
		{"a suffix at the hostname limit can expand nothing", maxLength + "ee", false},
		{"empty is not a suffix", "", false},
		{"userinfo moves the authority past the @", "corp.example@collector.example", false},
		{"a path ends the authority", "example.com/path", false},
		{"a scheme is not a suffix", "https://example.com", false},
		{"whitespace cannot be part of a name", "exam ple.com", false},
		{"a leading dot is an empty label", ".example.com", false},
		{"a trailing dot is an empty label", "example.com.", false},
		{"a port is not part of the name", "example.com:8443", false},
		{"a label is at most 63 characters", strings.Repeat("a", 64) + ".example.com", false},
		{"a leading hyphen is not a label", "-example.com", false},
		{"a control character cannot be part of a name", "example\x00.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDNSSuffix(tc.in); got != tc.want {
				t.Fatalf("isDNSSuffix(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The suffix passing its own shape check says nothing about the hostname built from
// it: <label>.<suffix> is longer than the suffix, and a hostname past 253 characters
// resolves nowhere. So the expansion measures what it built, which is the only place
// both halves are known — and it is a build defect, since the length the user
// contributed is a legal DNS label either way.
func TestAShortNameIsRefusedWhenTheExpandedHostnameIsTooLong(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	// A suffix well inside its own limit, under which a short label expands fine and
	// a long one does not: 200 + 1 + 60 = 261 characters.
	domain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 8)
	if !isDNSSuffix(domain) {
		t.Fatalf("premise: the suffix itself must be well-formed and inside its own limit (%d characters)", len(domain))
	}
	withInstanceDomain(t, domain)

	short := "a"
	if got, _, err := expandInstanceArg(short); err != nil || got != "https://"+short+"."+domain {
		t.Fatalf("premise: a short name that fits must still expand, got (%q, err=%v)", got, err)
	}

	long := strings.Repeat("e", 60)
	got, _, err := expandInstanceArg(long)
	if err == nil || got != "" {
		t.Fatalf("expandInstanceArg(<60 characters>) = (%q, err=%v); want it refused with a reason", got, err)
	}
	for _, want := range []string{instanceDomainEnv, "253"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the build defect is fixable, got: %v", want, err)
		}
	}
}

// The floor under that check: a suffix long enough that nothing can expand under it is
// not a usable instance domain at all, and the build that carries it is told so before
// any argument is measured against it.
func TestAnInstanceDomainThatLeavesNoRoomForAShortNameIsRefused(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()

	domain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(domain) != maxDNSNameLength {
		t.Fatalf("premise: the suffix should be exactly %d characters, got %d", maxDNSNameLength, len(domain))
	}
	withInstanceDomain(t, domain)

	if _, err := instanceDomain(); err == nil {
		t.Error("a suffix that leaves no room for any label is not a usable instance domain")
	}
	got, _, err := expandInstanceArg("acme")
	if err == nil || got != "" {
		t.Fatalf("expandInstanceArg(\"acme\") = (%q, err=%v); want it refused with a reason", got, err)
	}
	if !strings.Contains(err.Error(), instanceDomainEnv) {
		t.Errorf("error should name %q, got: %v", instanceDomainEnv, err)
	}
}

// A malformed compiled-in domain is a defect in the build, so the error has to name
// the variable that carries it and the value it carries — "acme does not name a
// deployment" would send the user hunting for a mistake they did not make.
func TestInstanceDomainRejectsAnUnusableBuildTimeValue(t *testing.T) {
	const bad = "corp.example@collector.example"
	withInstanceDomain(t, bad)

	got, err := instanceDomain()
	if err == nil {
		t.Fatalf("instanceDomain() = %q, want an error for %q", got, bad)
	}
	if got != "" {
		t.Errorf("instanceDomain() = %q, want no domain alongside the error", got)
	}
	for _, want := range []string{instanceDomainEnv, bad} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got: %v", want, err)
		}
	}
}

// The refusal is scoped to the expansion: a build with an unusable domain is still
// a working CLI for every form that does not need one, so a typo in one release
// variable cannot brick the binary.
func TestAMalformedInstanceDomainOnlyBlocksShortNames(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	withInstanceDomain(t, "corp.example@collector.example")

	if got, _, err := expandInstanceArg("acme"); err == nil || got != "" {
		t.Errorf("expandInstanceArg(\"acme\") = (%q, err=%v); want it refused with a reason", got, err)
	}
	for _, in := range []string{"https://blocks.acme.example", "blocks.acme.example"} {
		got, _, err := expandInstanceArg(in)
		if err != nil {
			t.Errorf("expandInstanceArg(%q) errored on a build defect it does not depend on: %v", in, err)
		}
		if got != "https://blocks.acme.example" {
			t.Errorf("expandInstanceArg(%q) = %q, want the URL to still resolve", in, got)
		}
	}
}

// End to end: with an unusable compiled-in domain, `blocks login acme` must fail
// instead of expanding to a URL whose authority is somewhere the user never named.
// "corp.example@<host>" is the shape that matters — everything before the @ is
// userinfo, so the expansion's real HOST is <host>, and the login would hand it the
// API key. The assertion is that nothing reached that host at all: not a request,
// not a byte, not a socket.
func TestLoginRefusesToExpandAShortNameUnderAMalformedInstanceDomain(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	unintended := newConnectionRecorder(t)
	badDomain := "corp.example@" + unintended.host
	withInstanceDomain(t, badDomain)

	// Premise: unguarded, this expansion really does point at the recorder.
	if got := mustHost(t, "https://acme."+badDomain); got != unintended.host {
		t.Fatalf("premise: the expansion should resolve to host %q, got %q", unintended.host, got)
	}

	const secret = "bk_live_secret"
	_, err := runLoginCapturing(t, "login", "acme", "--api-key", secret, "--no-write-env")
	if err == nil {
		t.Fatal("a build whose instance domain is not a DNS suffix must refuse the expansion")
	}
	for _, want := range []string{instanceDomainEnv, badDomain} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the build defect is fixable, got: %v", want, err)
		}
	}

	conns, received := unintended.observed()
	if conns != 0 {
		t.Errorf("the login opened %d connection(s) to a host the user never named", conns)
	}
	if strings.Contains(received, secret) {
		t.Errorf("the API key was transmitted to a host the user never named")
	}

	for name, p := range mustLoadProfiles(t).Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused expansion must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// withInstanceDomain builds this test as though the release had injected domain,
// which is the only way the value can be wrong in production.
func withInstanceDomain(t *testing.T, domain string) {
	t.Helper()
	orig := defaultInstanceDomain
	defaultInstanceDomain = domain
	t.Cleanup(func() { defaultInstanceDomain = orig })
}

// connectionRecorder is a bare TCP listener standing in for a host the CLI must
// never contact. It records every connection accepted and every byte received, so
// "no credential was transmitted" can be asserted in its strongest form: nothing
// was sent because no socket to that authority was ever opened. Recording at the
// socket rather than behind an HTTP handler keeps the assertion independent of the
// scheme — an https expansion whose TLS handshake fails still shows up here.
type connectionRecorder struct {
	host  string
	mu    sync.Mutex
	conns int
	bytes []byte
}

func newConnectionRecorder(t *testing.T) *connectionRecorder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	r := &connectionRecorder{host: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns++
			r.mu.Unlock()
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				r.mu.Lock()
				r.bytes = append(r.bytes, buf[:n]...)
				r.mu.Unlock()
			}()
		}
	}()
	return r
}

// observed reports how many connections were accepted and everything received on
// them.
func (r *connectionRecorder) observed() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns, string(r.bytes)
}

func TestResolveInstanceArgIgnoresProfileWithoutBaseURL(t *testing.T) {
	// "blocks-network" exists but records no BaseURL, so rule 2 must not claim
	// it — the name falls through to subdomain expansion.
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "blocks-network",
      "profiles": {"stock": {"orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	got, matched := resolveInstanceArg("stock")
	if want := "https://stock.blocks.ai"; got != want {
		t.Fatalf("resolveInstanceArg(\"stock\") = %q, want %q", got, want)
	}
	if matched != "" {
		t.Fatalf("resolveInstanceArg(\"stock\") matched profile = %q, want none", matched)
	}
}

func TestPromptDeploymentSkippedWhenProfileHasBaseURL(t *testing.T) {
	// Re-login against an existing enterprise profile must not prompt.
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "umbrella.blocks.ai",
      "profiles": {"umbrella.blocks.ai": {"base_url": "https://umbrella.blocks.ai", "enterprise": true, "orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	got, err := promptDeploymentIfFirstLogin()
	if err != nil {
		t.Fatalf("promptDeploymentIfFirstLogin error: %v", err)
	}
	if got.instanceURL != "" {
		t.Fatalf("expected no prompt for an existing deployment, got instanceURL=%q", got.instanceURL)
	}
	if got.explicitNetwork {
		t.Fatalf("expected explicitNetwork=false for existing deployment, got %v", got.explicitNetwork)
	}
}

func TestPromptDeploymentSkippedWhenNotATTY(t *testing.T) {
	// CI must keep today's Network default with no prompt. Under `go test`
	// stdin is not a terminal, so this exercises the real guard.

	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "blocks-network",
      "profiles": {"blocks-network": {"orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	// Override the TTY predicate to force the guard to be meaningful
	origIsTTY := isTTY
	isTTY = func() bool { return false }
	t.Cleanup(func() { isTTY = origIsTTY })

	got, err := promptDeploymentIfFirstLogin()
	if err != nil {
		t.Fatalf("promptDeploymentIfFirstLogin error: %v", err)
	}
	if got.instanceURL != "" {
		t.Fatalf("non-TTY must not prompt, got instanceURL=%q", got.instanceURL)
	}
	if got.explicitNetwork {
		t.Fatalf("expected explicitNetwork=false for non-TTY, got %v", got.explicitNetwork)
	}
}

func TestRequireReachableInstance(t *testing.T) {
	tests := []struct {
		name        string
		instanceURL string
		resolvedURL string
		err         error
		wantErr     bool
	}{
		{
			name:        "network error with instance URL returns error",
			instanceURL: "umbrella",
			resolvedURL: "https://umbrella.blocks.ai",
			err:         &net.DNSError{Err: "no such host", Name: "umbrella.blocks.ai", IsNotFound: true},
			wantErr:     true,
		},
		{
			name:        "error with empty instance URL returns nil",
			instanceURL: "",
			resolvedURL: "https://app.blocks.ai",
			err:         errors.New("boom"),
			wantErr:     false,
		},
		{
			name:        "nil error with instance URL returns nil",
			instanceURL: "umbrella",
			resolvedURL: "https://umbrella.blocks.ai",
			err:         nil,
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireReachableInstance(tt.instanceURL, tt.resolvedURL, tt.err)
			if (err != nil) != tt.wantErr {
				t.Errorf("requireReachableInstance() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err != nil {
				// Check that error contains the resolved URL
				if !strings.Contains(err.Error(), tt.resolvedURL) {
					t.Errorf("error should contain resolved URL %q, got: %v", tt.resolvedURL, err)
				}
			}
		})
	}
}

func TestRequireReachableInstance404StillSucceeds(t *testing.T) {
	// The 404 path must still succeed. When the server returns 404 on /api/v1/cli-config,
	// requireReachableInstance should return nil because Fetch maps 404 to a nil error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli-config" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// Use cliconfig.Fetch to get the real behavior including 404 mapping
	_, err := cliconfig.Fetch(srv.URL)

	// requireReachableInstance should return nil when Fetch returns no error (404 case)
	err = requireReachableInstance("test-instance", srv.URL, err)
	if err != nil {
		t.Errorf("404 path should succeed, but got error: %v", err)
	}
}

func TestLoginToProfileFailsFastAndPreservesCredential(t *testing.T) {
	// loginToProfile should fail fast when pointed at a closed server and preserve
	// the existing credential (not run the eviction that happens later).

	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")

	// Seed a credential file with an existing "blocks" entry using the proper v3 format
	seedCredential := `{
		"schema_version": 3,
		"blocks": {
			"access_token": "existing_token_12345",
			"mint_method": "oauth2"
		}
	}`
	if err := os.WriteFile(credFile, []byte(seedCredential), 0644); err != nil {
		t.Fatalf("failed to seed credential file: %v", err)
	}

	// Override credential path function
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()

	defer isolateProfiles(t)()

	// Create a server and immediately close it to simulate connection error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	srvURL := srv.URL
	srv.Close() // Close it so Fetch gets a connection error

	// Call loginToProfile with non-empty instanceURL pointing at closed server
	_, err := loginToProfile(context.Background(), deploymentChoice{instanceURL: srvURL, explicitNetwork: false})

	// Should return an error naming the resolved URL
	if err == nil {
		t.Fatal("loginToProfile should fail when server is unreachable")
	}
	if !strings.Contains(err.Error(), srvURL) {
		t.Errorf("error should contain resolved URL %q, got: %v", srvURL, err)
	}

	// Most importantly: the existing credential should still be there
	entry, credErr := auth.GetProviderCredential(credFile, "blocks")
	if credErr != nil {
		t.Fatalf("GetProviderCredential: %v", credErr)
	}
	if entry == nil {
		t.Error("existing credential was deleted - eviction ran when it should not have")
		return
	}
	if entry.AccessToken != "existing_token_12345" {
		t.Errorf("credential was modified, expected existing_token_12345, got: %s", entry.AccessToken)
	}
}

// The browser flow creates the API key on the deployment, and everything the login does
// with it afterwards is local and fallible. A failure there leaves a live credential
// nobody has been told about: nothing local can name it, so it cannot be revoked, and
// because `blocks login` always performs a fresh login the next attempt creates another
// beside it. The failure must therefore say a key exists, and name the organization it
// belongs to and the profile that could not hold it.
//
// The store here cannot be read at all, which is the first fallible step after the key
// is created — the one whose error ("cannot read the profile store") says least about
// what just happened at the deployment.
func TestALoginThatCannotRecordAFreshKeySaysTheKeyExists(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deployment := newDeploymentServer(t, false)
	const orgName = "Engineering"
	stubBrowserLoginMinting(t, "bk_minted_at_the_deployment", orgName)
	unreadableProfileStore(t, "{ not a contexts file")

	var err error
	out := captureStdout(func() {
		_, err = loginToProfile(context.Background(), deploymentChoice{instanceURL: deployment.url})
	})

	if err == nil {
		t.Fatal("premise: the local work after the key was created must fail")
	}
	for _, want := range []string{"Created an API key", orgName, mustHost(t, deployment.url), "revoke"} {
		if !strings.Contains(out, want) {
			t.Errorf("a login that could not record a fresh key must say %q:\n%s", want, out)
		}
	}
}

// The complement, and the reason the announcement is gated: a key handed to --api-key
// was not created by this login, so the same failure has nothing to announce — saying
// one was created would send the caller looking for a credential to revoke that this
// invocation never made.
func TestALoginThatWasHandedItsKeyAnnouncesNothingWhenItCannotRecordIt(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deployment := newDeploymentServer(t, false)
	unreadableProfileStore(t, "{ not a contexts file")
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	loginApiKey = "bk_supplied_by_the_caller"
	if f := loginCmd.Flags().Lookup("api-key"); f != nil {
		if err := f.Value.Set(loginApiKey); err != nil {
			t.Fatalf("set --api-key: %v", err)
		}
		f.Changed = true
	}
	resolveCLIContext(t, loginCmd)

	var err error
	out := captureStdout(func() {
		_, err = loginToProfile(context.Background(), deploymentChoice{instanceURL: deployment.url})
	})

	if err == nil {
		t.Fatal("premise: the local work must fail for this to prove anything")
	}
	if strings.Contains(out, "Created an API key") {
		t.Errorf("a supplied key was reported as one this login created:\n%s", out)
	}
}

// stubBrowserLoginMinting stands in for the browser flow, which binds a fixed local
// port, opens a real browser and waits minutes for a callback. It returns what that
// flow returns: a key that now exists on the deployment, for a named organization.
func stubBrowserLoginMinting(t *testing.T, apiKey, orgName string) {
	t.Helper()
	orig := ensureCredentials
	ensureCredentials = func(ctx context.Context, backendURL, clientID, supplied string) (*auth.Credentials, string, error) {
		return &auth.Credentials{ApiKey: apiKey, OrgId: "org-1", OrgName: orgName}, apiKey, nil
	}
	t.Cleanup(func() { ensureCredentials = orig })
}

func TestMaybeWriteEnvWritesBackendURLForEnterprise(t *testing.T) {
	// Reset all login flags for isolation
	resetLoginFlags()

	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "umbrella.blocks.ai",
      "profiles": {"umbrella.blocks.ai": {"base_url": "https://umbrella.blocks.ai", "enterprise": true, "orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	origPath := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = origPath })
	resolveCLIContext(t, loginCmd)

	projectDir := t.TempDir()
	loginDir = projectDir
	loginWriteEnv = true
	loginNoWriteEnv = false
	t.Cleanup(func() { loginDir = ""; loginWriteEnv = false; loginNoWriteEnv = false })

	if err := maybeWriteEnv("bk_secret", deploymentChoice{}); err != nil {
		t.Fatalf("maybeWriteEnv: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(projectDir, ".env"))
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	env := string(data)
	if !strings.Contains(env, "BLOCKS_API_KEY=bk_secret") {
		t.Errorf(".env missing api key:\n%s", env)
	}
	if !strings.Contains(env, "BLOCKS_BACKEND_URL=https://umbrella.blocks.ai") {
		t.Errorf(".env missing backend url:\n%s", env)
	}
}

func TestMaybeWriteEnvOmitsBackendURLOnStockNetwork(t *testing.T) {
	// Stock Network records no BaseURL; writing one would pin the project to a
	// URL the CLI otherwise resolves via CDM.
	// Reset all login flags for isolation
	resetLoginFlags()

	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "blocks-network",
      "profiles": {"blocks-network": {"orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	origPath := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = origPath })
	resolveCLIContext(t, loginCmd)

	projectDir := t.TempDir()
	loginDir = projectDir
	loginWriteEnv = true
	loginNoWriteEnv = false
	t.Cleanup(func() { loginDir = ""; loginWriteEnv = false; loginNoWriteEnv = false })

	if err := maybeWriteEnv("bk_secret", deploymentChoice{}); err != nil {
		t.Fatalf("maybeWriteEnv: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(projectDir, ".env"))
	if strings.Contains(string(data), "BLOCKS_BACKEND_URL") {
		t.Errorf("stock Network must not pin a backend url:\n%s", data)
	}
}

// The key and the deployment pin reach .env as ONE rewrite. Written separately, a
// failure between them leaves the freshly minted key beside the previous
// deployment's URL, which is a credential every later command sends to the wrong
// place. Here every write fails, and the discriminating assertion is how many were
// attempted: one rewrite means one warning, two separate writes meant two.
func TestMaybeWriteEnvAppliesTheKeyAndPinAsOneRewrite(t *testing.T) {
	// Reset flags for isolation
	resetLoginFlags()

	// Set up enterprise profile
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "umbrella.blocks.ai",
      "profiles": {"umbrella.blocks.ai": {"base_url": "https://umbrella.blocks.ai", "enterprise": true, "orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	origPath := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = origPath })
	resolveCLIContext(t, loginCmd)

	// Create a directory named ".env" to make writes fail with EISDIR
	projectDir := t.TempDir()
	envDirPath := filepath.Join(projectDir, ".env")
	if err := os.Mkdir(envDirPath, 0755); err != nil {
		t.Fatalf("mkdir .env: %v", err)
	}

	// Set up warning mode: interactive=true, loginWriteEnv=false, pre-set stdin
	origIsInteractive := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = origIsInteractive })

	// Pre-set stdin scanner to simulate "y" response
	origStdinScanner := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader("y\n"))
	t.Cleanup(func() { stdinScanner = origStdinScanner })

	loginDir = projectDir
	loginWriteEnv = false // warning mode - failures should be warnings, not fatal
	loginNoWriteEnv = false
	t.Cleanup(func() { loginDir = ""; loginWriteEnv = false; loginNoWriteEnv = false })

	// Capture stderr to count warnings
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	// Call maybeWriteEnv - should not return an error (warning mode)
	err = maybeWriteEnv("bk_secret", deploymentChoice{})

	// Restore stderr and read captured output
	w.Close()
	os.Stderr = origStderr
	var buf strings.Builder
	io.Copy(&buf, r)
	stderrOutput := buf.String()

	if err != nil {
		t.Fatalf("maybeWriteEnv should not fail in warning mode: %v", err)
	}

	warningCount := strings.Count(stderrOutput, "Warning:")
	if warningCount != 1 {
		t.Errorf("expected one warning (the key and the pin are one rewrite), got %d in: %q", warningCount, stderrOutput)
	}
}

// The error policy applied to that one rewrite: a warning when the write was only
// offered, fatal when --write-env asked for it.
func TestApplyEnvMutationsHonoursTheWriteEnvErrorPolicy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only directory, so the failure cannot be provoked")
	}
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// An unwritable directory makes the rewrite fail without any file to inspect.
	projectDir := t.TempDir()
	if err := os.Chmod(projectDir, 0555); err != nil { // read+execute only, no write
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(projectDir, 0755) }) // restore for cleanup

	mutations := []auth.EnvMutation{
		{Key: blocksBackendURLEnv, Value: "https://blocks.acme.example"},
		{Key: blocksAPIKeyEnv, Value: "bk_secret"},
	}

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w

	loginWriteEnv = false // offered, not requested: a failure must not fail the login
	warnErr := applyEnvMutations(projectDir, mutations...)
	loginWriteEnv = true // explicitly requested: a failure is the login's failure
	fatalErr := applyEnvMutations(projectDir, mutations...)

	w.Close()
	os.Stderr = origStderr
	var buf strings.Builder
	io.Copy(&buf, r)
	stderrOutput := buf.String()

	if warnErr != nil {
		t.Errorf("a failed write must not fail the login when it was only offered: %v", warnErr)
	}
	if fatalErr == nil {
		t.Error("a failed write must fail the login when --write-env requested it")
	}
	if got := strings.Count(stderrOutput, "Warning:"); got != 1 {
		t.Errorf("expected exactly one warning for one rewrite, got %d in: %q", got, stderrOutput)
	}
}

func TestDescribeUnreachableHostnameMismatch(t *testing.T) {
	// Use httptest.NewTLSServer to create a real hostname mismatch error chain
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// Use srv.Client() to trust the server's CA (essential for hostname verification)
	c := srv.Client()
	// Create hostname mismatch by changing "127.0.0.1" to "localhost"
	mismatch := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	_, rawErr := c.Get(mismatch + "/api/v1/cli-config")
	if rawErr == nil {
		t.Fatal("expected hostname mismatch error, got nil")
	}

	// Mirror cliconfig.Fetch exactly
	err := fmt.Errorf("cli-config fetch failed: %w", rawErr)

	// Verify we can extract the hostname error with value form (not pointer)
	var hostnameErr x509.HostnameError
	if !errors.As(err, &hostnameErr) {
		t.Fatalf("error chain does not contain x509.HostnameError, got: %T: %v", err, err)
	}

	// Test the message classification
	message := describeUnreachable(mismatch, err)
	expectedMessage := "no Blocks deployment at " + mismatch
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}

	// Verify the message does NOT contain leaked internals
	if strings.Contains(message, "certificate") || strings.Contains(message, "x509") || strings.Contains(message, "netlify") {
		t.Errorf("message should not contain certificate internals: %q", message)
	}
}

func TestDescribeUnreachableDNSNotFound(t *testing.T) {
	// Construct a DNS not-found error directly
	dnsErr := &net.DNSError{
		Err:        "no such host",
		Name:       "nonexistent.example.com",
		IsNotFound: true,
	}

	message := describeUnreachable("https://nonexistent.example.com", dnsErr)
	expectedMessage := "https://nonexistent.example.com does not resolve"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}
}

func TestDescribeUnreachableConnectionRefused(t *testing.T) {
	// Create a connection refused error using the portable pattern our classifier expects
	// Use a generic error that represents connection refused without platform-specific constants
	connRefusedErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		// Use a representative error that our portable classifier will detect as connection refused
		// (not a timeout, not DNS, not nil - so it gets classified as connection refused)
		Err: fmt.Errorf("connect: connection refused"),
	}

	message := describeUnreachable("https://localhost:99999", connRefusedErr)
	expectedMessage := "nothing is listening at https://localhost:99999"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}
}

func TestDescribeUnreachableTimeout(t *testing.T) {
	// Create a timeout error
	timeoutErr := &net.DNSError{
		Err:       "i/o timeout",
		Name:      "example.com",
		IsTimeout: true,
	}

	message := describeUnreachable("https://example.com", timeoutErr)
	expectedMessage := "timed out connecting to https://example.com"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}
}

func TestDescribeUnreachableHTTPStatus(t *testing.T) {
	// Test via a server returning 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, fetchErr := cliconfig.Fetch(srv.URL)
	if fetchErr == nil {
		t.Fatal("expected HTTP 500 error, got nil")
	}

	message := describeUnreachable(srv.URL, fetchErr)
	expectedMessage := srv.URL + " returned HTTP 500"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}

	// Test the hint for HTTP status errors
	hints := formatHints(srv.URL, fetchErr)
	expectedHint := "  The deployment may be temporarily unavailable. Try again, or contact your administrator."
	if hints != expectedHint {
		t.Errorf("formatHints() = %q, want %q", hints, expectedHint)
	}
}

func TestDescribeUnreachableMalformedResponse(t *testing.T) {
	// Create a typed decode error
	decodeErr := cliconfig.DecodeError{Err: fmt.Errorf("invalid character 'x' looking for beginning of value")}

	message := describeUnreachable("https://example.com", decodeErr)
	expectedMessage := "https://example.com did not return a Blocks configuration"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}
}

func TestDescribeUnreachableFallback(t *testing.T) {
	// Test unclassified error
	unknownErr := errors.New("something completely unexpected")

	message := describeUnreachable("https://example.com", unknownErr)
	expectedMessage := "could not reach https://example.com: something completely unexpected"
	if message != expectedMessage {
		t.Errorf("describeUnreachable() = %q, want %q", message, expectedMessage)
	}
}

func TestFormatHintsShortName(t *testing.T) {
	// Test that spelling hint appears for single-label hostname under defaultInstanceDomain
	hostnameErr := x509.HostnameError{
		Certificate: &x509.Certificate{}, // Prevent nil pointer dereference
		Host:        "garb.blocks.ai",
	}
	hints := formatHints("https://garb.blocks.ai", hostnameErr)

	expectedHints := "  A short name expands to <name>.blocks.ai — check the spelling.\n" +
		"  If your deployment uses a custom domain, pass its full URL instead."

	if hints != expectedHints {
		t.Errorf("formatHints() for short name = %q, want %q", hints, expectedHints)
	}
}

func TestFormatHintsCustomDomain(t *testing.T) {
	// Test that spelling hint does NOT appear for custom domains
	hostnameErr := x509.HostnameError{
		Certificate: &x509.Certificate{}, // Prevent nil pointer dereference
		Host:        "blocks.acme.com",
	}
	hints := formatHints("https://blocks.acme.com", hostnameErr)

	expectedHints := "  If your deployment uses a custom domain, pass its full URL instead."

	if hints != expectedHints {
		t.Errorf("formatHints() for custom domain = %q, want %q", hints, expectedHints)
	}

	// Assert that spelling hint is absent
	if strings.Contains(hints, "check the spelling") {
		t.Errorf("formatHints() should not contain spelling hint for custom domain, got: %q", hints)
	}
}

func TestFormatHintsMultiLabelInstanceDomain(t *testing.T) {
	// Test that spelling hint does NOT appear for multi-label hosts under instance domain
	hostnameErr := x509.HostnameError{
		Certificate: &x509.Certificate{}, // Prevent nil pointer dereference
		Host:        "foo.bar.blocks.ai",
	}
	hints := formatHints("https://foo.bar.blocks.ai", hostnameErr)

	expectedHints := "  If your deployment uses a custom domain, pass its full URL instead."

	if hints != expectedHints {
		t.Errorf("formatHints() for multi-label host = %q, want %q", hints, expectedHints)
	}

	// Assert that spelling hint is absent (not a short name expansion)
	if strings.Contains(hints, "check the spelling") {
		t.Errorf("formatHints() should not contain spelling hint for multi-label host, got: %q", hints)
	}
}

func TestFormatHintsDecodeError(t *testing.T) {
	// Test that no hints are shown for decode errors
	decodeErr := cliconfig.DecodeError{Err: fmt.Errorf("invalid JSON")}
	hints := formatHints("https://example.com", decodeErr)

	if hints != "" {
		t.Errorf("formatHints() for decode error should return empty string, got: %q", hints)
	}
}

func TestPromptDeploymentNoInputModeReturnsError(t *testing.T) {
	// Test that --no-input propagates error instead of silently returning empty string

	// Reset all state first
	resetLoginFlags()
	wizard.SetNoInputMode(false) // Ensure clean start
	t.Cleanup(func() {
		resetLoginFlags()
		wizard.SetNoInputMode(false)
	})

	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3, "active": "blocks-network",
      "profiles": {"blocks-network": {"orgs": {}}}
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	origPath := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = origPath })
	resolveCLIContext(t, loginCmd)

	// Override isTTY to return true so we reach the interactive path
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() { isTTY = origIsTTY })

	// Ensure we're not using cached flags from previous tests
	loginApiKey = ""
	loginApiKeyStdin = false
	loginNetwork = false

	// Set no-input mode in the wizard package AFTER all other setup
	wizard.SetNoInputMode(true)

	// This should return an error mentioning --network
	_, err := promptDeploymentIfFirstLogin()
	if err == nil {
		t.Fatal("expected error when --no-input is set and prompt would be required")
	}
	if !strings.Contains(err.Error(), "--network") {
		t.Errorf("error should mention --network flag, got: %v", err)
	}
}

func TestRequireReachableInstanceNewMessages(t *testing.T) {
	tests := []struct {
		name            string
		instanceURL     string
		resolvedURL     string
		err             error
		wantErr         bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:        "hostname mismatch produces clean message",
			instanceURL: "garb",
			resolvedURL: "https://garb.blocks.ai",
			err: x509.HostnameError{
				Certificate: &x509.Certificate{}, // Prevent nil pointer dereference
				Host:        "garb.blocks.ai",
			},
			wantErr:         true,
			wantContains:    []string{"no Blocks deployment at https://garb.blocks.ai", "check the spelling"},
			wantNotContains: []string{"certificate", "x509", "netlify"},
		},
		{
			name:            "HTTP 500 warns and continues",
			instanceURL:     "test",
			resolvedURL:     "https://test.blocks.ai",
			err:             cliconfig.HTTPStatusError{StatusCode: 500},
			wantErr:         false,
			wantContains:    []string{}, // Warnings go to stderr, not error return
			wantNotContains: []string{},
		},
		{
			name:            "DNS error",
			instanceURL:     "missing",
			resolvedURL:     "https://missing.blocks.ai",
			err:             &net.DNSError{Err: "no such host", Name: "missing.blocks.ai", IsNotFound: true},
			wantErr:         true,
			wantContains:    []string{"does not resolve", "check the spelling"},
			wantNotContains: []string{"certificate"},
		},
		{
			name:        "no error returns nil",
			instanceURL: "test",
			resolvedURL: "https://test.blocks.ai",
			err:         nil,
			wantErr:     false,
		},
		{
			name:        "empty instanceURL returns nil even with error",
			instanceURL: "",
			resolvedURL: "https://app.blocks.ai",
			err:         errors.New("some error"),
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireReachableInstance(tt.instanceURL, tt.resolvedURL, tt.err)

			if (err != nil) != tt.wantErr {
				t.Errorf("requireReachableInstance() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				return
			}

			errMsg := err.Error()
			for _, want := range tt.wantContains {
				if !strings.Contains(errMsg, want) {
					t.Errorf("error message should contain %q, got: %v", want, errMsg)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if strings.Contains(errMsg, notWant) {
					t.Errorf("error message should NOT contain %q, got: %v", notWant, errMsg)
				}
			}
		})
	}
}

// TestNetworkLoginStoresUnderDefaultProfile tests the core fix: when an enterprise
// profile is active and we run --network login, the credential should be stored
// under blocks-network, not the active enterprise profile.
func TestNetworkLoginStoresUnderDefaultProfile(t *testing.T) {
	tmpDir := t.TempDir()
	credFile := filepath.Join(tmpDir, "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	defer func() { auth.CredentialPathFunc = origPathFunc }()
	defer isolateProfiles(t)()
	srv := loginTestServer(t)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)

	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// Blocks Network's own remote config, pointed at the test server
	networkRemoteConfigAt(t, srv.URL)

	// Set up an enterprise profile as active
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3,
      "active": "acme.blocks.ai",
      "profiles": {
        "blocks-network": {"orgs": {}},
        "acme.blocks.ai": {"enterprise": true, "orgs": {"org-1": {"org_name": "Enterprise Org", "api_key": "bk_enterprise_key", "key_id": "key1", "expires_at": "0001-01-01T00:00:00Z"}}}
      }
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	origPath := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = origPath })
	resolveCLIContext(t, loginCmd)

	// Run --network login
	loginNetwork = true
	rootCmd.SetArgs([]string{"login", "--api-key", "bk_network_key_123", "--no-write-env", "--network"})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("login --network failed: %v", err)
	}

	// Verify: the new credential should be stored under blocks-network, not acme.blocks.ai
	c, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}

	// blocks-network should have the new key
	networkProfile := c.Profiles[profiles.DefaultProfile]
	if len(networkProfile.Orgs) != 1 {
		t.Errorf("blocks-network should have exactly 1 org key, got %d", len(networkProfile.Orgs))
	}
	for _, orgKey := range networkProfile.Orgs {
		if orgKey.ApiKey != "bk_network_key_123" {
			t.Errorf("blocks-network key = %q, want bk_network_key_123", orgKey.ApiKey)
		}
	}

	// acme.blocks.ai should be unchanged
	enterpriseProfile := c.Profiles["acme.blocks.ai"]
	if len(enterpriseProfile.Orgs) != 1 {
		t.Errorf("acme.blocks.ai should still have exactly 1 org key, got %d", len(enterpriseProfile.Orgs))
	}
	for _, orgKey := range enterpriseProfile.Orgs {
		if orgKey.ApiKey != "bk_enterprise_key" {
			t.Errorf("acme.blocks.ai key should be unchanged, got %q", orgKey.ApiKey)
		}
	}
}

// An explicit Network login must authenticate at Blocks Network, whatever an ambient
// BLOCKS_CDM_URL names. The CDM payload carries api.baseUrl, so an endpoint served by
// an enterprise deployment answers "where is Blocks Network" with that deployment —
// and the login would send the key it minted there and then store it in the profile
// that describes Blocks Network. --network is the explicit-intent flag and already
// outranks an ambient BLOCKS_BACKEND_URL; the assertion here is at the socket, that
// the deployment named only by the environment received nothing, and that its CDM
// endpoint was not even fetched.
func TestAnExplicitNetworkLoginIgnoresAnAmbientCDMEndpoint(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	network := newDeploymentServer(t, false)
	enterprise := newDeploymentServer(t, true)
	networkRemoteConfigAt(t, network.url)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	var cdmFetches atomic.Int64
	t.Setenv(cdm.URLEnv, cdmEndpointServing(t, enterprise.url, &cdmFetches))
	cdm.Reset()

	const secret = "bk_live_secret"
	runLoginArgs(t, "login", "--network", "--api-key", secret, "--no-write-env")

	if len(enterprise.requestsTo) != 0 {
		t.Errorf("the login reached the deployment only %s named: %v", cdm.URLEnv, enterprise.requestsTo)
	}
	for _, sent := range enterprise.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a deployment the user did not ask for: %q", sent)
		}
	}
	if got := cdmFetches.Load(); got != 0 {
		t.Errorf("the ambient CDM endpoint was fetched %d time(s); an explicit Network login must not resolve through it", got)
	}
	if !network.authenticated {
		t.Fatalf("the login never authenticated against Blocks Network; it reached %v", enterprise.requestsTo)
	}
	if got := orgKeyOf(t, mustLoadProfiles(t), profiles.DefaultProfile); got != secret {
		t.Errorf("profile %s key = %q, want the key minted at Blocks Network", profiles.DefaultProfile, got)
	}
	if got, ok := os.LookupEnv(cdm.URLEnv); ok {
		t.Errorf("%s = %q, want it removed from the environment entirely — a delegated runtime inherits it", cdm.URLEnv, got)
	}
}

// The other half of that rule, for the login that names no deployment at all. A bare
// login under an exported BLOCKS_CDM_URL authenticates against the deployment the
// payload's api.baseUrl names — nothing local outranks it and every request follows
// it — so that deployment is what the login has to record: the profile it stores the
// key under, the BaseURL that profile records, and the pin beside the key in .env.
//
// Recording anything else is not a cosmetic mismatch. The key lands in a profile
// describing somewhere else with no pin to send it back, and the credential tiers then
// refuse to spend it at all, so the login leaves the user logged in to nothing.
//
// The assertions run from the socket outwards: which deployment authenticated, then
// what was written about it.
func TestABareLoginRecordsTheDeploymentARedirectedCDMEndpointNames(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	enterprise := newDeploymentServer(t, true)
	// A profile that records no deployment of its own: the stock Blocks Network shape,
	// which is what makes the endpoint the only thing naming a target.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	var cdmFetches atomic.Int64
	t.Setenv(cdm.URLEnv, cdmEndpointServing(t, enterprise.url, &cdmFetches))
	cdm.Reset()

	const secret = "bk_minted_at_the_redirected_deployment"
	projectDir := t.TempDir()
	runLoginArgs(t, "login", "--api-key", secret, "--write-env", "--dir", projectDir)

	if !enterprise.authenticated {
		t.Fatalf("the login never authenticated against the deployment %s names; it asked for %v", cdm.URLEnv, enterprise.requestsTo)
	}
	if cdmFetches.Load() == 0 {
		t.Error("the redirected endpoint was never fetched, so nothing resolved the deployment this case is about")
	}

	store := mustLoadProfiles(t)
	profile := mustHost(t, enterprise.url)
	if got := orgKeyOf(t, store, profile); got != secret {
		t.Errorf("profile %s key = %q, want the key minted at the deployment the login reached", profile, got)
	}
	if got := store.Profiles[profile].BaseURL; !profiles.SameBaseURL(got, enterprise.url) {
		t.Errorf("profile %s base_url = %q, want the deployment the key was minted at (%q)", profile, got, enterprise.url)
	}
	if got := store.Profiles[profiles.DefaultProfile].Orgs; len(got) != 0 {
		t.Errorf("profile %s holds %v; a key minted elsewhere must not land in it", profiles.DefaultProfile, got)
	}

	if got := envValue(t, projectDir, blocksBackendURLEnv); got != enterprise.url {
		t.Errorf(".env pins %q, want the deployment the login authenticated against (%q)", got, enterprise.url)
	}
	if got, want := envValue(t, projectDir, cdm.URLEnv), cdm.EndpointFor(enterprise.url); got != want {
		t.Errorf(".env %s = %q, want that deployment's own endpoint (%q)", cdm.URLEnv, got, want)
	}
	if got := envValue(t, projectDir, blocksAPIKeyEnv); got != secret {
		t.Errorf(".env key = %q, want %q", got, secret)
	}
}

// Regression, and the boundary of the tier above: an instance argument names the
// target outright, so it is settled from what the argument says and nothing else. The
// discriminating assertion is that the ambient endpoint was not fetched even once —
// resolving it would mean the login asked a deployment it is about to set aside where
// to authenticate, which is the hole --network already closed.
func TestAnInstanceLoginResolvesLocallyDespiteAnAmbientCDMEndpoint(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	named := newDeploymentServer(t, true)
	elsewhere := newDeploymentServer(t, true)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})

	var cdmFetches atomic.Int64
	t.Setenv(cdm.URLEnv, cdmEndpointServing(t, elsewhere.url, &cdmFetches))
	cdm.Reset()

	const secret = "bk_minted_at_the_named_instance"
	projectDir := t.TempDir()
	runLoginArgs(t, "login", named.url, "--api-key", secret, "--write-env", "--dir", projectDir)

	if got := cdmFetches.Load(); got != 0 {
		t.Errorf("the ambient CDM endpoint was fetched %d time(s); an instance argument settles the target locally", got)
	}
	if len(elsewhere.requestsTo) != 0 {
		t.Errorf("the login reached the deployment only %s named: %v", cdm.URLEnv, elsewhere.requestsTo)
	}
	if !named.authenticated {
		t.Fatalf("the login never authenticated against the instance it named; it asked for %v", named.requestsTo)
	}
	profile := mustHost(t, named.url)
	if got := orgKeyOf(t, mustLoadProfiles(t), profile); got != secret {
		t.Errorf("profile %s key = %q, want the key minted at the named instance", profile, got)
	}
	if got := envValue(t, projectDir, blocksBackendURLEnv); got != named.url {
		t.Errorf(".env pins %q, want the instance the login named (%q)", got, named.url)
	}
}

// The tier's lower boundary: BLOCKS_CDM_URL holding the build's own default endpoint
// redirects nothing, so a bare login there is still a stock Blocks Network login. It
// records no deployment and pins nothing — freezing the origin the CLI is meant to
// resolve for itself would leave a project pinned to a URL that is only today's answer.
//
// The premise is asserted through clictx, because that is where "the endpoint was
// redirected" is decided; the login reads that verdict rather than keeping a second
// copy of the rule, and this case fails if either side changes its mind alone.
func TestTheBuildsOwnCDMEndpointIsNotADeploymentTheLoginRecords(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	t.Setenv(cdm.URLEnv, cdm.DefaultCDMURL)
	cdm.Reset()
	t.Chdir(t.TempDir())
	resolveCLIContext(t, loginCmd)

	if !clictx.ProfileIsTarget() {
		t.Fatal("premise: the build's own CDM endpoint must leave the active profile as the target")
	}
	if got := resolveLoginDeployment(deploymentChoice{}).url; got != "" {
		t.Errorf("resolveLoginDeployment = %q, want no deployment recorded for stock Blocks Network", got)
	}
	if got := loginBackendPin(deploymentChoice{}); got != "" {
		t.Errorf("loginBackendPin = %q, want no pin for stock Blocks Network", got)
	}
}

// Pressing Enter at the instance prompt is not an answer. Treating it as one
// resolved the login to Blocks Network, silently contradicting the Enterprise
// choice the user had just made.
func TestEnterpriseInstancePromptRefusesAnEmptyAnswer(t *testing.T) {
	setupEnterpriseInstancePrompt(t, "\n   \n")

	got, err := captureStdoutErr(t, promptEnterpriseInstance)
	if err == nil {
		t.Fatalf("an empty answer must not resolve a deployment, got %+v", got)
	}
	if got.explicitNetwork || got.instanceURL != "" {
		t.Fatalf("an empty answer must resolve nothing, got %+v", got)
	}
	for _, want := range []string{"instance URL or short name", "--network"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// The empty answer is re-prompted once, so a stray Enter is recoverable without
// rerunning the command.
func TestEnterpriseInstancePromptRePromptsAfterAnEmptyAnswer(t *testing.T) {
	setupEnterpriseInstancePrompt(t, "\nacme\n")

	got, err := captureStdoutErr(t, promptEnterpriseInstance)
	if err != nil {
		t.Fatalf("promptEnterpriseInstance: %v", err)
	}
	if want := "https://acme." + defaultInstanceDomain; got.instanceURL != want {
		t.Fatalf("instanceURL = %q, want %q", got.instanceURL, want)
	}
	if got.explicitNetwork {
		t.Fatal("an Enterprise answer must not be recorded as an explicit Network choice")
	}
}

func TestEnterpriseInstancePromptErrorsUnderNoInput(t *testing.T) {
	setupEnterpriseInstancePrompt(t, "acme\n")
	noInputMode = true

	if _, err := captureStdoutErr(t, promptEnterpriseInstance); err == nil {
		t.Fatal("expected an error when a prompt is required with --no-input")
	} else if !strings.Contains(err.Error(), "--network") {
		t.Errorf("error should name the flag that answers instead, got: %v", err)
	}
}

// The success line names the deployment the login authenticated against. It is
// the deployment's own product name where discovery supplied one, the profile's
// where that profile describes the deployment reached, and the host otherwise —
// never a brand belonging to an instance the login never touched.
func TestLoginTargetName(t *testing.T) {
	branded := &cliconfig.Config{Enterprise: true, ProductName: "Acme AI Hub"}

	tests := []struct {
		name       string
		override   string
		choice     deploymentChoice
		backendURL string
		disco      *cliconfig.Config
		want       string
	}{
		{
			name:       "the deployment's own discovered product name wins",
			choice:     deploymentChoice{instanceURL: "https://acme.blocks.ai"},
			backendURL: "https://acme.blocks.ai",
			disco:      branded,
			want:       "Acme AI Hub",
		},
		{
			name:       "an explicit Network request is Blocks Network whatever the profile is branded",
			choice:     deploymentChoice{explicitNetwork: true},
			backendURL: "https://api.blocks.ai",
			want:       "Blocks Network",
		},
		{
			name:       "the profile's brand when the login reached the profile's own deployment",
			choice:     deploymentChoice{},
			backendURL: "https://umbrella.blocks.ai",
			want:       "Umbrella Corporation",
		},
		{
			name:       "the host when an ambient backend URL displaced the profile",
			override:   "http://127.0.0.1:8899",
			choice:     deploymentChoice{},
			backendURL: "http://127.0.0.1:8899",
			want:       "127.0.0.1:8899",
		},
		{
			name:       "the host when an instance argument named a deployment the profile does not describe",
			choice:     deploymentChoice{instanceURL: "https://acme.blocks.ai"},
			backendURL: "https://acme.blocks.ai",
			want:       "acme.blocks.ai",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seedEnterpriseProfileForTest(t)
			t.Setenv("BLOCKS_BACKEND_URL", tc.override)
			resolveCLIContext(t, loginCmd)
			branding.Set("Umbrella Corporation")
			t.Cleanup(branding.Reset)

			if got := loginTargetName(tc.choice, tc.backendURL, tc.disco); got != tc.want {
				t.Fatalf("loginTargetName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPromptEnterpriseInstanceEOFReturnsError verifies that pressing Ctrl-D at the
// "Instance URL or short name:" prompt returns an error instead of a Network choice.
// This is the core bug fix: EOF after choosing Enterprise must not silently resolve
// to Network.
func TestPromptEnterpriseInstanceEOFReturnsError(t *testing.T) {
	setupEnterpriseInstancePrompt(t, "") // EOF - empty stdin

	got, err := captureStdoutErr(t, promptEnterpriseInstance)
	if err == nil {
		t.Fatal("EOF at Enterprise instance prompt must return error, not resolve deployment")
	}
	if got.explicitNetwork {
		t.Error("EOF must not resolve to Network choice")
	}
	if got.instanceURL != "" {
		t.Error("EOF must not resolve to any instance URL")
	}
	if !strings.Contains(err.Error(), "instance URL or short name") {
		t.Errorf("error should explain what's needed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--network") {
		t.Errorf("error should mention --network alternative, got: %v", err)
	}
}

// TestPromptEnterpriseInstanceEmptyAnswerRetriesThenErrors verifies that empty
// answers (Enter key) are re-prompted and eventually return the same error as EOF.
// This confirms both paths now agree.
func TestPromptEnterpriseInstanceEmptyAnswerRetriesThenErrors(t *testing.T) {
	setupEnterpriseInstancePrompt(t, "\n\n") // Two empty answers to exceed attempt limit

	_, err := captureStdoutErr(t, promptEnterpriseInstance)
	if err == nil {
		t.Fatal("exhausted attempts at Enterprise instance prompt must return error")
	}
	if !strings.Contains(err.Error(), "instance URL or short name") {
		t.Errorf("error should explain what's needed, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--network") {
		t.Errorf("error should mention --network alternative, got: %v", err)
	}
}

// TestPromptEnterpriseInstanceValidInstanceStillWorks verifies that providing
// a valid instance still works correctly, with profile aliases preferred.
func TestPromptEnterpriseInstanceValidInstanceStillWorks(t *testing.T) {
	// Set up a profile to test alias resolution
	path := filepath.Join(t.TempDir(), "contexts.json")
	if err := os.WriteFile(path, []byte(`{
      "schema_version": 3,
      "active": "blocks-network",
      "profiles": {
        "blocks-network": {"orgs": {}},
        "acme": {"base_url": "https://blocks.acme.com", "enterprise": true, "orgs": {}}
      }
    }`), 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })

	// Test direct host resolution
	setupEnterpriseInstancePrompt(t, "blocks.example.com\n")
	got, err := captureStdoutErr(t, promptEnterpriseInstance)
	if err != nil {
		t.Fatalf("valid instance should resolve without error: %v", err)
	}
	if want := "https://blocks.example.com"; got.instanceURL != want {
		t.Errorf("instanceURL = %q, want %q", got.instanceURL, want)
	}
	if got.profileName != "" {
		t.Errorf("direct host should not match profile, got profileName=%q", got.profileName)
	}

	// Test short name expansion
	setupEnterpriseInstancePrompt(t, "umbrella\n")
	got, err = captureStdoutErr(t, promptEnterpriseInstance)
	if err != nil {
		t.Fatalf("short name should resolve without error: %v", err)
	}
	if want := "https://umbrella." + defaultInstanceDomain; got.instanceURL != want {
		t.Errorf("instanceURL = %q, want %q", got.instanceURL, want)
	}
	if got.profileName != "" {
		t.Errorf("short name should not match profile, got profileName=%q", got.profileName)
	}

	// Test profile alias resolution (should prefer alias over expansion)
	setupEnterpriseInstancePrompt(t, "acme\n")
	got, err = captureStdoutErr(t, promptEnterpriseInstance)
	if err != nil {
		t.Fatalf("profile alias should resolve without error: %v", err)
	}
	if want := "https://blocks.acme.com"; got.instanceURL != want {
		t.Errorf("instanceURL = %q, want %q (should resolve via profile)", got.instanceURL, want)
	}
	if got.profileName != "acme" {
		t.Errorf("profileName = %q, want 'acme'", got.profileName)
	}
}

// runLoginCapturing runs the login command through the root command — so argument
// resolution, target resolution, persistence and messaging all run as in
// production — and returns what it printed and the error it returned.
// Both streams are captured, not just stdout: a targeting decision the CLI makes
// before the command body runs is reported from PersistentPreRun, and that hook writes
// to stderr so it cannot corrupt the commands whose stdout is a machine-readable
// document. A caller asserting on what the user was told has to read both.
func runLoginCapturing(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetLoginFlags()
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	var err error
	out := captureStdoutStderr(func() { err = rootCmd.Execute() })
	return out, err
}

// exportedBackendURL points this invocation at a deployment the way a shell or a CI
// job does: set in the environment, with no project .env behind it. That is the
// provenance the scripted BLOCKS_BACKEND_URL + BLOCKS_API_KEY path has, and it must
// keep outranking the active profile.
func exportedBackendURL(t *testing.T, url string) {
	t.Helper()
	t.Setenv(blocksBackendURLEnv, url)
	if src := envFileSource(blocksBackendURLEnv); src != "" {
		t.Fatalf("premise: an exported value must carry no .env provenance, got %q", src)
	}
}

// pinnedBackendURL points this invocation at a deployment the way a cloned
// repository does: an assignment in the project .env, read by the same loader the
// root command runs at startup, which is what records the provenance the login
// distinguishes.
func pinnedBackendURL(t *testing.T, url string) string {
	t.Helper()
	dir := writeProjectEnv(t, t.TempDir(), blocksBackendURLEnv+"="+url+"\n")
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)
	if envFileSource(blocksBackendURLEnv) == "" {
		t.Fatal("premise: a value read from the project .env must carry its provenance")
	}
	if got := os.Getenv(blocksBackendURLEnv); got != url {
		t.Fatalf("premise: the .env must supply %s=%q, got %q", blocksBackendURLEnv, url, got)
	}
	return dir
}

// A credential belongs to the deployment it was minted at. A login that follows an
// exported backend URL authenticates against that deployment, so that is the profile
// the key belongs in: storing it in whichever profile happened to be active leaves
// that profile holding another deployment's key and — its own BaseURL untouched —
// sending that key to its own deployment as soon as the exported value goes away.
func TestLoginStoresTheKeyUnderTheDeploymentItWasMintedAt(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deploymentA := newDeploymentServer(t, false)
	deploymentB := newDeploymentServer(t, false)
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL:      deploymentA.url,
		DefaultOrgID: "org-a",
		Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_a"}},
	})
	exportedBackendURL(t, deploymentB.url)

	runLoginArgs(t, "login", "--api-key", "bk_minted_at_b", "--no-write-env")

	if !deploymentB.authenticated {
		t.Fatal("the login never reached the deployment the exported backend URL names")
	}
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}

	nameB := mustHost(t, deploymentB.url)
	pb, ok := store.Profiles[nameB]
	if !ok {
		t.Fatalf("no profile for the deployment the key was minted at (%q); have %v", nameB, profileNames(store))
	}
	if got := orgKeyOf(t, store, nameB); got != "bk_minted_at_b" {
		t.Errorf("profile %s key = %q, want bk_minted_at_b", nameB, got)
	}
	if !profiles.SameBaseURL(pb.BaseURL, deploymentB.url) {
		t.Errorf("profile %s base_url = %q, want the deployment the key was minted at (%q)",
			nameB, pb.BaseURL, deploymentB.url)
	}

	pa := store.Profiles["deployment-a"]
	if got := pa.Orgs["org-a"].ApiKey; got != "bk_a" {
		t.Errorf("profile deployment-a key = %q; a key minted elsewhere must not land in it", got)
	}
	if !profiles.SameBaseURL(pa.BaseURL, deploymentA.url) {
		t.Errorf("profile deployment-a base_url = %q, want %q", pa.BaseURL, deploymentA.url)
	}
}

// A BLOCKS_BACKEND_URL that came from a project .env — a file that arrives with a
// cloned repository — is weaker evidence of intent than one the user exported, and
// login is the command that hands a credential to whatever the target turns out to
// be. So a file-sourced value naming a deployment the active profile does not
// describe is not followed, and nothing reaches that host.
func TestLoginDoesNotSendACredentialToAProjectEnvBackendTheProfileDoesNotDescribe(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	profileDeployment := newDeploymentServer(t, false)
	unnamed := newDeploymentServer(t, false)
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: profileDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	pinnedBackendURL(t, unnamed.url)

	out, err := runLoginCapturing(t, "login", "--api-key", "bk_live_secret", "--no-write-env")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if len(unnamed.requestsTo) != 0 {
		t.Errorf("the login reached the deployment only the project .env named: %v", unnamed.requestsTo)
	}
	for _, sent := range unnamed.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}
	if !profileDeployment.authenticated {
		t.Error("the login must fall back to the deployment the active profile records")
	}
	if got := orgKeyOf(t, mustLoadProfiles(t), "deployment-a"); got != "bk_live_secret" {
		t.Errorf("profile deployment-a key = %q, want the key stored against the deployment it was sent to", got)
	}

	// The user has to be able to see which deployment was used, and which was set
	// aside, before deciding to trust the result.
	for _, want := range []string{unnamed.url, mustHost(t, profileDeployment.url), blocksBackendURLEnv} {
		if !strings.Contains(out, want) {
			t.Errorf("login output must name %q:\n%s", want, out)
		}
	}
}

// The complement, and the reason the fix is narrow: an exported BLOCKS_BACKEND_URL
// is the supported way to point a headless login at a deployment, and it still wins
// over the active profile.
func TestLoginFollowsAnExportedBackendURLOverTheActiveProfile(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	profileDeployment := newDeploymentServer(t, false)
	exported := newDeploymentServer(t, false)
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: profileDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	exportedBackendURL(t, exported.url)

	runLoginArgs(t, "login", "--api-key", "bk_scripted", "--no-write-env")

	if !exported.authenticated {
		t.Fatal("an exported backend URL must still decide where a scripted login authenticates")
	}
	if profileDeployment.authenticated {
		t.Error("the login fell back to the profile's deployment; the exported value must win")
	}
}

// The other complement: a project .env pin naming a deployment some saved profile
// describes is honoured for every command, so login has to authenticate against the
// same deployment. Here the pin names deployment B, a profile for B exists — the
// user logged in to it — and a profile for a different deployment is active. That is
// exactly what `blocks login <B> --write-env` followed by `blocks profile use <A>`
// leaves behind: the directory keeps acting on B, the context banner names B, and
// publish and unregister reach B. A login that fell back to the active profile's
// deployment would be the one command in that directory acting somewhere else, and
// would mint a key at a deployment the .env beside it does not name.
func TestLoginFollowsAProjectEnvPinForAnyDeploymentAProfileDescribes(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	activeDeployment := newDeploymentServer(t, false)
	pinnedDeployment := newDeploymentServer(t, false)
	pinnedProfile := hostSlug(pinnedDeployment.url)
	seedProfile(t, pinnedProfile, profiles.Profile{
		BaseURL: pinnedDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	// Seeded last, so this is the active profile — `blocks profile use` after the fact.
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: activeDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	pinnedBackendURL(t, pinnedDeployment.url)

	runLoginArgs(t, "login", "--api-key", "bk_minted_at_pinned", "--no-write-env")

	if !pinnedDeployment.authenticated {
		t.Fatalf("the login never authenticated against the pinned deployment every other command targets; it reached %v instead",
			activeDeployment.requestsTo)
	}
	if len(activeDeployment.requestsTo) != 0 {
		t.Errorf("the login talked to the active profile's deployment instead of the pinned one: %v", activeDeployment.requestsTo)
	}
	for _, sent := range activeDeployment.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a deployment this directory does not target: %q", sent)
		}
	}
	if got := orgKeyOf(t, mustLoadProfiles(t), pinnedProfile); got != "bk_minted_at_pinned" {
		t.Errorf("profile %s key = %q, want the key stored against the deployment it was sent to", pinnedProfile, got)
	}
}

// remoteConfigWithoutAnOAuthClientID makes Blocks Network's own remote config resolve
// to url while naming no OAuth client id.
//
// It is the shape an interactive login case needs. Such a login has no key to hand, so
// it runs the browser flow — which binds a fixed local port, opens a real browser and
// then waits five minutes for a callback, none of which a test may do. Withholding the
// client id stops the login one step earlier, at "OAuth client id must be set", which
// is after the target has been resolved and probed and so leaves the targeting
// decision fully observable at the socket.
//
// The document is written to the cached-config tier under an isolated HOME rather than
// pointed at by BLOCKS_CDM_URL, because a login that resolves Blocks Network drops that
// variable — see networkRemoteConfigAt, whose payload this one differs from only in the
// client id.
func remoteConfigWithoutAnOAuthClientID(t *testing.T, url string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	dir := filepath.Join(home, ".blocks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	document := `{"playground":{"publishKey":"demo","subscribeKey":"demo"},` +
		`"network":{"publishKey":"demo","subscribeKey":"demo"},` +
		`"api":{"baseUrl":"` + url + `","clientId":""}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(document), 0600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	t.Setenv("BLOCKS_CLI_CLIENT_ID", "")
	t.Setenv(cdm.URLEnv, "")
	cdm.Reset()
	t.Cleanup(cdm.Reset)
}

// `blocks logout` deliberately keeps the deployment targeting in the project .env, and
// says so, precisely because a later bare `blocks login` is meant to return to the same
// deployment. This is that return trip, in the state logout leaves behind: the .env
// pins deployment B, a profile for B exists — so the pin is honoured, and every other
// command in this directory reaches B — and the active profile records no deployment of
// its own, which is what `blocks profile use blocks-network` leaves.
//
// A first-login prompt that decides from the active profile alone finds no deployment
// there, asks the question, and takes its default: Blocks Network. Pressing Enter then
// authenticates somewhere the directory does not name and stores the key in a profile
// no later command in this directory resolves — breaking the promise logout printed.
//
// The assertion is at the socket, on which deployment received the login's discovery
// probe, because that is the decision under test: printed text can agree with the
// banner while the request goes elsewhere.
func TestABareLoginReturnsToTheDeploymentThisDirectoryIsPinnedTo(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	// A terminal, because off one the question is not asked at all: TTY state must not
	// be what saves this case.
	forceTTY(t)
	// An answer the prompt would accept, so a prompt that does run resolves rather
	// than blocking — and lands on its Blocks Network default, which is the defect.
	pipeStdin(t, "\n")

	network := newDeploymentServer(t, false)
	pinned := newDeploymentServer(t, false)
	remoteConfigWithoutAnOAuthClientID(t, network.url)

	pinnedProfile := hostSlug(pinned.url)
	seedProfile(t, pinnedProfile, profiles.Profile{
		BaseURL: pinned.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	// Seeded last, so this is the active profile: it records no deployment and holds
	// no key, which is where `blocks profile use blocks-network` and `blocks logout`
	// leave a user whose project directory still pins B.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	pinnedBackendURL(t, pinned.url)

	out, err := runLoginCapturing(t, "login")

	// The login cannot complete without an OAuth client id, and stopping there is the
	// premise: it proves the flow got past target resolution to the credential step
	// rather than failing on the deployment question.
	if err == nil || !strings.Contains(err.Error(), "OAuth client id") {
		t.Fatalf("premise: the login must reach the credential step for the deployment it resolved, got %v\n%s", err, out)
	}
	if pinned.requestsTo["/api/v1/cli-config"] == 0 {
		t.Errorf("the login never probed the deployment this directory is pinned to; it reached %v", network.requestsTo)
	}
	if len(network.requestsTo) != 0 {
		t.Errorf("the login went to Blocks Network instead of the pinned deployment: %v", network.requestsTo)
	}
	for _, sent := range network.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a deployment this directory does not target: %q", sent)
		}
	}
}

// The complement, and the reason the question is not simply deleted: on a genuine
// first run nothing names a deployment — no profile records one and nothing ambient
// names one — and the user must still be asked. Only the prompt records an explicit
// Blocks Network choice, so that is the evidence the question was put.
func TestAGenuineFirstLoginIsStillAskedWhichDeployment(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	forceTTY(t)
	pipeStdin(t, "\n")

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	t.Chdir(t.TempDir())
	resolveCLIContext(t, loginCmd)
	requireNoSuppliedCredential(t)
	if got := clictx.ChosenBackendURL(); got != "" {
		t.Fatalf("premise: nothing may name a deployment on a first run, got %q", got)
	}

	choice, err := promptly(t, "promptDeploymentIfFirstLogin on a first run", promptDeploymentIfFirstLogin)
	if err != nil {
		t.Fatalf("promptDeploymentIfFirstLogin: %v", err)
	}
	if !choice.explicitNetwork {
		t.Errorf("a first run must still be asked which deployment, got %+v", choice)
	}
}

// The --no-input interaction, settled by making the question unreachable rather than by
// answering it. The flag's contract is that a *required* prompt becomes an error naming
// the flag that answers it, and a deployment this invocation already resolved requires
// no prompt — so there is nothing left for the flag to refuse. A first run with nothing
// naming a deployment still refuses, which
// TestNoInputRefusesTheDeploymentQuestionOffATerminalToo pins.
func TestNoInputHasNothingToRefuseOnceTheDeploymentIsResolved(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)

	const pinned = "https://blocks.acme.example"
	seedProfile(t, "acme", profiles.Profile{BaseURL: pinned, Orgs: map[string]profiles.OrgKey{}})
	// Seeded last, so the active profile records no deployment of its own and only the
	// pin names one.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	pinnedBackendURL(t, pinned)
	resolveCLIContext(t, loginCmd)
	requireNoSuppliedCredential(t)
	setNoInputMode(true)

	choice, err := promptly(t, "promptDeploymentIfFirstLogin under --no-input in a pinned directory", promptDeploymentIfFirstLogin)
	if err != nil {
		t.Fatalf("--no-input must not refuse a question that is never asked: %v", err)
	}
	if choice.explicitNetwork || choice.instanceURL != "" {
		t.Errorf("the resolved deployment must be followed, not re-decided, got %+v", choice)
	}
	if got := resolveLoginDeployment(choice).url; got != pinned {
		t.Errorf("resolveLoginDeployment = %q, want the pinned deployment %q", got, pinned)
	}
}

// The state that made every later bare login ask the question again: a *completed* stock
// Blocks Network login. It stores an empty BaseURL, because empty is how "resolve via
// CDM" is recorded, so no origin is named and the choice looked unmade — though it had
// been made, and the cached organization is the record of it. --no-input is the sharp
// end, and the reason this is more than a redundant prompt: a settled deployment became
// an error unless --network was repeated on every single invocation.
func TestACompletedNetworkLoginIsNotAskedWhichDeploymentAgain(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)
	withoutATerminal(t)

	// Empty BaseURL is the stock-Network shape. The cached organization is what separates
	// this from the empty profile first run materializes, which must still be asked —
	// TestAGenuineFirstLoginIsStillAskedWhichDeployment holds that other side.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "org_1",
		Orgs:         map[string]profiles.OrgKey{"org_1": {OrgName: "acme", ApiKey: "bk_cached_key"}},
	})
	t.Chdir(t.TempDir())
	resolveCLIContext(t, loginCmd)
	if got := clictx.ChosenBackendURL(); got != "" {
		t.Fatalf("premise: a stock Network profile names no origin of its own, got %q", got)
	}
	setNoInputMode(true)

	choice, err := promptly(t, "promptDeploymentIfFirstLogin after a completed Network login", promptDeploymentIfFirstLogin)
	if err != nil {
		t.Fatalf("--no-input must not refuse a question a completed login already answered: %v", err)
	}
	if choice.explicitNetwork || choice.instanceURL != "" {
		t.Errorf("a settled deployment must be followed, not re-decided, got %+v", choice)
	}
}

// A pin is honoured because some profile describes the deployment it names, and that
// profile may be an alias rather than the host — `blocks login <url> --profile acme`
// then `blocks profile use` something else. A bare login in that directory belongs in
// the alias: naming the destination after the host instead forks a second profile for
// one deployment and leaves `acme` holding the credential this login replaced, which
// is the key `blocks --profile acme` would go on sending.
func TestABareLoginAfterAPinReusesTheProfileThatDescribesTheDeployment(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	pinnedDeployment := newDeploymentServer(t, false)
	activeDeployment := newDeploymentServer(t, false)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      pinnedDeployment.url,
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Resolved Org", ApiKey: "bk_stale"}},
	})
	// Seeded last, so this is the active profile — `blocks profile use` after the fact.
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: activeDeployment.url,
		Orgs:    map[string]profiles.OrgKey{},
	})
	pinnedBackendURL(t, pinnedDeployment.url)

	runLoginArgs(t, "login", "--api-key", "bk_minted_at_pinned", "--no-write-env")

	store := mustLoadProfiles(t)
	if _, forked := store.Profiles[mustHost(t, pinnedDeployment.url)]; forked {
		t.Errorf("a second profile was created for a deployment %q already describes; have %v", "acme", profileNames(store))
	}
	if got := orgKeyOf(t, store, "acme"); got != "bk_minted_at_pinned" {
		t.Errorf("profile acme key = %q, want the key this login minted — the alias must not be left stale", got)
	}
	if !profiles.SameBaseURL(store.Profiles["acme"].BaseURL, pinnedDeployment.url) {
		t.Errorf("profile acme base_url = %q, want %q", store.Profiles["acme"].BaseURL, pinnedDeployment.url)
	}
	if got := store.Profiles["deployment-a"].Orgs["org-1"].ApiKey; got != "" {
		t.Errorf("profile deployment-a key = %q; a key minted elsewhere must not land in it", got)
	}
}

// The same rule through the argument: a full URL a profile already describes belongs in
// that profile, exactly as its alias does.
func TestAnInstanceURLAProfileDescribesReusesThatProfile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const deployment = "https://blocks.acme.example"
	seedProfile(t, "acme", profiles.Profile{BaseURL: deployment, Orgs: map[string]profiles.OrgKey{}})
	resolveCLIContext(t, loginCmd)

	if got := resolveProfileName(deploymentChoice{instanceURL: deployment}); got != "acme" {
		t.Errorf("resolveProfileName = %q, want acme — no second profile for the same deployment", got)
	}
}

// An instance argument and --network name the target outright, so neither can be
// second-guessed by anything ambient — including a project .env pin.
func TestExplicitTargetsOutrankAProjectEnvBackendPin(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const named = "https://blocks.acme.example"
	const pinned = "https://collector.unnamed.example"
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL: "https://blocks.other.example",
		Orgs:    map[string]profiles.OrgKey{},
	})
	pinnedBackendURL(t, pinned)
	resolveCLIContext(t, loginCmd)

	instance := resolveLoginDeployment(deploymentChoice{instanceURL: named})
	if instance.url != named || instance.network {
		t.Errorf("an instance argument must be used as given, got %+v", instance)
	}
	if got := resolveLoginBackend(deploymentChoice{instanceURL: named}); got != named {
		t.Errorf("resolveLoginBackend = %q, want the named instance %q", got, named)
	}

	network := resolveLoginDeployment(deploymentChoice{explicitNetwork: true})
	if !network.network || network.url != "" {
		t.Errorf("--network must resolve to Blocks Network with nothing ambient reaching it, got %+v", network)
	}
}

// A pin naming the very deployment the active profile records — what
// `blocks login --write-env` leaves behind in its own project directory — displaces
// nothing, so it is still followed and still pinned.
func TestAProjectEnvPinForTheProfilesOwnDeploymentIsStillFollowed(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const deployment = "https://blocks.acme.example"
	seedProfile(t, "acme", profiles.Profile{BaseURL: deployment, Orgs: map[string]profiles.OrgKey{}})
	pinnedBackendURL(t, deployment)
	resolveCLIContext(t, loginCmd)

	d := resolveLoginDeployment(deploymentChoice{})
	if d.url != deployment || d.network {
		t.Errorf("resolveLoginDeployment = %+v, want the profile's own deployment %q", d, deployment)
	}
	if got := loginBackendPin(deploymentChoice{}); got != deployment {
		t.Errorf("loginBackendPin = %q, want %q", got, deployment)
	}
	if got := resolveProfileName(deploymentChoice{}); got != "acme" {
		t.Errorf("resolveProfileName = %q, want acme — no second profile for the same deployment", got)
	}
}

// A pin on its own only redirects the REST origin, and only for a consumer: an agent
// runtime resolves its keysets from the CDM, so a script run directly would talk
// REST to this deployment while subscribed to the public network's keyset. The
// endpoint written must therefore belong to the deployment the key was minted at —
// the same trap as the profile the key is stored under, since the active profile
// here records somewhere else entirely.
func TestWriteEnvPinsTheCDMEndpointOfTheDeploymentTheKeyWasMintedAt(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	deploymentA := newDeploymentServer(t, true)
	deploymentB := newDeploymentServer(t, true)
	seedProfile(t, "deployment-a", profiles.Profile{
		BaseURL:    deploymentA.url,
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	exportedBackendURL(t, deploymentB.url)

	projectDir := t.TempDir()
	runLoginArgs(t, "login", "--api-key", "bk_minted_at_b", "--write-env", "--dir", projectDir)

	if got := envValue(t, projectDir, blocksAPIKeyEnv); got != "bk_minted_at_b" {
		t.Errorf(".env key = %q, want bk_minted_at_b", got)
	}
	if got := envValue(t, projectDir, blocksBackendURLEnv); got != deploymentB.url {
		t.Errorf(".env pins %q, want the deployment the key was minted at (%q)", got, deploymentB.url)
	}
	want := cdm.EndpointFor(deploymentB.url)
	if got := envValue(t, projectDir, cdm.URLEnv); got != want {
		t.Errorf(".env %s = %q, want %q", cdm.URLEnv, got, want)
	}
	if got := envValue(t, projectDir, cdm.URLEnv); got == cdm.EndpointFor(deploymentA.url) {
		t.Errorf("%s names the active profile's deployment, not the one authenticated against", cdm.URLEnv)
	}
}

// The complement: an explicit Network login must leave nothing behind that still
// points at an enterprise deployment. A stale CDM endpoint beside a fresh Network key
// is the same defect as a stale pin — it resolves that deployment's keysets — so both
// go in the rewrite that writes the key.
func TestWriteEnvDropsStaleTargetingWhenNetworkWasChosen(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	const stale = "https://blocks.acme.example"
	projectDir := writeProjectEnv(t, t.TempDir(),
		blocksBackendURLEnv+"="+stale+"\n"+
			cdm.URLEnv+"="+cdm.EndpointFor(stale)+"\n"+
			"OTHER=keep-me\n")

	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	resolveCLIContext(t, loginCmd)

	loginWriteEnv = true
	loginDir = projectDir
	captureStdout(func() {
		if err := maybeWriteEnv("bk_network", deploymentChoice{explicitNetwork: true}); err != nil {
			t.Fatalf("maybeWriteEnv: %v", err)
		}
	})

	if got := envValue(t, projectDir, blocksAPIKeyEnv); got != "bk_network" {
		t.Errorf(".env key = %q, want bk_network", got)
	}
	if got := envValue(t, projectDir, blocksBackendURLEnv); got != "" {
		t.Errorf(".env still pins %q; Blocks Network needs no pin", got)
	}
	if got := envValue(t, projectDir, cdm.URLEnv); got != "" {
		t.Errorf(".env still carries %s=%q; a Network key must not resolve another deployment's keysets", cdm.URLEnv, got)
	}
	if got := envValue(t, projectDir, "OTHER"); got != "keep-me" {
		t.Errorf("unrelated assignment lost: OTHER = %q", got)
	}
}

// A deployment names itself in its own discovery payload, and the login prints that
// name — so the name is somewhere a deployment can put terminal control sequences and
// have the CLI render them: erase the line the login just printed, and rewrite it as a
// login to somewhere else. Nothing the login prints may carry a raw control character.
func TestLoginDoesNotPrintADeploymentsOwnControlSequences(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	// The sequences an attacker-supplied name would carry: erase the line, return to
	// column zero, then rewrite.
	const forged = `Acme\x1b[2K\x0d\x1b[1AHarmless Corporation`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cli-config":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"enterprise":true,"productName":"Acme\u001b[2K\r\u001b[1AHarmless Corporation"}`))
		case "/api/v1/registry/publish-context":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"orgId":"org-1","orgName":"Resolved Org","agentCount":3}`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	out, err := runLoginCapturing(t, "login", srv.URL, "--api-key", "bk_live_secret", "--no-write-env")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, r := range out {
		if r == 0x7f || (r < ' ' && r != '\n') {
			t.Fatalf("login output carries the control character %U from a deployment-supplied name:\n%q", r, out)
		}
	}
	if !strings.Contains(out, forged) {
		t.Errorf("output should show the sequences escaped so tampering stays visible, want %q in:\n%q", forged, out)
	}
}

// mustLoadProfiles reads the isolated profile store or fails the test.
func mustLoadProfiles(t *testing.T) *profiles.Contexts {
	t.Helper()
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	return store
}

// TestOtherPromptEOFDefaultsUnchanged verifies that the three other `!ok` defaults
// remain unchanged. This regression test proves the fix stayed in its lane.
func TestOtherPromptEOFDefaultsUnchanged(t *testing.T) {
	// Set up EOF condition for all prompts
	origStdinScanner := stdinScanner
	stdinScanner = bufio.NewScanner(strings.NewReader(""))
	origIsTTY := isTTY
	isTTY = func() bool { return true }
	t.Cleanup(func() {
		stdinScanner = origStdinScanner
		isTTY = origIsTTY
	})

	// Test 1: shouldWriteEnv -> true (default yes)
	loginWriteEnv = false
	loginNoWriteEnv = false
	loginApiKey = ""
	loginApiKeyStdin = false
	origIsInteractive := isInteractive
	isInteractive = func() bool { return true }
	t.Cleanup(func() { isInteractive = origIsInteractive })

	got, err := shouldWriteEnv()
	if err != nil {
		t.Errorf("shouldWriteEnv EOF should not error: %v", err)
	}
	if !got {
		t.Error("shouldWriteEnv EOF should default to true")
	}

	// Reset scanner for next test
	stdinScanner = bufio.NewScanner(strings.NewReader(""))

	// Test 2: confirmUnregister -> error (refuse to delete)
	if err := confirmUnregister("Test Agent"); err == nil {
		t.Error("confirmUnregister EOF should return error (refuse deletion)")
	}
}

// The whole rule deploymentURL encodes, pinned directly rather than only through
// whatever an end-to-end case happens to pass. The origin this returns has endpoint
// paths appended to it as a string, so anything that ends the authority early — a
// query, a fragment — or names a port nothing can listen on has to be refused here,
// where the argument can still be explained, rather than inside the first request.
func TestDeploymentURLAcceptsOnlyOriginsADeploymentCanHave(t *testing.T) {
	accepted := []struct {
		name string
		in   string
	}{
		{"a bare https origin", "https://blocks.acme.example"},
		{"a custom port", "https://blocks.acme.example:8443"},
		{"the lowest port", "https://blocks.acme.example:1"},
		{"the highest port", "https://blocks.acme.example:65535"},
		{"a path prefix a deployment is served under", "https://blocks.acme.example/blocks"},
		{"a path prefix with a port", "https://blocks.acme.example:8443/blocks"},
		{"http to this machine by name", "http://localhost:3001"},
		{"http to this machine by address", "http://127.0.0.1:3001"},
		{"http to this machine by IPv6 address", "http://[::1]:3001"},
		{"http to this machine by IPv6 address with no port", "http://[::1]"},
		{"https to a loopback address", "https://127.0.0.1:3001"},
		{"https to a global IPv6 address", "https://[2001:db8::1]:8443/blocks"},
	}
	for _, tc := range accepted {
		t.Run("accept "+tc.name, func(t *testing.T) {
			if got := deploymentURL(tc.in); got != tc.in {
				t.Fatalf("deploymentURL(%q) = %q, want it used as given", tc.in, got)
			}
		})
	}

	refused := []struct {
		name string
		in   string
	}{
		{"a query ends the origin", "https://blocks.acme.example?x=1"},
		{"an empty query still ends the origin", "https://blocks.acme.example?"},
		{"a query after a path prefix", "https://blocks.acme.example/blocks?x=1"},
		{"a fragment ends the origin", "https://blocks.acme.example#frag"},
		{"a fragment after a path prefix", "https://blocks.acme.example/blocks#frag"},
		{"a query and a fragment together", "https://blocks.acme.example?x=1#frag"},
		{"a port above the TCP range", "https://blocks.acme.example:99999"},
		{"a port far above the TCP range", "https://blocks.acme.example:4294967296"},
		{"port zero is not an address", "https://blocks.acme.example:0"},
		{"userinfo moves the authority past the @", "https://blocks.acme.example@collector.attacker.example"},
		{"http to a host that is not this machine", "http://blocks.acme.example"},
		{"a scheme that is not http(s)", "ftp://blocks.acme.example"},
		{"no host at all", "https://"},
		// url.Parse checks that brackets are balanced and that any port after them is
		// well formed, and nothing else: it reports the host of https://[x] as x. Every
		// other rule here passed, so the login announced a target and then failed inside
		// its first request against an authority that cannot resolve.
		{"a bracketed host that is not an address", "https://[x]"},
		{"a bracketed host with a port that is not an address", "https://[x]:8443"},
		{"a bracketed host spelled to look like an address", "https://[gggg::1]"},
		{"a bracketed IPv4-style host that is not an address", "https://[999.1.1.1]"},
		{"an empty bracketed host", "https://[]"},
		{"a bracketed loopback with a zone", "http://[fe80::1%25eth0]"},
	}
	for _, tc := range refused {
		t.Run("reject "+tc.name, func(t *testing.T) {
			if got := deploymentURL(tc.in); got != "" {
				t.Fatalf("deploymentURL(%q) = %q, want it refused", tc.in, got)
			}
		})
	}
}

// End to end, at the socket: a query-bearing URL reads as a deployment origin and
// would have every endpoint path appended after the query, so the assertion is that
// the deployment received nothing at all — no probe, and no request carrying the key.
func TestLoginRefusesAURLCarryingAQueryBeforeItReachesTheNetwork(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	// http to a loopback host, so an unguarded login really would complete against
	// this server: everything except the query is a URL this CLI accepts.
	deployment := newDeploymentServer(t, false)
	arg := deployment.url + "?x=1"

	const secret = "bk_live_secret"
	_, err := runLoginCapturing(t, "login", arg, "--api-key", secret, "--no-write-env")

	if len(deployment.requestsTo) != 0 {
		t.Errorf("the login reached the deployment despite the query: %v", deployment.requestsTo)
	}
	for _, sent := range deployment.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to an origin the CLI cannot append a path to: %q", sent)
		}
	}
	if err == nil {
		t.Fatal("a URL carrying a query is not a deployment origin and must fail the login")
	}
	for _, want := range []string{arg, "does not name a deployment", "no query string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	for name, p := range mustLoadProfiles(t).Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused argument must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// The complement for the port rule, which used to be caught only once the login had
// announced its target and begun: an unroutable port has to be refused at validation
// time, and the error has to say what a port may be.
func TestLoginRefusesAnOutOfRangePortBeforeItReachesTheNetwork(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const arg = "https://blocks.acme.example:99999"
	_, err := runLoginCapturing(t, "login", arg, "--api-key", "bk_live_secret", "--no-write-env")
	if err == nil {
		t.Fatal("a port outside 1-65535 must fail the login")
	}
	for _, want := range []string{arg, "does not name a deployment", "1-65535"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

// The same input without the scheme is the same input: a bare host is accepted by
// prefixing https://, so it names the same authority and the same unroutable port, and
// answering it differently would mean one rule with two verdicts. It used to be
// accepted here and fail inside the first request, after the login had announced its
// target — the asymmetry this closes.
func TestLoginRefusesAnOutOfRangePortOnABareHostToo(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const arg = "blocks.acme.example:99999"
	if got, _ := resolveInstanceArg(arg); got != "" {
		t.Errorf("resolveInstanceArg(%q) = %q; the schemed form of the same input is refused", arg, got)
	}
	if got := deploymentURL("https://" + arg); got != "" {
		t.Fatalf("premise: the URL this argument expands to must be refused, got %q", got)
	}

	_, err := runLoginCapturing(t, "login", arg, "--api-key", "bk_live_secret", "--no-write-env")
	if err == nil {
		t.Fatal("a port outside 1-65535 must fail the login whether or not the argument carried a scheme")
	}
	// The wording names the rule the argument broke, not only the forms it already
	// matched: it is a bare host, so "pass a bare host" explains nothing.
	for _, want := range []string{arg, "does not name a deployment", "1-65535"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
	for name, p := range mustLoadProfiles(t).Profiles {
		for orgID, k := range p.Orgs {
			if k.ApiKey != "" {
				t.Errorf("profile %s org %s stored key %q; a refused argument must store nothing", name, orgID, k.ApiKey)
			}
		}
	}
}

// A login that repoints an existing profile must not leave the previous deployment's
// keys in it. `--profile acme` names the destination outright, so a login can land in a
// profile describing a different deployment; the profile's BaseURL is then repointed
// while its cached organization keys still belong to where they were minted. The result
// describes B and holds A's keys — and because the profile *does* describe the target,
// the credential tiers will hand one of A's keys to B, which is the exact
// credential-crossing this PR exists to stop.
func TestRetargetingAProfileDiscardsThePreviousDeploymentsKeys(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	old := newDeploymentServer(t, false)
	newer := newDeploymentServer(t, false)

	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      old.url,
		DefaultOrgID: "org_old",
		Orgs:         map[string]profiles.OrgKey{"org_old": {OrgName: "Old Org", ApiKey: "bk_old_deployment"}},
	})

	p, err := profiles.Load()
	if err != nil {
		t.Fatalf("premise: %v", err)
	}
	if len(p.Profiles["acme"].Orgs) != 1 {
		t.Fatal("premise: the profile must start with a cached key for the old deployment")
	}

	out, err := runRootCapturing(t, "login", newer.url, "--profile", "acme",
		"--api-key", "bk_for_new_deployment", "--no-write-env")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	after, err := profiles.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := after.Profiles["acme"]
	if _, stale := got.Orgs["org_old"]; stale {
		t.Error("the previous deployment's organization key is still cached under this profile")
	}
	for id, k := range got.Orgs {
		if k.ApiKey == "bk_old_deployment" {
			t.Errorf("org %s still holds the old deployment's key", id)
		}
	}
	if got.DefaultOrgID == "org_old" {
		t.Error("the default organization still names the previous deployment's org")
	}
	if !strings.Contains(out, "described a different deployment") {
		t.Errorf("the discard must be reported, not silent; got:\n%s", out)
	}
}

// Two tenants on one host are different deployments with different credentials, so the
// auto-generated profile name has to tell them apart. Naming both after the host alone
// put them in one profile, which then described one tenant while holding the other's keys.
func TestPathPrefixedDeploymentsDoNotShareAGeneratedProfileName(t *testing.T) {
	a := hostSlug("https://blocks.acme.example/tenant-a")
	b := hostSlug("https://blocks.acme.example/tenant-b")
	if a == b {
		t.Errorf("two tenants collapsed to one profile name: %q", a)
	}
	if plain := hostSlug("https://blocks.acme.example"); plain != "blocks.acme.example" {
		t.Errorf("a host-only deployment must keep its host as the name, got %q", plain)
	}
	if trailing := hostSlug("https://blocks.acme.example/"); trailing != "blocks.acme.example" {
		t.Errorf("a bare trailing slash is not a path prefix, got %q", trailing)
	}
}

// The branding half of the same bug. mergeDiscovery overwrites ProductName,
// OAuthClientID and DashboardBaseURL only when discovery answers with a non-empty value,
// so retargeting a profile at a deployment that reports no product name left the previous
// deployment's branding on it. The context banner reads its name from there, which makes
// that the "believing you are on Enterprise while actually on Network" state the banner
// exists to prevent.
func TestRetargetingAProfileDiscardsThePreviousDeploymentsBranding(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetLoginFlags()
	t.Cleanup(resetLoginFlags)

	// The destination reports no enterprise metadata at all (stock-shaped: cli-config 404).
	plain := newDeploymentServer(t, false)

	seedProfile(t, "acme", profiles.Profile{
		BaseURL:          "https://old.acme.example",
		Enterprise:       true,
		ProductName:      "Umbrella Corporation",
		OAuthClientID:    "old-client-id",
		DashboardBaseURL: "https://dashboard.old.acme.example",
		DefaultOrgID:     "org_old",
		Orgs:             map[string]profiles.OrgKey{"org_old": {OrgName: "Old", ApiKey: "bk_old"}},
	})

	out, err := runRootCapturing(t, "login", plain.url, "--profile", "acme",
		"--api-key", "bk_new", "--no-write-env")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}

	after, err := profiles.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := after.Profiles["acme"]
	if got.ProductName == "Umbrella Corporation" {
		t.Error("the previous deployment's product name is still on the profile; the banner would name the wrong product")
	}
	if got.OAuthClientID == "old-client-id" {
		t.Error("the previous deployment's OAuth client id survived the retarget")
	}
	if got.DashboardBaseURL == "https://dashboard.old.acme.example" {
		t.Error("the previous deployment's dashboard origin survived the retarget")
	}
	if got.Enterprise {
		t.Error("the previous deployment's enterprise verdict survived a retarget to a non-enterprise deployment")
	}
}

// The alias branch skips the *argument* rules on purpose — an alias is a saved name, not
// an address someone typed. The origin rule is a different question and does apply: the
// stored URL is about to receive OAuth codes and an API key, and a profile saved before a
// rule tightened, or edited by hand, can hold cleartext HTTP or a deceptive authority.
// The origin-validation rollout covered environment, profile, linker and CDM tiers and
// missed exactly this one.
func TestASavedAliasWithABadOriginIsRefusedBeforeAnythingIsSent(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	for _, tc := range []struct{ name, base, want string }{
		{"remote cleartext http", "http://backend.acme.example", "https:"},
		{"userinfo authority", "https://trusted.example@collector.example", "username or password"},
		// origin.Validate establishes this reason itself rather than letting url.Parse decide,
		// because whether the parser rejects a bracketed non-IP host varies by Go release. It
		// used to depend on the toolchain, which is why this assertion passed locally and
		// failed in CI.
		{"bracketed host that is not an IP", "https://[x]", "invalid host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedProfile(t, "acme", profiles.Profile{BaseURL: tc.base, Orgs: map[string]profiles.OrgKey{}})

			url, matched, err := expandInstanceArg("acme")
			if err == nil {
				t.Fatalf("expandInstanceArg(alias) = %q/%q, want a refusal", url, matched)
			}
			if url != "" {
				t.Errorf("a refused alias must not also resolve, got %q", url)
			}
			if !strings.Contains(err.Error(), "acme") {
				t.Errorf("the refusal must name the profile, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal must say why: got %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A legitimate alias still resolves, path prefix and custom port included.
	seedProfile(t, "good", profiles.Profile{BaseURL: "https://blocks.acme.example:8443/tenant-a", Orgs: map[string]profiles.OrgKey{}})
	url, matched, err := expandInstanceArg("good")
	if err != nil {
		t.Fatalf("a valid alias must resolve: %v", err)
	}
	if url != "https://blocks.acme.example:8443/tenant-a" || matched != "good" {
		t.Errorf("expandInstanceArg = %q/%q, want the stored URL and the alias name", url, matched)
	}
}

// The retarget cleanup must not be gated on cached keys. A logged-out profile has none but
// keeps its enterprise verdict, product name, OAuth client id and dashboard origin, and
// discovery only overwrites each when it answers with a non-empty value — so gating on
// keys let a logged-out profile carry deployment A's branding into deployment B.
func TestRetargetingALoggedOutProfileStillDiscardsItsMetadata(t *testing.T) {
	loggedOut := profiles.Profile{
		BaseURL:          "https://old.acme.example",
		Enterprise:       true,
		ProductName:      "Umbrella Corporation",
		OAuthClientID:    "old-client-id",
		DashboardBaseURL: "https://dashboard.old.acme.example",
		Orgs:             map[string]profiles.OrgKey{}, // logout cleared the keys
	}
	if !retargetsAnotherDeployment(loggedOut, deploymentChoice{instanceURL: "https://new.acme.example"}) {
		t.Error("a logged-out profile pointed at another deployment still has metadata to discard")
	}

	// Nothing deployment-scoped at all is genuinely not a retarget.
	fresh := profiles.Profile{Orgs: map[string]profiles.OrgKey{}}
	if retargetsAnotherDeployment(fresh, deploymentChoice{instanceURL: "https://new.acme.example"}) {
		t.Error("a profile with nothing cached has nothing to discard and must not be reported")
	}
}
