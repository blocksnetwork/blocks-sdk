package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func TestResolveWebappBackendURL_Precedence(t *testing.T) {
	const asset = "https://app.blocks.ai"

	t.Run("flag wins over everything", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "https://env.example.test")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://profile.example.test", Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := resolveWebappBackendURL("https://flag.example.test", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://flag.example.test" {
			t.Fatalf("got %q, want the --backend-url flag value", got)
		}
	})

	t.Run("env wins over profile", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "https://env.example.test")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://profile.example.test", Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://env.example.test" {
			t.Fatalf("got %q, want BLOCKS_BACKEND_URL", got)
		}
	})

	t.Run("active profile BaseURL wins over asset default", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://blocks.acme.com" {
			t.Fatalf("got %q, want the active profile BaseURL", got)
		}
	})

	t.Run("ldflag default wins over asset base when no flag/env/profile", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		orig := defaultBackendURL
		defaultBackendURL = "https://packaged.example.test"
		defer func() { defaultBackendURL = orig }()
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://packaged.example.test" {
			t.Fatalf("got %q, want the ldflag defaultBackendURL", got)
		}
	})

	t.Run("falls back to asset base for stock users", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		// Default profile has empty BaseURL (stock Blocks).
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != asset {
			t.Fatalf("got %q, want the asset base (stock default)", got)
		}
	})
}

func TestResolveWebappBackendURL_TrimsTrailingSlash(t *testing.T) {
	const asset = "https://app.blocks.ai"

	t.Run("flag value has its trailing slash stripped", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		got, err := resolveWebappBackendURL("https://blocks.acme.com/", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://blocks.acme.com" {
			t.Fatalf("got %q, want the flag value with no trailing slash", got)
		}
	})

	t.Run("multiple trailing slashes are all stripped", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		got, err := resolveWebappBackendURL("https://blocks.acme.com///", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://blocks.acme.com" {
			t.Fatalf("got %q, want all trailing slashes stripped", got)
		}
	})

	t.Run("env value has its trailing slash stripped", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "https://env.example.test/")
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://env.example.test" {
			t.Fatalf("got %q, want the env value with no trailing slash", got)
		}
	})

	t.Run("profile BaseURL has its trailing slash stripped", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://blocks.acme.com/", Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := resolveWebappBackendURL("", asset)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://blocks.acme.com" {
			t.Fatalf("got %q, want the profile BaseURL with no trailing slash", got)
		}
	})
}

func TestCurrentIntendedBackendURL(t *testing.T) {
	t.Run("env first", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "https://env.example.test")
		got, err := currentIntendedBackendURL()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://env.example.test" {
			t.Fatalf("got %q, want env", got)
		}
	})
	t.Run("profile second, empty when neither", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		got, err := currentIntendedBackendURL()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty (intent unknown)", got)
		}
	})
	t.Run("ldflag default when no env/profile", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		orig := defaultBackendURL
		defaultBackendURL = "https://packaged.example.test"
		defer func() { defaultBackendURL = orig }()
		got, err := currentIntendedBackendURL()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "https://packaged.example.test" {
			t.Fatalf("got %q, want the ldflag defaultBackendURL", got)
		}
	})
}

// TestBackendResolvedFromExplicitSource_MatchesResolver asserts the two public
// helpers stay in lockstep: backendResolvedFromExplicitSource must report true
// exactly when resolveWebappBackendURL picks a non-asset (explicit) tier. Both
// derive from the single resolveWebappBackend resolver, so this guards against
// the precedence order drifting between them.
func TestBackendResolvedFromExplicitSource_MatchesResolver(t *testing.T) {
	const asset = "https://app.blocks.ai"

	setEnv := func(t *testing.T, v string) { t.Helper(); t.Setenv("BLOCKS_BACKEND_URL", v) }
	setProfile := func(t *testing.T, base string) {
		t.Helper()
		p := profiles.Profile{Orgs: map[string]profiles.OrgKey{}}
		if base != "" {
			p.BaseURL = base
		}
		_ = profiles.Upsert(profiles.DefaultProfile, p, true)
	}
	setLdflag := func(t *testing.T, v string) {
		t.Helper()
		orig := defaultBackendURL
		defaultBackendURL = v
		t.Cleanup(func() { defaultBackendURL = orig })
	}

	cases := []struct {
		name         string
		flag         string
		env          string
		profileBase  string
		ldflag       string
		wantExplicit bool
	}{
		{"flag is explicit", "https://flag.example.test", "", "", "", true},
		{"env is explicit", "", "https://env.example.test", "", "", true},
		{"profile is explicit", "", "", "https://profile.example.test", "", true},
		{"ldflag is explicit", "", "", "", "https://packaged.example.test", true},
		{"asset fallback is not explicit", "", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer isolateProfiles(t)()
			setEnv(t, tc.env)
			setProfile(t, tc.profileBase)
			setLdflag(t, tc.ldflag)

			gotURL, err := resolveWebappBackendURL(tc.flag, asset)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			gotExplicit := backendResolvedFromExplicitSource(tc.flag)

			if gotExplicit != tc.wantExplicit {
				t.Fatalf("backendResolvedFromExplicitSource = %v, want %v", gotExplicit, tc.wantExplicit)
			}
			isAsset := gotURL == asset
			if tc.wantExplicit && isAsset {
				t.Fatalf("resolved %q equals asset base but source was reported explicit", gotURL)
			}
			if !tc.wantExplicit && !isAsset {
				t.Fatalf("resolved %q is not the asset base but source was reported non-explicit", gotURL)
			}
		})
	}
}

