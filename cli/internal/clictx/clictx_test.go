package clictx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// seedProfiles points the profile store at a temp contexts.json holding the
// given profiles, with `active` set to activeName.
func seedProfiles(t *testing.T, activeName string, ps map[string]profiles.Profile) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contexts.json")
	data, err := json.Marshal(profiles.Contexts{
		SchemaVersion: 3,
		Active:        activeName,
		Profiles:      ps,
	})
	if err != nil {
		t.Fatalf("marshal contexts: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return path, nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig; Reset() })
}

func enterpriseProfile() profiles.Profile {
	return profiles.Profile{
		BaseURL:      "https://umbrella.blocks.ai",
		Enterprise:   true,
		ProductName:  "Umbrella Corporation",
		DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{
			"org-1": {OrgName: "Engineering", ApiKey: "bk_test"},
		},
	}
}

func TestResolveMakesNoNetworkCallWhenProfileRecordsEnterprise(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
	}))
	t.Cleanup(srv.Close)

	// The profile records a reachable deployment, so any round-trip the resolver
	// made would show up as a call here.
	p := enterpriseProfile()
	p.BaseURL = srv.URL
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{"umbrella.blocks.ai": p})

	Resolve(nil)

	if calls != 0 {
		t.Fatalf("Resolve must not hit the network, got %d call(s)", calls)
	}
	if !Enterprise() {
		t.Fatal("Enterprise() should be true from the profile alone")
	}
	if calls != 0 {
		t.Fatalf("Enterprise() must not hit the network when the profile records it, got %d", calls)
	}
	if got := Profile(); got != "umbrella.blocks.ai" {
		t.Fatalf("Profile() = %q", got)
	}
	if got := Org(); got != "Engineering" {
		t.Fatalf("Org() = %q", got)
	}
}

func TestEnterpriseFallsBackToDiscoveryOnceAndMemoizes(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/cli-config" {
			calls++
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// Custom backend recorded, but the profile does NOT say enterprise — this is
	// the scripted --api-key case that never ran login.
	p := enterpriseProfile()
	p.Enterprise = false
	p.BaseURL = srv.URL
	seedProfiles(t, "custom", map[string]profiles.Profile{"custom": p})

	Resolve(nil)
	if calls != 0 {
		t.Fatalf("Resolve must be local-only, got %d call(s)", calls)
	}

	if !Enterprise() {
		t.Fatal("Enterprise() should be true from discovery")
	}
	if calls != 1 {
		t.Fatalf("discovery should fire once, got %d", calls)
	}
	// Memoized: a second read must not re-fetch.
	_ = Enterprise()
	_ = Enterprise()
	if calls != 1 {
		t.Fatalf("Enterprise() must memoize, got %d call(s)", calls)
	}
}

func TestEnterpriseSkipsDiscoveryOnStockNetwork(t *testing.T) {
	// No BaseURL and not enterprise → cannot be enterprise, so no round-trip.
	seedProfiles(t, "blocks-network", map[string]profiles.Profile{
		"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
	})

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	Resolve(&Overrides{DefaultBackendURL: srv.URL})
	if Enterprise() {
		t.Fatal("stock Network must not be enterprise")
	}
	if calls != 0 {
		t.Fatalf("stock Network must skip discovery, got %d call(s)", calls)
	}
}

func TestBannerFormatAndEmptyCases(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	Resolve(nil)
	if got, want := Banner(), "[umbrella.blocks.ai / Engineering]"; got != want {
		t.Fatalf("Banner() = %q, want %q", got, want)
	}

	// No resolvable org → no half-empty banner.
	Reset()
	seedProfiles(t, "blocks-network", map[string]profiles.Profile{
		"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
	})
	Resolve(nil)
	if got := Banner(); got != "" {
		t.Fatalf("Banner() with no org = %q, want empty", got)
	}
}

func TestBannerOnNetworkProfile(t *testing.T) {
	// The banner is deliberately NOT enterprise-gated.
	seedProfiles(t, "blocks-network", map[string]profiles.Profile{
		"blocks-network": {
			DefaultOrgID: "org-9",
			Orgs:         map[string]profiles.OrgKey{"org-9": {OrgName: "Acme Inc", ApiKey: "bk_x"}},
		},
	})
	Resolve(nil)
	if got, want := Banner(), "[blocks-network / Acme Inc]"; got != want {
		t.Fatalf("Banner() = %q, want %q", got, want)
	}
}

func TestBannerNamesTheEffectiveBackendNotTheActiveProfile(t *testing.T) {
	// The active profile targets deployment A as Org A. An ambient backend URL and
	// key — the shape a project .env produces — point the request at deployment B.
	// Naming A, or Org A, would describe a deployment the command will not touch.
	seedProfiles(t, "deployment-a", map[string]profiles.Profile{
		"deployment-a": {
			BaseURL:      "https://a.blocks.ai",
			Enterprise:   true,
			DefaultOrgID: "org-a",
			Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_a"}},
		},
	})

	Resolve(&Overrides{BackendURL: "https://b.blocks.ai", EnvCredential: "bk_b"})

	banner := Banner()
	if strings.Contains(banner, "Org A") {
		t.Errorf("Banner() = %q, must not attribute the active profile's org", banner)
	}
	if strings.Contains(banner, "deployment-a") || strings.Contains(banner, "a.blocks.ai") {
		t.Errorf("Banner() = %q, must not name the deployment the profile records", banner)
	}
	if !strings.Contains(banner, "b.blocks.ai") {
		t.Errorf("Banner() = %q, want the backend the request will reach", banner)
	}
	if got := Org(); got != "" {
		t.Errorf("Org() = %q, want empty for a key from another deployment", got)
	}
}

