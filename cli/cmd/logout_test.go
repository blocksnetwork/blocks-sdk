package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// isolateLogoutSideEffects redirects logout's non-profile side effects (the
// legacy credentials.json delete and the ./.env scrub) to throwaway locations so
// a test only exercises contexts.json behavior and never touches real files.
func isolateLogoutSideEffects(t *testing.T) {
	t.Helper()
	credFile := filepath.Join(t.TempDir(), "credentials.json")
	origPathFunc := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credFile, nil }
	t.Cleanup(func() { auth.CredentialPathFunc = origPathFunc })
	oldDir, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldDir) })
}

// TestLogoutClearsSelectedProfileNotActive verifies that logout clears the
// profile chosen via --profile / BLOCKS_PROFILE (resolved by
// profiles.SelectedName), not the saved active profile. Regression test for the
// bug where `blocks --profile acme logout` cleared the active profile instead.
func TestLogoutClearsSelectedProfileNotActive(t *testing.T) {
	withTempProfiles(t)
	isolateLogoutSideEffects(t)

	orgKeys := func(v string) map[string]profiles.OrgKey {
		return map[string]profiles.OrgKey{"org-1": {OrgName: "Org 1", ApiKey: v}}
	}
	if err := profiles.Upsert("acme", profiles.Profile{BaseURL: "https://blocks.acme.com", DefaultOrgID: "org-1", Orgs: orgKeys("bk_acme")}, false); err != nil {
		t.Fatalf("seed acme: %v", err)
	}
	// default is the active profile; it must be left untouched.
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{DefaultOrgID: "org-1", Orgs: orgKeys("bk_default")}, true); err != nil {
		t.Fatalf("seed default: %v", err)
	}

	// Select acme via the --profile override; default stays active.
	profiles.SetActiveOverride("acme")
	defer profiles.SetActiveOverride("")

	if err := runLogout(logoutCmd, nil); err != nil {
		t.Fatalf("logout: %v", err)
	}

	c, err := profiles.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// acme (the selected profile) must have its keys cleared...
	acme := c.Profiles["acme"]
	if len(acme.Orgs) != 0 || acme.DefaultOrgID != "" {
		t.Errorf("acme keys should be cleared, got Orgs=%v DefaultOrgID=%q", acme.Orgs, acme.DefaultOrgID)
	}
	// ...but its deployment target preserved.
	if acme.BaseURL != "https://blocks.acme.com" {
		t.Errorf("acme BaseURL should be preserved, got %q", acme.BaseURL)
	}

	// The active (default) profile must be untouched.
	def := c.Profiles[profiles.DefaultProfile]
	if len(def.Orgs) == 0 || def.DefaultOrgID != "org-1" {
		t.Errorf("active profile keys should be preserved, got Orgs=%v DefaultOrgID=%q", def.Orgs, def.DefaultOrgID)
	}
}

// TestLogoutReportsFailureWhenProfileSaveFails is a regression test for a logout
// that claimed success while the credential survived: when the cleared profile
// cannot be persisted (contexts.json readable but not writable), logout must
// return an error instead of printing "Logged out.", and the org key — the
// primary authenticating credential — must be reported as still on disk.
func TestLogoutReportsFailureWhenProfileSaveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("read-only file perms do not block writes when running as root")
	}
	dir := withTempProfiles(t)
	isolateLogoutSideEffects(t)
	t.Setenv("BLOCKS_PROFILE", "") // resolve to the saved active profile, deterministically

	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Org 1", ApiKey: "bk_secret"}},
	}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Make contexts.json readable but not writable: Load() succeeds, Save() fails.
	path := filepath.Join(dir, "contexts.json")
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	if err := runLogout(logoutCmd, nil); err == nil {
		t.Fatal("expected logout to return an error when the profile store is not writable")
	}

	// Restore write perms and confirm the credential genuinely persisted — the
	// false-success path would have reported "Logged out." while leaving this key.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("restore chmod: %v", err)
	}
	c, err := profiles.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	def := c.Profiles[profiles.DefaultProfile]
	if k, ok := def.DefaultOrgKey(); !ok || k.ApiKey != "bk_secret" {
		t.Errorf("org key should remain on disk after a failed logout, got ok=%v key=%q", ok, k.ApiKey)
	}
}

// TestLogoutReportsFailureWhenProfileStoreUnreadable is a regression test: when
// contexts.json cannot be parsed, logout cannot know whether a credential is
// still cached, so it must surface the failure rather than claim success.
func TestLogoutReportsFailureWhenProfileStoreUnreadable(t *testing.T) {
	dir := withTempProfiles(t)
	isolateLogoutSideEffects(t)
	t.Setenv("BLOCKS_PROFILE", "")

	path := filepath.Join(dir, "contexts.json")
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt store: %v", err)
	}

	if err := runLogout(logoutCmd, nil); err == nil {
		t.Fatal("expected logout to return an error when the profile store is unreadable")
	}
}