// TestResolveWebappBackend_SurfacesProfileError proves a real profiles.Active()
// error (here: a --profile/BLOCKS_PROFILE naming a profile that doesn't exist)
// is returned rather than silently swallowed and fallen through to a different
// tier. A swallowed error would bake the wrong backend origin into app.js — the
// exact footgun this feature exists to prevent.
func TestResolveWebappBackend_SurfacesProfileError(t *testing.T) {
	defer isolateProfiles(t)()
	// No flag, no env: resolution reaches the profile tier.
	t.Setenv("BLOCKS_BACKEND_URL", "")
	// Name a profile that the (empty, isolated) store does not contain, so
	// profiles.Active() returns "profile %q not found".
	t.Setenv("BLOCKS_PROFILE", "does-not-exist")

	_, _, err := resolveWebappBackend("", "https://app.blocks.ai")
	if err == nil {
		t.Fatal("expected resolveWebappBackend to surface the profile error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should name the missing profile; got: %v", err)
	}
}

// TestResolveWebappBackend_NoErrorOnStockProfile confirms the common path (stock
// profile, empty BaseURL) still returns nil error and falls back cleanly — a
// missing profile is an error, but "profile exists with no BaseURL" is not.
func TestResolveWebappBackend_NoErrorOnStockProfile(t *testing.T) {
	defer isolateProfiles(t)()
	t.Setenv("BLOCKS_BACKEND_URL", "")
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		Orgs: map[string]profiles.OrgKey{},
	}, true)

	got, _, err := resolveWebappBackend("", "https://app.blocks.ai")
	if err != nil {
		t.Fatalf("stock profile must not error; got: %v", err)
	}
	if got != "https://app.blocks.ai" {
		t.Fatalf("got %q, want asset-base fallback", got)
	}
}

// TestResolveWebappAssetBase covers the asset-host mirror: an explicit
// --blocks-base-url flag always wins (trailing-slash normalized); otherwise the
// asset host follows the already-resolved backend origin so the two cannot drift.
func TestResolveWebappAssetBase(t *testing.T) {
	cases := []struct {
		name               string
		assetFlag          string
		resolvedBackendURL string
		want               string
	}{
		{"flag wins over backend", "https://cdn.acme.com", "https://api.acme.com", "https://cdn.acme.com"},
		{"flag trailing slash stripped", "https://cdn.acme.com/", "https://api.acme.com", "https://cdn.acme.com"},
		{"no flag mirrors backend", "", "https://api.acme.com", "https://api.acme.com"},
		{"no flag mirrors stock backend", "", "https://app.blocks.ai", "https://app.blocks.ai"},
		{"whitespace flag treated as unset", "   ", "https://api.acme.com", "https://api.acme.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveWebappAssetBase(tc.assetFlag, tc.resolvedBackendURL)
			if got != tc.want {
				t.Fatalf("resolveWebappAssetBase(%q, %q) = %q, want %q", tc.assetFlag, tc.resolvedBackendURL, got, tc.want)
			}
		})
	}
}