func TestBannerKeepsOrgWhenTheAmbientBackendMatchesTheProfile(t *testing.T) {
	// The `blocks login --write-env` flow: .env pins the profile's own backend and
	// its own key, so the profile still describes the request in full.
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	// Trailing slash included on purpose: the same deployment, spelled differently.
	Resolve(&Overrides{BackendURL: "https://umbrella.blocks.ai/", EnvCredential: "bk_test"})

	if got, want := Banner(), "[umbrella.blocks.ai / Engineering]"; got != want {
		t.Fatalf("Banner() = %q, want %q", got, want)
	}
	if !Enterprise() {
		t.Error("Enterprise() should stay true when the pin names the profile's own backend")
	}
}

func TestBannerReportsOrgUnknownForAnUnverifiableKey(t *testing.T) {
	// --api-key (or BLOCKS_API_KEY) supplying a key the store has never seen. The
	// deployment is still the profile's, but only the backend can say which org
	// the key belongs to, and borrowing the profile's would name an org the
	// command is not acting as.
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	Resolve(&Overrides{FlagCredential: "bk_minted_elsewhere"})

	banner := Banner()
	if strings.Contains(banner, "Engineering") {
		t.Errorf("Banner() = %q, must not attribute an org it cannot verify", banner)
	}
	if !strings.Contains(banner, "umbrella.blocks.ai") {
		t.Errorf("Banner() = %q, want the targeted deployment named", banner)
	}
	if !strings.Contains(banner, "unknown") {
		t.Errorf("Banner() = %q, want the org marked unknown", banner)
	}
	if got := Org(); got != "" {
		t.Errorf("Org() = %q, want empty", got)
	}
}

func TestBannerReportsOrgUnknownForAPipedKey(t *testing.T) {
	// --api-key-stdin supplying a key the store has never seen: as with --api-key,
	// only the backend can say which org it belongs to. (A piped key the profile does
	// hold is attributed — see TestOrgAttributesASuppliedKeyTheProfileAlreadyHolds.)
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	Resolve(&Overrides{
		CredentialFromStdin: true,
		ReadStdinCredential: func() (string, error) { return "bk_piped_elsewhere", nil },
	})

	banner := Banner()
	if strings.Contains(banner, "Engineering") {
		t.Errorf("Banner() = %q, must not attribute an org to a piped key", banner)
	}
	if !strings.Contains(banner, "unknown") {
		t.Errorf("Banner() = %q, want the org marked unknown", banner)
	}
}

// Resolve runs from PersistentPreRun on every command, so a round-trip there
// would tax every invocation — including ones that never talk to a backend at
// all. This pins the guarantee for the fullest override shape: an ambient backend
// URL, a build-time default, a flag key, an env key and a piped key all present.
func TestResolveMakesNoNetworkCallForAnyOverrideShape(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
	}))
	t.Cleanup(srv.Close)

	p := enterpriseProfile()
	p.BaseURL = srv.URL
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{"umbrella.blocks.ai": p})

	stdinReads := 0
	Resolve(&Overrides{
		BackendURL:          srv.URL,
		DefaultBackendURL:   srv.URL,
		FlagCredential:      "bk_flag",
		EnvCredential:       "bk_env",
		CredentialFromStdin: true,
		ReadStdinCredential: func() (string, error) { stdinReads++; return "bk_stdin", nil },
		StoredCredential:    func() (string, bool, error) { return "bk_store", false, nil },
	})

	if calls != 0 {
		t.Fatalf("Resolve made %d network call(s), want 0", calls)
	}
	if stdinReads != 0 {
		t.Fatalf("Resolve consumed stdin %d time(s); the read must stay lazy", stdinReads)
	}
	if got := BackendURL(); got != srv.URL {
		t.Errorf("BackendURL() = %q, want %q", got, srv.URL)
	}
	if got := ChosenBackendURL(); got != srv.URL {
		t.Errorf("ChosenBackendURL() = %q, want %q", got, srv.URL)
	}
}

// The lazily resolved credential must be produced once and shared, because stdin
// cannot be read twice. Every consumer reads it, so the count is what protects
// them from each other.
func TestEffectiveCredentialReadsStdinExactlyOnce(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	reads := 0
	Resolve(&Overrides{
		CredentialFromStdin: true,
		ReadStdinCredential: func() (string, error) { reads++; return "bk_piped", nil },
	})

	for i := 0; i < 3; i++ {
		if got := EffectiveCredential(); got.Key != "bk_piped" || got.Source != SourceStdin {
			t.Fatalf("EffectiveCredential() = %+v, want the piped key", got)
		}
	}
	_ = Banner()
	_ = Org()
	if reads != 1 {
		t.Fatalf("stdin was read %d time(s), want exactly 1", reads)
	}
}

// The precedence must live here and nowhere else, so pin the whole ladder.
func TestEffectiveCredentialPrecedence(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	full := func() *Overrides {
		return &Overrides{
			FlagCredential:      "bk_flag",
			EnvCredential:       "bk_env",
			CredentialFromStdin: true,
			ReadStdinCredential: func() (string, error) { return "bk_stdin", nil },
			StoredCredential:    func() (string, bool, error) { return "bk_store", false, nil },
		}
	}

	cases := []struct {
		name   string
		mutate func(*Overrides)
		key    string
		source Source
	}{
		{"flag wins", func(o *Overrides) {}, "bk_flag", SourceFlag},
		{"stdin beats env", func(o *Overrides) { o.FlagCredential = "" }, "bk_stdin", SourceStdin},
		{"env beats profile", func(o *Overrides) {
			o.FlagCredential, o.CredentialFromStdin = "", false
		}, "bk_env", SourceEnv},
		{"profile beats the legacy store", func(o *Overrides) {
			o.FlagCredential, o.CredentialFromStdin, o.EnvCredential = "", false, ""
		}, "bk_test", SourceProfile},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Reset()
			o := full()
			tc.mutate(o)
			Resolve(o)
			got := EffectiveCredential()
			if got.Key != tc.key || got.Source != tc.source {
				t.Fatalf("EffectiveCredential() = %+v, want key %q from source %v", got, tc.key, tc.source)
			}
		})
	}
}

