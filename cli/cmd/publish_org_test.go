package cmd

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func TestResolveOrgKeyUsesCacheThenMints(t *testing.T) {
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	defer func() { profiles.ContextsPathFunc = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"apiKey":"bk_minted","keyId":"k2","expiresAt":""}`))
	}))
	defer srv.Close()

	// Profile has a key for o1 but not o2.
	p := profiles.Profile{BaseURL: srv.URL, Enterprise: true, DefaultOrgID: "o1",
		Orgs: map[string]profiles.OrgKey{"o1": {OrgName: "Finance", ApiKey: "bk_o1"}}}
	if err := profiles.Upsert("acme", p, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Cached org → returns cached key, no mint.
	key, err := resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o1", "Finance")
	if err != nil || key != "bk_o1" {
		t.Fatalf("cached: key=%q err=%v", key, err)
	}
	// Uncached org → mints + caches.
	key, err = resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o2", "IT")
	if err != nil || key != "bk_minted" {
		t.Fatalf("mint: key=%q err=%v", key, err)
	}
	c, _ := profiles.Load()
	if c.Profiles["acme"].Orgs["o2"].ApiKey != "bk_minted" {
		t.Fatalf("minted key not cached: %+v", c.Profiles["acme"].Orgs)
	}
	// Publishing under o2 must make it the default so later run/whoami don't
	// resolve the stale o1 key via DefaultOrgKey().
	if got := c.Profiles["acme"].DefaultOrgID; got != "o2" {
		t.Fatalf("DefaultOrgID = %q, want o2 (publish target becomes default)", got)
	}
}

// TestResolveOrgKeySwitchesDefaultOnCachedHit verifies that picking an org whose
// key is already cached still switches the profile's DefaultOrgID — so the
// cached-key fast path can't leave run/whoami pointing at the previous default.
func TestResolveOrgKeySwitchesDefaultOnCachedHit(t *testing.T) {
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	defer func() { profiles.ContextsPathFunc = orig }()

	// Default is o1; both orgs already have cached keys (no mint needed).
	p := profiles.Profile{Enterprise: true, DefaultOrgID: "o1",
		Orgs: map[string]profiles.OrgKey{
			"o1": {OrgName: "Finance", ApiKey: "bk_o1"},
			"o2": {OrgName: "IT", ApiKey: "bk_o2"},
		}}
	if err := profiles.Upsert("acme", p, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Pick the cached o2. backendURL is unused — no mint happens on the cache hit.
	key, err := resolveOrgPublishKey("http://unused", "bk_o1", "acme", "o2", "IT")
	if err != nil || key != "bk_o2" {
		t.Fatalf("cached o2: key=%q err=%v", key, err)
	}

	c, _ := profiles.Load()
	pr := c.Profiles["acme"]
	if pr.DefaultOrgID != "o2" {
		t.Fatalf("DefaultOrgID = %q, want o2 after publishing under cached o2", pr.DefaultOrgID)
	}
	// whoami/run resolve via DefaultOrgKey() — it must now surface o2's key.
	if k, ok := pr.DefaultOrgKey(); !ok || k.ApiKey != "bk_o2" {
		t.Fatalf("DefaultOrgKey = %+v ok=%v, want o2 key bk_o2", k, ok)
	}
}