// TestResolveWebappURLs proves the single seam both init paths use: the asset
// host and backend URL are resolved from one precedence source. Stock users
// (empty profile, no flag/env) must still get https://app.blocks.ai for BOTH,
// enterprise profiles must get the profile origin for BOTH, and --blocks-base-url
// must override only the asset host while the backend still resolves normally.
func TestResolveWebappURLs(t *testing.T) {
	const stock = "https://app.blocks.ai"

	t.Run("stock user gets app.blocks.ai for both", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		asset, backend, err := resolveWebappURLs("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset != stock || backend != stock {
			t.Fatalf("asset=%q backend=%q, want both %q", asset, backend, stock)
		}
	})

	t.Run("enterprise profile drives asset AND backend", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://blocks.pubnub.example", Orgs: map[string]profiles.OrgKey{},
		}, true)
		asset, backend, err := resolveWebappURLs("", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset != "https://blocks.pubnub.example" {
			t.Fatalf("asset = %q, want the enterprise profile origin", asset)
		}
		if backend != "https://blocks.pubnub.example" {
			t.Fatalf("backend = %q, want the enterprise profile origin", backend)
		}
	})

	t.Run("--blocks-base-url overrides asset only", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			BaseURL: "https://blocks.pubnub.example", Orgs: map[string]profiles.OrgKey{},
		}, true)
		asset, backend, err := resolveWebappURLs("", "https://cdn.pubnub.example")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset != "https://cdn.pubnub.example" {
			t.Fatalf("asset = %q, want the --blocks-base-url override", asset)
		}
		if backend != "https://blocks.pubnub.example" {
			t.Fatalf("backend = %q, want the profile origin (unaffected by --blocks-base-url)", backend)
		}
	})

	t.Run("--backend-url drives asset via mirror", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		asset, backend, err := resolveWebappURLs("https://api.split.example", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset != "https://api.split.example" || backend != "https://api.split.example" {
			t.Fatalf("asset=%q backend=%q, want both the --backend-url origin", asset, backend)
		}
	})

	t.Run("trailing-slash --blocks-base-url yields equal trimmed origins", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
			Orgs: map[string]profiles.OrgKey{},
		}, true)
		asset, backend, err := resolveWebappURLs("", "https://cdn.acme.com/")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if asset != "https://cdn.acme.com" {
			t.Fatalf("asset = %q, want trailing slash stripped", asset)
		}
		if backend != "https://cdn.acme.com" {
			t.Fatalf("backend = %q, want the trimmed asset base as last-tier fallback", backend)
		}
	})

	t.Run("surfaces profile resolution error", func(t *testing.T) {
		defer isolateProfiles(t)()
		t.Setenv("BLOCKS_BACKEND_URL", "")
		t.Setenv("BLOCKS_PROFILE", "does-not-exist")
		_, _, err := resolveWebappURLs("", "")
		if err == nil {
			t.Fatal("expected resolveWebappURLs to surface the profile error, got nil")
		}
	})
}

// These four cases share one setup and differ only in how — or whether — the
// environment names a CDM endpoint, because that is the variable under test. The CDM
// payload carries api.baseUrl, so it is the last tier of the backend precedence: a
// profile that records no deployment of its own (the stock Blocks Network shape)
// resolves its backend from it. Resolving that tier at the call site, after clictx had
// already matched the profile, chosen the credential, decided the enterprise verdict
// and named the deployment in the banner, is how the deployment a request reached came
// to differ from the one every printed line described.
//
// They drive the real command, and they assert at the socket — which deployment
// received an Authorization header — because printed text is exactly what was already
// consistent while the request went elsewhere.

// cdmTargetFixture is the shared world: a deployment standing in for Blocks Network,
// a separate enterprise deployment, a stock profile holding a key minted at the former,
// and a local CDM cache naming the former as the API origin. What differs per case is
// whether BLOCKS_CDM_URL points at the enterprise deployment's own CDM endpoint.
type cdmTargetFixture struct {
	network    *deploymentServer
	enterprise *deploymentServer
	// enterpriseCDM is the enterprise deployment's own CDM endpoint, reporting that
	// deployment as the API origin — the value a .env or an export would carry.
	enterpriseCDM string
	// cdmFetches counts fetches of enterpriseCDM.
	cdmFetches *atomic.Int64
}

func newCDMTargetFixture(t *testing.T) *cdmTargetFixture {
	t.Helper()
	restoreCLIState(t)
	isolateCredentials(t)
	t.Cleanup(isolateProfiles(t))
	isolateAmbientState(t)

	f := &cdmTargetFixture{
		network:    newDeploymentServer(t, false),
		enterprise: newDeploymentServer(t, true),
		cdmFetches: new(atomic.Int64),
	}
	f.enterpriseCDM = cdmEndpointServing(t, f.enterprise.url, f.cdmFetches)
	// Blocks Network's own remote config, cached on disk. It answers only while nothing
	// names an endpoint, which is what makes it the control for these cases.
	networkRemoteConfigAt(t, f.network.url)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "org-net",
		Orgs:         map[string]profiles.OrgKey{"org-net": {OrgName: "Network Org", ApiKey: "bk_network"}},
	})
	return f
}