// An organization chosen after the local resolution finished must supersede it
// everywhere at once. The choice necessarily arrives late — Resolve() may not
// prompt, and the credential is memoized before anything can ask — so a caller
// that only swapped its own copy of the key would leave the banner naming the
// organization the precedence picked while the request went out as another.
func TestSelectOrgSupersedesTheResolvedCredentialAndOrg(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	Resolve(nil)

	// Resolve (and so memoize) the credential first, exactly as a command does
	// before it is in any position to ask which organization to publish under.
	if got := EffectiveCredential(); got.Key != "bk_test" || got.Source != SourceProfile {
		t.Fatalf("EffectiveCredential() = %+v, want the profile's own key", got)
	}
	if got, want := Banner(), "[umbrella.blocks.ai / Engineering]"; got != want {
		t.Fatalf("Banner() = %q, want %q before a choice is made", got, want)
	}

	SelectOrg("Operations", "bk_ops")

	if got := EffectiveCredential(); got.Key != "bk_ops" {
		t.Errorf("EffectiveCredential().Key = %q, want the chosen organization's key", got.Key)
	}
	if got := Org(); got != "Operations" {
		t.Errorf("Org() = %q, want Operations", got)
	}
	if got, want := Banner(), "[umbrella.blocks.ai / Operations]"; got != want {
		t.Errorf("Banner() = %q, want %q", got, want)
	}
}

// Supplied() decides whether anything may still change the organization a request
// acts as, so it must cover every tier the invocation itself names — including
// BLOCKS_API_KEY, which a project .env supplies just as deliberately as a flag.
func TestSuppliedCoversEveryCredentialTheInvocationNames(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	cases := []struct {
		name string
		o    *Overrides
		want bool
	}{
		{"--api-key", &Overrides{FlagCredential: "bk_flag"}, true},
		{"--api-key-stdin", &Overrides{
			CredentialFromStdin: true,
			ReadStdinCredential: func() (string, error) { return "bk_piped", nil },
		}, true},
		{"BLOCKS_API_KEY", &Overrides{EnvCredential: "bk_env"}, true},
		{"the profile's stored key", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Reset()
			Resolve(tc.o)
			c := EffectiveCredential()
			if got := c.Source.Supplied(); got != tc.want {
				t.Fatalf("Source(%v).Supplied() = %v, want %v", c.Source, got, tc.want)
			}
		})
	}
}

// displacedProfile seeds an active profile for deployment A holding A's key, so an
// ambient backend URL naming deployment B displaces it.
func displacedProfile(t *testing.T) {
	t.Helper()
	seedProfiles(t, "deployment-a", map[string]profiles.Profile{
		"deployment-a": {
			BaseURL:      "https://a.blocks.ai",
			DefaultOrgID: "org-a",
			Orgs:         map[string]profiles.OrgKey{"org-a": {OrgName: "Org A", ApiKey: "bk_a"}},
		},
	})
}

// The credential tiers that read local storage must honour the same rule as the
// enterprise verdict: a profile an ambient backend URL displaced describes a
// deployment the request will not reach, so its key must not go to the one it
// will. Sending it produces a confusing authentication failure at best and one
// deployment's credential arriving at another at worst.
func TestEffectiveCredentialRefusesAProfileKeyForAnotherDeployment(t *testing.T) {
	displacedProfile(t)

	Resolve(&Overrides{BackendURL: "https://b.blocks.ai"})

	c := EffectiveCredential()
	if c.Key != "" {
		t.Fatalf("EffectiveCredential().Key = %q, must not send the displaced profile's key", c.Key)
	}
	if c.Err == nil {
		t.Fatal("EffectiveCredential().Err = nil; a logged-in user with no key for the target needs to be told so")
	}
	msg := c.Err.Error()
	if !strings.Contains(msg, "b.blocks.ai") {
		t.Errorf("error = %q, want the targeted deployment named", msg)
	}
	if !strings.Contains(msg, "BLOCKS_API_KEY") || !strings.Contains(msg, "--api-key") {
		t.Errorf("error = %q, want both ways to supply a credential", msg)
	}
	if strings.Contains(msg, "bk_a") {
		t.Errorf("error = %q, must not echo a credential", msg)
	}
}

// The documented headless mechanism: an ambient backend URL plus an ambient key,
// with no profile for that deployment. The guard above must leave it untouched.
func TestEffectiveCredentialUsesTheEnvKeyWhenTheProfileIsDisplaced(t *testing.T) {
	displacedProfile(t)

	Resolve(&Overrides{BackendURL: "https://b.blocks.ai", EnvCredential: "bk_b"})

	c := EffectiveCredential()
	if c.Err != nil {
		t.Fatalf("EffectiveCredential().Err = %v, want none when an ambient key is supplied", c.Err)
	}
	if c.Key != "bk_b" || c.Source != SourceEnv {
		t.Fatalf("EffectiveCredential() = %+v, want the ambient key", c)
	}
}

