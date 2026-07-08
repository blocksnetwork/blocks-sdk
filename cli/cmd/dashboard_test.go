package cmd

import (
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// TestResolveDashboardURLPrefersAppBaseOverride confirms an explicit dashboard
// override (BLOCKS_APP_BASE_URL) wins over the deployment fallback.
func TestResolveDashboardURLPrefersAppBaseOverride(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "https://dashboard.acme.com")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://api.acme.com")

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://dashboard.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want dashboard override", got)
	}
}

// TestResolveDashboardURLFallsBackToBackendEnv covers BLOCKS-563: without a
// dashboard override, the dashboard opens on the deployment origin
// (BLOCKS_BACKEND_URL) rather than stock CDM / app.blocks.ai.
func TestResolveDashboardURLFallsBackToBackendEnv(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://blocks.acme.com")

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://blocks.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want backend-env fallback", got)
	}
}

// TestResolveDashboardURLFallsBackToActiveProfile covers BLOCKS-563: the active
// profile's BaseURL drives the dashboard origin when no override and no
// BLOCKS_BACKEND_URL are set.
func TestResolveDashboardURLFallsBackToActiveProfile(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://blocks.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want active-profile fallback", got)
	}
}