// publishPrivately runs a real, non-interactive `blocks publish` of a valid card and
// returns everything it printed plus the error it ended with.
func publishPrivately(t *testing.T, extraArgs ...string) (string, error) {
	t.Helper()
	cardPath := filepath.Join(writeValidProject(t), "agent-card.json")
	resetPublishFlags()
	t.Cleanup(resetPublishFlags)
	origVersion := Version
	Version = "dev"
	t.Cleanup(func() { Version = origVersion })

	args := append([]string{"publish", cardPath, "--listing", "private"}, extraArgs...)
	var err error
	out := captureStdout(func() {
		rootCmd.SetArgs(args)
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		err = rootCmd.Execute()
	})
	return out, err
}

// credentialsSentTo reports every non-empty Authorization header a deployment received.
// It is the assertion these cases turn on: whether a key left for a deployment is a
// fact about the wire, not about what the CLI said it was doing.
func credentialsSentTo(d *deploymentServer) []string {
	var sent []string
	for _, a := range d.authorizations {
		if a != "" {
			sent = append(sent, a)
		}
	}
	return sent
}

// An exported BLOCKS_CDM_URL is authoritative by design — it is how a headless run
// names its configuration source — so the deployment its payload points at is the
// deployment the request reaches. What must not survive that is the rest of the
// resolution still describing Blocks Network: the stock profile counted as the target,
// so the key it holds was selected and posted to the enterprise deployment. A key
// minted at one deployment may not be sent to another.
func TestARedirectedCDMEndpointNeverReceivesTheProfilesNetworkKey(t *testing.T) {
	f := newCDMTargetFixture(t)
	t.Setenv(cdm.URLEnv, f.enterpriseCDM)
	cdm.Reset()

	// Every prompt is answered by a flag, so nothing but the target resolution can
	// decide whether this publish completes.
	_, err := publishPrivately(t, "--billing-mode", "free")

	// Reported rather than fatal, so the assertion that actually matters — what
	// crossed the wire — is still reached and named in the failure.
	if err == nil {
		t.Error("publish must not proceed with a key minted at a deployment it is not calling")
	}
	if sent := credentialsSentTo(f.enterprise); len(sent) != 0 {
		t.Errorf("the enterprise deployment received %v; the stock profile's key must never reach it", sent)
	}
	if sent := credentialsSentTo(f.network); len(sent) != 0 {
		t.Errorf("Blocks Network received %v; the request was not going there either", sent)
	}
	if n := f.enterprise.requestsTo["/api/v1/registry/agents"]; n != 0 {
		t.Errorf("%d publish request(s) reached the enterprise registry, want 0", n)
	}
	// The error has to name the deployment a key is needed for, or a user with keys for
	// both deployments cannot tell which one to supply.
	if host := mustHost(t, f.enterprise.url); err != nil && !strings.Contains(err.Error(), host) {
		t.Errorf("error %q must name the deployment a credential is needed for (%s)", err, host)
	}
}

// The supported headless shape of the same configuration: the caller supplies a key for
// the deployment the CDM endpoint names. The request then goes out, and the banner has
// to name the deployment it went to. Naming the profile instead is the failure this
// whole resolution exists to prevent — it reads as confirmation that a private publish
// landed on Blocks Network when it landed on someone's enterprise instance.
func TestTheBannerNamesTheDeploymentARedirectedCDMEndpointSendsTheRequestTo(t *testing.T) {
	f := newCDMTargetFixture(t)
	t.Setenv(cdm.URLEnv, f.enterpriseCDM)
	t.Setenv(blocksAPIKeyEnv, "bk_minted_at_enterprise")
	cdm.Reset()

	out, err := publishPrivately(t, "--billing-mode", "free")
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}

	if got, want := credentialsSentTo(f.enterprise), "Bearer bk_minted_at_enterprise"; len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("the enterprise deployment received %v, want %q", got, want)
	}
	if sent := credentialsSentTo(f.network); len(sent) != 0 {
		t.Errorf("Blocks Network received %v; nothing was addressed to it", sent)
	}
	host := mustHost(t, f.enterprise.url)
	if !strings.Contains(out, "["+host+" ") {
		t.Errorf("the banner must name the deployment the publish reached (%s):\n%s", host, out)
	}
	if strings.Contains(out, "["+profiles.DefaultProfile) || strings.Contains(out, "Network Org") {
		t.Errorf("the banner names the profile rather than the deployment the publish reached:\n%s", out)
	}
	// The verdict gates behaviour, not just wording — it forces free billing and drops
	// the marketplace lines — so it too must describe the deployment being called.
	if !clictx.Enterprise() {
		t.Error("the enterprise verdict must be taken from the deployment the request reached")
	}
}