func TestEffectiveCredentialUsesTheFlagKeyWhenTheProfileIsDisplaced(t *testing.T) {
	displacedProfile(t)

	Resolve(&Overrides{BackendURL: "https://b.blocks.ai", FlagCredential: "bk_flag"})

	c := EffectiveCredential()
	if c.Err != nil {
		t.Fatalf("EffectiveCredential().Err = %v, want none when --api-key is supplied", c.Err)
	}
	if c.Key != "bk_flag" || c.Source != SourceFlag {
		t.Fatalf("EffectiveCredential() = %+v, want the flag key", c)
	}
}

// The common case: nothing displaces the profile, so its key is exactly what the
// request must carry.
func TestEffectiveCredentialUsesTheProfileKeyWhenTheProfileIsTheTarget(t *testing.T) {
	displacedProfile(t)

	for _, tc := range []struct {
		name string
		o    *Overrides
	}{
		{"no override at all", nil},
		{"an override naming the profile's own backend", &Overrides{BackendURL: "https://a.blocks.ai/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Reset()
			Resolve(tc.o)
			c := EffectiveCredential()
			if c.Err != nil {
				t.Fatalf("EffectiveCredential().Err = %v, want none", c.Err)
			}
			if c.Key != "bk_a" || c.Source != SourceProfile {
				t.Fatalf("EffectiveCredential() = %+v, want the profile's key", c)
			}
		})
	}
}

// The legacy store records no deployment at all, so with the profile that holds it
// displaced there is nothing to say its key belongs to the backend being called.
func TestEffectiveCredentialRefusesTheLegacyStoreWhenTheProfileIsDisplaced(t *testing.T) {
	displacedProfile(t)

	// Empty the profile's own key so the legacy store is the tier that answers.
	seedProfiles(t, "deployment-a", map[string]profiles.Profile{
		"deployment-a": {BaseURL: "https://a.blocks.ai", Orgs: map[string]profiles.OrgKey{}},
	})

	Resolve(&Overrides{
		BackendURL:       "https://b.blocks.ai",
		StoredCredential: func() (string, bool, error) { return "bk_legacy", false, nil },
	})

	c := EffectiveCredential()
	if c.Key != "" {
		t.Fatalf("EffectiveCredential().Key = %q, must not send a stored key to another deployment", c.Key)
	}
	if c.Err == nil || !strings.Contains(c.Err.Error(), "b.blocks.ai") {
		t.Fatalf("EffectiveCredential().Err = %v, want an error naming the target", c.Err)
	}
}

// A displaced profile that holds no key means the caller is simply not
// authenticated anywhere, and the displacement error would then describe a
// credential that does not exist. The plain "no credential at all" answer must
// survive, so the command can say 'run blocks login'.
func TestEffectiveCredentialIsEmptyWhenADisplacedProfileHoldsNoKey(t *testing.T) {
	seedProfiles(t, "blocks-network", map[string]profiles.Profile{
		"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
	})

	Resolve(&Overrides{
		BackendURL:       "https://b.blocks.ai",
		StoredCredential: func() (string, bool, error) { return "", false, nil },
	})

	c := EffectiveCredential()
	if c.Key != "" || c.Err != nil || c.Source != SourceNone {
		t.Fatalf("EffectiveCredential() = %+v, want nothing at all", c)
	}
}

// The guard runs inside the lazy credential resolution, so Resolve must stay
// local-only for a displaced profile too — it runs from PersistentPreRun on every
// command, including ones that never talk to a backend.
func TestResolveMakesNoNetworkCallWithADisplacedProfile(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
	}))
	t.Cleanup(srv.Close)

	displacedProfile(t)

	Resolve(&Overrides{
		BackendURL:       srv.URL,
		StoredCredential: func() (string, bool, error) { return "bk_legacy", false, nil },
	})

	if calls != 0 {
		t.Fatalf("Resolve made %d network call(s), want 0", calls)
	}
	if EffectiveCredential().Err == nil {
		t.Fatal("a displaced profile with no supplied key should resolve to an error")
	}
	if calls != 0 {
		t.Fatalf("resolving the credential made %d network call(s), want 0", calls)
	}
}

// A piped key outranks the profile, so it still resolves for a displaced profile —
// and still exactly once, however many consumers read it. The guard must not add a
// second resolution path that would find stdin already drained.
func TestEffectiveCredentialReadsStdinExactlyOnceWithADisplacedProfile(t *testing.T) {
	displacedProfile(t)

	reads := 0
	Resolve(&Overrides{
		BackendURL:          "https://b.blocks.ai",
		CredentialFromStdin: true,
		ReadStdinCredential: func() (string, error) { reads++; return "bk_piped", nil },
		StoredCredential:    func() (string, bool, error) { return "bk_legacy", false, nil },
	})

	for i := 0; i < 3; i++ {
		if got := EffectiveCredential(); got.Key != "bk_piped" || got.Source != SourceStdin || got.Err != nil {
			t.Fatalf("EffectiveCredential() = %+v, want the piped key", got)
		}
	}
	_ = Banner()
	_ = Org()
	_ = TargetName()
	if reads != 1 {
		t.Fatalf("stdin was read %d time(s), want exactly 1", reads)
	}
}

// An enterprise profile displaced by an ambient backend URL says nothing about the
// deployment being called. Trusting it there would force free billing and suppress
// the marketplace prompts against a deployment that has both.
func TestEnterpriseIgnoresAProfileTheOverrideDisplaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // a stock, non-enterprise backend
	}))
	t.Cleanup(srv.Close)

	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	Resolve(&Overrides{BackendURL: srv.URL})

	if ProfileIsTarget() {
		t.Fatal("ProfileIsTarget() should be false when an override points elsewhere")
	}
	if Enterprise() {
		t.Fatal("Enterprise() must describe the backend being called, not a displaced profile")
	}
}

