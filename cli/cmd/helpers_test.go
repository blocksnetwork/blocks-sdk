package cmd

import (
	"strings"
	"testing"

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