// The control. With no endpoint named, the CDM payload is simply how a stock profile
// finds its own backend: the profile is still the target, its key is still the one to
// send, and nothing extra is asked of the network — in particular no discovery probe,
// since a target the user did not pick cannot be an enterprise deployment.
func TestWithNoRedirectedCDMEndpointTheStockProfilePublishesAsBefore(t *testing.T) {
	f := newCDMTargetFixture(t)
	t.Setenv(cdm.URLEnv, "")
	cdm.Reset()

	out, err := publishPrivately(t, "--billing-mode", "free")
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}

	if got, want := credentialsSentTo(f.network), "Bearer bk_network"; len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("Blocks Network received %v, want %q", got, want)
	}
	if n := f.network.requestsTo["/api/v1/registry/agents"]; n != 1 {
		t.Errorf("%d publish request(s) reached Blocks Network, want 1", n)
	}
	if n := f.network.requestsTo["/api/v1/cli-config"]; n != 0 {
		t.Errorf("%d discovery probe(s); a target the user did not pick must not cost a round-trip", n)
	}
	if got := f.cdmFetches.Load(); got != 0 {
		t.Errorf("the endpoint nothing named was fetched %d time(s)", got)
	}
	if sent := credentialsSentTo(f.enterprise); len(sent) != 0 {
		t.Errorf("the enterprise deployment received %v", sent)
	}
	if !strings.Contains(out, "["+profiles.DefaultProfile+" / Network Org]") {
		t.Errorf("the banner must still name the profile that is the target:\n%s", out)
	}
}

// A BLOCKS_CDM_URL that only a project .env supplied is not intent — that file arrives
// with a cloned repository — and the caller drops it before anything can act on it. It
// therefore never becomes the target: the stock profile is still the target, its key is
// still sent to Blocks Network, and the endpoint the file named is never fetched. This
// is the gate the previous three cases sit behind, so it is asserted directly rather
// than assumed.
func TestAProjectFileCDMEndpointNeverBecomesTheTarget(t *testing.T) {
	f := newCDMTargetFixture(t)
	t.Chdir(writeProjectEnv(t, t.TempDir(), cdm.URLEnv+"="+f.enterpriseCDM+"\n"))
	loadProjectEnv(t, cdm.URLEnv, blocksBackendURLEnv, blocksAPIKeyEnv)
	if envFileSource(cdm.URLEnv) == "" {
		t.Fatalf("premise: a value read from the project .env must carry its provenance")
	}
	cdm.Reset()

	out, err := publishPrivately(t, "--billing-mode", "free")
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}

	if got, ok := os.LookupEnv(cdm.URLEnv); ok {
		t.Errorf("%s = %q, want it dropped before it could name the target", cdm.URLEnv, got)
	}
	if got := f.cdmFetches.Load(); got != 0 {
		t.Errorf("the declined endpoint was fetched %d time(s); the decline must run first", got)
	}
	if got, want := credentialsSentTo(f.network), "Bearer bk_network"; len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("Blocks Network received %v, want %q — a declined pin must not withhold the profile's key", got, want)
	}
	if sent := credentialsSentTo(f.enterprise); len(sent) != 0 {
		t.Errorf("the enterprise deployment received %v", sent)
	}
}

// An unreadable credential store is not an absent login. loadCredentials folded
// StoreErr into "not logged in — run 'blocks login' first", which invites the caller to
// mint a second key against a store that will fail to record it, and told invite
// commands to report a local storage failure as a missing login. resolvePublishApiKey
// already reported it; the two mappers read the same Credential and must agree.
func TestLoadCredentialsReportsAnUnreadableStore(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	boom := errors.New("contexts.json: permission denied")
	clictx.Reset()
	t.Cleanup(clictx.Reset)
	clictx.Resolve(&clictx.Overrides{
		StoredCredential: func() (string, bool, error) { return "", false, boom },
	})

	_, err := loadCredentials()
	if err == nil {
		t.Fatal("an unreadable credential store must be an error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying cause must be wrapped, got %v", err)
	}
	if strings.Contains(err.Error(), "not logged in") {
		t.Errorf("a storage failure must not be reported as a missing login: %v", err)
	}

	// The two mappers read the same Credential, so they must agree on this arm.
	if _, perr := resolvePublishApiKey(); perr == nil || !errors.Is(perr, boom) {
		t.Errorf("resolvePublishApiKey must report the same cause, got %v", perr)
	}
}