// The name a success message gives the deployment follows the banner's rule: the
// locally cached brand only while the profile is the target, the deployment's host
// once something points the request elsewhere.
func TestTargetNameNamesTheBackendOnceTheProfileIsDisplaced(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)

	Resolve(&Overrides{BackendURL: "http://127.0.0.1:8899", EnvCredential: "bk_elsewhere"})

	got := TargetName()
	if strings.Contains(got, "Umbrella Corporation") {
		t.Errorf("TargetName() = %q, must not name a product the request will not reach", got)
	}
	if got != "127.0.0.1:8899" {
		t.Errorf("TargetName() = %q, want the effective backend's host", got)
	}
}

// The common case: nothing displaces the profile, so its brand does describe the
// deployment and must still be what the user reads.
func TestTargetNameKeepsTheProductNameWhenTheProfileIsTheTarget(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)

	for _, tc := range []struct {
		name string
		o    *Overrides
	}{
		{"no override at all", &Overrides{EnvCredential: "bk_test"}},
		{"an override naming the profile's own backend", &Overrides{BackendURL: "https://umbrella.blocks.ai/", EnvCredential: "bk_test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Resolve(tc.o)
			if got, want := TargetName(), "Umbrella Corporation"; got != want {
				t.Fatalf("TargetName() = %q, want %q", got, want)
			}
		})
	}
}

// A stock install has no profile and points nowhere, so the target is the public
// deployment and keeps the stock name.
func TestTargetNameIsTheStockNameOnStockNetwork(t *testing.T) {
	seedProfiles(t, "blocks-network", map[string]profiles.Profile{
		"blocks-network": {Orgs: map[string]profiles.OrgKey{}},
	})
	t.Cleanup(branding.Reset)

	Resolve(&Overrides{EnvCredential: "bk_test"})

	if got, want := TargetName(), "Blocks Network"; got != want {
		t.Fatalf("TargetName() = %q, want %q", got, want)
	}
}

// hostileOrgName is an organization name of the shape a deployment can hand back:
// printable text followed by an erase-line and a cursor-up sequence, then a
// replacement. Printed raw before a destructive command it wipes out the banner and
// the confirmation the operator is meant to check, and redraws them naming a
// different deployment.
const hostileOrgName = "Engineering\x1b[2K\r\x1b[1AAcme Corporation"

// controlChars reports every C0/C1 control character and DEL left in s. It is the
// assertion these cases need: whether the *terminal* would act on the string, not
// whether it looks a particular way.
func controlChars(s string) []rune {
	var found []rune
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			found = append(found, r)
		}
	}
	return found
}

// The banner exists so an operator can verify the deployment and organization a
// destructive command is about to act on. An organization name is server-supplied,
// so a name that can rewrite the line it appears on defeats the check rather than
// merely looking odd.
func TestBannerCannotBeForgedByAServerSuppliedOrgName(t *testing.T) {
	p := enterpriseProfile()
	p.Orgs = map[string]profiles.OrgKey{"org-1": {OrgName: hostileOrgName, ApiKey: "bk_test"}}
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{"umbrella.blocks.ai": p})

	Resolve(&Overrides{})

	got := Banner()
	if bad := controlChars(got); bad != nil {
		t.Fatalf("Banner() = %q carries control characters %U — it can erase or rewrite itself", got, bad)
	}
	// The banner must still be a banner: dropping the org silently would leave the
	// operator with nothing to check.
	if !strings.Contains(got, "Engineering") || !strings.HasPrefix(got, "[umbrella.blocks.ai / ") {
		t.Errorf("Banner() = %q, want the deployment and the printable part of the org name", got)
	}
}

// The same vector reaches the sentences that name the deployment — including
// unregister's "This cannot be undone" prompt — because the product name is
// whatever the deployment called itself during discovery.
func TestTargetNameCannotBeForgedByAServerSuppliedProductName(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	branding.Set("Umbrella\x1b[2K\rAcme")
	t.Cleanup(branding.Reset)

	Resolve(&Overrides{EnvCredential: "bk_test"})

	got := TargetName()
	if bad := controlChars(got); bad != nil {
		t.Fatalf("TargetName() = %q carries control characters %U", got, bad)
	}
	if !strings.HasPrefix(got, "Umbrella") {
		t.Errorf("TargetName() = %q, want the printable part of the product name kept", got)
	}
}

// The host label is derived from an origin a project `.env` can supply, so it is the
// third way into the same two lines.
func TestBannerHostLabelCannotCarryControlCharacters(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})

	Resolve(&Overrides{BackendURL: "https://acme.example.test\x1b[2K\r", EnvCredential: "bk_env"})

	for _, got := range []string{Banner(), TargetName()} {
		if bad := controlChars(got); bad != nil {
			t.Errorf("%q carries control characters %U", got, bad)
		}
	}
}

// A command whose organization does not scope its operation — the deployment
// authorizes it by rights over a named agent or invitation — must not get an
// organization in its banner. Naming one there is a true statement about the
// credential that reads as a false one about what the command can reach, so the
// banner names the subject authorization turns on, or the deployment alone when the
// command states its own subject a line later.
func TestBannerNamesNoOrganizationWhereItDoesNotScopeTheOperation(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	Resolve(nil)

	// The organization is verifiable here — the org-scoped shape names it — which is
	// what makes this the interesting case: the banner withholds an answer it has.
	if got, want := Banner(), "[umbrella.blocks.ai / Engineering]"; got != want {
		t.Fatalf("Banner() = %q, want %q — the org-scoped shape must be unchanged", got, want)
	}

	deploymentOnly := Banner(OrgIsNotTheScope())
	if got, want := deploymentOnly, "[umbrella.blocks.ai]"; got != want {
		t.Errorf("Banner(OrgIsNotTheScope()) = %q, want %q", got, want)
	}

	withSubject := Banner(ActsOn("agent my_agent → user@example.test"))
	if got, want := withSubject, "[umbrella.blocks.ai / agent my_agent → user@example.test]"; got != want {
		t.Errorf("Banner(ActsOn(...)) = %q, want %q", got, want)
	}

	for _, got := range []string{deploymentOnly, withSubject} {
		if strings.Contains(got, "Engineering") {
			t.Errorf("%q names an organization that does not scope the operation", got)
		}
		if strings.Contains(got, "organization") {
			t.Errorf("%q raises the organization at all, which is the inference to avoid", got)
		}
		if !strings.Contains(got, "umbrella.blocks.ai") {
			t.Errorf("%q must still name the deployment the request will reach", got)
		}
	}
}

// The subject reaches the banner from an argument or an agent card, so it is the
// same forgery vector as a server-supplied organization name and must be escaped by
// the banner rather than by each caller.
func TestBannerSubjectCannotForgeTheLine(t *testing.T) {
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
		"umbrella.blocks.ai": enterpriseProfile(),
	})
	Resolve(nil)

	got := Banner(ActsOn("agent victim_agent\x1b[2K\r\x1b[1Aagent harmless_agent"))
	if bad := controlChars(got); bad != nil {
		t.Fatalf("Banner() = %q carries control characters %U — it can erase or rewrite itself", got, bad)
	}
	if !strings.HasPrefix(got, "[umbrella.blocks.ai / agent victim_agent") {
		t.Errorf("Banner() = %q, want the deployment and the printable part of the subject", got)
	}
}

// A credential the invocation supplied is not automatically unattributable. When it
// is byte-for-byte a key the target profile already holds — the ordinary
// `login --write-env` shape, where the `.env` carries the key login stored — the
// store itself names the organization, and that is evidence rather than a guess. The
// nuance is pinned here because the surrounding documentation once claimed every
// flag, piped or ambient key yielded "organization unknown".
func TestOrgAttributesASuppliedKeyTheProfileAlreadyHolds(t *testing.T) {
	seed := func(t *testing.T) {
		seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{
			"umbrella.blocks.ai": enterpriseProfile(),
		})
	}

	t.Run("flag", func(t *testing.T) {
		seed(t)
		Resolve(&Overrides{FlagCredential: "bk_test"})

		if got := EffectiveCredential().Source; got != SourceFlag {
			t.Fatalf("Source = %v, want SourceFlag — the flag must still win the precedence", got)
		}
		if got := Org(); got != "Engineering" {
			t.Errorf("Org() = %q, want Engineering for a key the profile has cached", got)
		}
		if got, want := Banner(), "[umbrella.blocks.ai / Engineering]"; got != want {
			t.Errorf("Banner() = %q, want %q", got, want)
		}
	})

	t.Run("piped", func(t *testing.T) {
		seed(t)
		Resolve(&Overrides{
			CredentialFromStdin: true,
			ReadStdinCredential: func() (string, error) { return "bk_test", nil },
		})

		if got := EffectiveCredential().Source; got != SourceStdin {
			t.Fatalf("Source = %v, want SourceStdin", got)
		}
		if !OrgKnown() {
			t.Error("OrgKnown() = false, want true for a piped key the profile has cached")
		}
	})

	// One byte off is a different key, and only the backend can say whose it is.
	t.Run("near miss stays unknown", func(t *testing.T) {
		seed(t)
		Resolve(&Overrides{FlagCredential: "bk_test2"})

		if got := Org(); got != "" {
			t.Errorf("Org() = %q, want empty — a key the store has never seen must not borrow an org", got)
		}
		if got, want := Banner(), "[umbrella.blocks.ai / organization unknown]"; got != want {
			t.Errorf("Banner() = %q, want %q", got, want)
		}
	})
}

// A legitimate non-ASCII organization name must survive intact: the guard targets
// control characters, and an org whose name is not Latin script is ordinary.
func TestBannerKeepsAUnicodeOrgNameIntact(t *testing.T) {
	const unicodeOrg = "Ωμέγα 株式会社 — Ünïcödé"
	p := enterpriseProfile()
	p.Orgs = map[string]profiles.OrgKey{"org-1": {OrgName: unicodeOrg, ApiKey: "bk_test"}}
	seedProfiles(t, "umbrella.blocks.ai", map[string]profiles.Profile{"umbrella.blocks.ai": p})

	Resolve(&Overrides{})

	if got, want := Banner(), "[umbrella.blocks.ai / "+unicodeOrg+"]"; got != want {
		t.Errorf("Banner() = %q, want %q", got, want)
	}
}

// TestMain clears BLOCKS_CDM_URL for every case in this package. The variable is a
// tier of the target precedence now — it names the deployment a profile that records
// none resolves to — so an endpoint exported in the shell running the tests would
// retarget every case that relies on a stock profile. The cases that exercise the tier
// set it themselves.
func TestMain(m *testing.M) {
	os.Unsetenv(cdm.URLEnv)
	os.Exit(m.Run())
}

// stockNetworkProfile is a profile that records no deployment of its own — the shape
// `blocks login --network` writes — holding a key minted at Blocks Network.
func stockNetworkProfile() profiles.Profile {
	return profiles.Profile{
		DefaultOrgID: "org-net",
		Orgs:         map[string]profiles.OrgKey{"org-net": {OrgName: "Network Org", ApiKey: "bk_network"}},
	}
}

// cdmAndDiscoveryServer stands in for an enterprise deployment that also serves its
// own CDM endpoint at /api/v1/cdm, reporting itself as the API origin. It counts the
// CDM fetches so a case can pin exactly when — and how often — the remote tier of the
// target precedence is resolved.
func cdmAndDiscoveryServer(t *testing.T, fetches *atomic.Int64) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/cdm":
			fetches.Add(1)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(cdm.Config{Api: cdm.ApiConfig{BaseURL: srv.URL, ClientID: "enterprise-client"}})
		case "/api/v1/cli-config":
			w.Write([]byte(`{"enterprise":true,"productName":"Umbrella Corporation"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The last tier of the target precedence is a deployment only remote config can name,
// and Resolve() runs from every command's PersistentPreRun — including the ones that
// never reach a backend — so it may not fetch it. What the resolver can settle without
// the fetch, it settles: whether the active profile still describes the target is the
// presence of the redirect, not its payload, so a stored key is withheld before
// anything goes near the network. Naming the deployment is what forces the fetch, once,
// and the credential, the enterprise verdict and the request origin then all describe
// that one answer.
func TestResolveNeverFetchesTheCDMNamedTargetAndOneFetchAnswersEveryConsumer(t *testing.T) {
	var fetches atomic.Int64
	enterprise := cdmAndDiscoveryServer(t, &fetches)
	t.Setenv(cdm.URLEnv, enterprise.URL+"/api/v1/cdm")
	cdm.Reset()
	t.Cleanup(cdm.Reset)

	seedProfiles(t, profiles.DefaultProfile, map[string]profiles.Profile{
		profiles.DefaultProfile: stockNetworkProfile(),
	})

	Resolve(nil)
	if got := fetches.Load(); got != 0 {
		t.Fatalf("Resolve made %d CDM fetch(es); it runs for every command and must stay local", got)
	}
	if ProfileIsTarget() {
		t.Error("a profile that records no deployment does not describe the one a redirected CDM endpoint names")
	}
	if got := fetches.Load(); got != 0 {
		t.Fatalf("deciding the profile is displaced must stay local, got %d fetch(es)", got)
	}

	c := EffectiveCredential()
	if c.Key != "" {
		t.Errorf("EffectiveCredential().Key = %q, want the profile's key withheld from a deployment it was not minted at", c.Key)
	}
	if c.Err == nil {
		t.Fatal("withholding the key must be reported, not silently returned as no credential at all")
	}
	if host := mustHostOf(t, enterprise.URL); !strings.Contains(c.Err.Error(), host) {
		t.Errorf("error %q must name the deployment a key is needed for (%s)", c.Err, host)
	}

	url, err := EffectiveBackendURL()
	if err != nil {
		t.Fatalf("EffectiveBackendURL: %v", err)
	}
	if url != enterprise.URL {
		t.Errorf("EffectiveBackendURL() = %q, want the origin the CDM payload names (%q)", url, enterprise.URL)
	}
	if !Enterprise() {
		t.Error("Enterprise() must describe the deployment the CDM endpoint named, not the stock profile")
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("the target must be fetched exactly once for every consumer, got %d", got)
	}
}

// The complement, and the case that must stay free: with no endpoint named, the CDM
// payload is how a stock profile finds its own backend, so the profile is still the
// target and its key is still the one to send. Nothing here may consult the network —
// not the resolver, and not the credential the request will carry.
func TestAnUnredirectedCDMLeavesTheStockProfileAsTheTarget(t *testing.T) {
	var fetches atomic.Int64
	enterprise := cdmAndDiscoveryServer(t, &fetches)
	// The endpoint exists and is reachable, so any fetch would be counted; nothing
	// points this invocation at it.
	_ = enterprise
	cdm.Reset()
	t.Cleanup(cdm.Reset)

	seedProfiles(t, profiles.DefaultProfile, map[string]profiles.Profile{
		profiles.DefaultProfile: stockNetworkProfile(),
	})

	Resolve(nil)

	if !ProfileIsTarget() {
		t.Error("with nothing pointing elsewhere, the active profile is the target")
	}
	if got := EffectiveCredential(); got.Key != "bk_network" || got.Source != SourceProfile {
		t.Errorf("EffectiveCredential() = %+v, want the profile's own key", got)
	}
	if Enterprise() {
		t.Error("a target the user did not pick cannot be an enterprise deployment")
	}
	if got, want := Banner(), "[blocks-network / Network Org]"; got != want {
		t.Errorf("Banner() = %q, want %q", got, want)
	}
	if got := fetches.Load(); got != 0 {
		t.Errorf("the ordinary case fetched %d time(s); nothing about it needs the network", got)
	}
}

// mustHostOf is the host of rawURL, for asserting that a message names the deployment
// it is about.
func mustHostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		t.Fatalf("cannot parse a host out of %q: %v", rawURL, err)
	}
	return u.Host
}

// The strict origin rules used to apply only where a user types a deployment URL, on the
// `blocks login` argument. Every other tier reaches the same requests carrying the same
// credential without being typed: an exported BLOCKS_BACKEND_URL, a profile written
// before the check existed, the linker default, and the api.baseUrl a remote config
// names. Those are now held to the same rules, and the failure surfaces from
// EffectiveBackendURL — the accessor every request path goes through — rather than from
// Resolve, which has no error to return.
func TestAnInvalidEffectiveTargetIsRefusedWhateverTierSuppliedIt(t *testing.T) {
	cases := []struct {
		name string
		o    Overrides
		want string
	}{
		{
			// Reaches collector.example while reading as trusted.example.
			name: "userinfo in an exported backend URL",
			o:    Overrides{BackendURL: "https://trusted.example@collector.example"},
			want: "username or password",
		},
		{
			name: "non-loopback http in an exported backend URL",
			o:    Overrides{BackendURL: "http://backend.acme.example"},
			want: "https:",
		},
		{
			// An origin has endpoint paths appended, so this would request a path no
			// deployment serves.
			name: "query string in an exported backend URL",
			o:    Overrides{BackendURL: "https://backend.acme.example?x=1"},
			want: "query string",
		},
		{
			name: "port outside the TCP range in the build default",
			o:    Overrides{DefaultBackendURL: "https://backend.acme.example:99999"},
			want: "port outside",
		},
		{
			// url.Parse accepts this: Host is "[x]" and Hostname() is "x".
			name: "bracketed host that is not an IP literal",
			o:    Overrides{BackendURL: "https://[x]"},
			// origin.Validate states this reason regardless of which layer catches the input;
			// it used to depend on the Go release, which is why this passed locally and failed
			// in CI.
			want: "invalid host",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Reset()
			t.Cleanup(Reset)
			Resolve(&tc.o)

			got, err := EffectiveBackendURL()
			if err == nil {
				t.Fatalf("EffectiveBackendURL() = %q, want a refusal", got)
			}
			if got != "" {
				t.Errorf("a refused target must not also be returned, got %q", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must say why: got %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The complement: a legitimate origin still resolves, including the forms the rules
// deliberately allow — a loopback host over http, a custom port, and a path prefix.
func TestAValidEffectiveTargetStillResolves(t *testing.T) {
	for _, raw := range []string{
		"https://backend.acme.example",
		"https://backend.acme.example:8443",
		"https://backend.acme.example/tenant-a",
		"http://localhost:3001",
		"http://127.0.0.1:3001",
	} {
		Reset()
		Resolve(&Overrides{BackendURL: raw})
		got, err := EffectiveBackendURL()
		if err != nil {
			t.Errorf("EffectiveBackendURL() for %q: unexpected refusal %v", raw, err)
		}
		if got != raw {
			t.Errorf("EffectiveBackendURL() = %q, want %q", got, raw)
		}
	}
	Reset()
}

// A stock Blocks Network profile must not be treated as describing this build's
// linker-default deployment. profileMatchesLocalTarget used to be set from "was there an
// override" rather than from the resolved target, so absent an override it was true
// unconditionally — and in a packaged Enterprise build, where the target falls through to
// the baked-in default, the Network profile was then the target. profileDisplaced() was
// therefore false and EffectiveCredential handed the Network key over as the Bearer token
// for Enterprise, silently, while the banner named Enterprise.
func TestANetworkProfileDoesNotClaimTheLinkerDefaultDeployment(t *testing.T) {
	const enterprise = "https://blocks.acme.example"

	t.Run("network profile with a cached key does not lend it to the linker default", func(t *testing.T) {
		Reset()
		t.Cleanup(Reset)
		// Empty BaseURL is how a completed stock Network login is stored.
		seedProfiles(t, profiles.DefaultProfile, map[string]profiles.Profile{
			profiles.DefaultProfile: {
				DefaultOrgID: "org_net",
				Orgs:         map[string]profiles.OrgKey{"org_net": {OrgName: "Net", ApiKey: "bk_network_key"}},
			},
		})
		Resolve(&Overrides{DefaultBackendURL: enterprise})

		if ProfileIsTarget() {
			t.Error("a Network profile does not describe the linker-default deployment")
		}
		c := EffectiveCredential()
		if c.Key == "bk_network_key" {
			t.Error("the Network key must not be sent to the linker-default deployment")
		}
		if c.Err == nil {
			t.Error("the resolver must say a credential is needed for that deployment rather than staying silent")
		}
	})

	t.Run("network profile still describes stock Network", func(t *testing.T) {
		Reset()
		t.Cleanup(Reset)
		seedProfiles(t, profiles.DefaultProfile, map[string]profiles.Profile{
			profiles.DefaultProfile: {
				DefaultOrgID: "org_net",
				Orgs:         map[string]profiles.OrgKey{"org_net": {OrgName: "Net", ApiKey: "bk_network_key"}},
			},
		})
		// No override and no linker default: nothing local named a deployment, which is
		// Blocks Network — the one arrangement where the empty profile is the target.
		Resolve(&Overrides{})

		if !ProfileIsTarget() {
			t.Error("with no deployment named locally the Network profile is the target")
		}
		if got := EffectiveCredential(); got.Key != "bk_network_key" {
			t.Errorf("its own key must still resolve, got %q (err %v)", got.Key, got.Err)
		}
	})

	t.Run("an enterprise profile describing the linker default still matches", func(t *testing.T) {
		Reset()
		t.Cleanup(Reset)
		seedProfiles(t, "acme", map[string]profiles.Profile{
			"acme": {
				BaseURL:      enterprise,
				Enterprise:   true,
				DefaultOrgID: "org_ent",
				Orgs:         map[string]profiles.OrgKey{"org_ent": {OrgName: "Acme", ApiKey: "bk_enterprise_key"}},
			},
		})
		Resolve(&Overrides{DefaultBackendURL: enterprise})

		if !ProfileIsTarget() {
			t.Error("a profile recording that deployment does describe it")
		}
		if got := EffectiveCredential(); got.Key != "bk_enterprise_key" {
			t.Errorf("its own key must resolve, got %q (err %v)", got.Key, got.Err)
		}
	})
}
