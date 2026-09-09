package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
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

func TestLogoutNamesPreservedDeployment(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	isolateLogoutSideEffects(t)
	branding.Set("Umbrella Corporation")
	t.Cleanup(func() { branding.Reset() })
	out := captureStdout(func() {
		if err := runBlocksLogout(); err != nil {
			t.Fatalf("runBlocksLogout: %v", err)
		}
	})
	if !strings.Contains(out, "Logged out of Umbrella Corporation") {
		t.Errorf("should name the product:\n%s", out)
	}
	if !strings.Contains(out, "umbrella.blocks.ai") {
		t.Errorf("should name the preserved profile:\n%s", out)
	}
	if !strings.Contains(out, "blocks profile remove") {
		t.Errorf("should say how to forget it:\n%s", out)
	}
}

func TestLogoutDefaultProfileOmitsRemovalHint(t *testing.T) {
	withTempProfiles(t)
	isolateLogoutSideEffects(t)
	orgKeys := map[string]profiles.OrgKey{"org-1": {OrgName: "Org 1", ApiKey: "bk_default"}}
	if err := profiles.Upsert(profiles.DefaultProfile, profiles.Profile{DefaultOrgID: "org-1", Orgs: orgKeys}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out := captureStdout(func() {
		if err := runBlocksLogout(); err != nil {
			t.Fatalf("runBlocksLogout: %v", err)
		}
	})
	if !strings.Contains(out, "Logged out of") {
		t.Errorf("should print logout message:\n%s", out)
	}
	if strings.Contains(out, "blocks profile remove") {
		t.Errorf("should NOT suggest removing default profile:\n%s", out)
	}
	if strings.Contains(out, "Deployment kept") {
		t.Errorf("should NOT print deployment-kept message for default profile:\n%s", out)
	}
}

// Logout clears the credentials stored in a profile and sends no request, so an
// ambient backend URL pointing requests elsewhere must not change what it names:
// the profile — and its brand — is what the sentence is about.
func TestLogoutNamesTheProfileEvenWhenRequestsWouldGoElsewhere(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	isolateLogoutSideEffects(t)
	t.Setenv("BLOCKS_BACKEND_URL", "http://127.0.0.1:8899")
	t.Setenv("BLOCKS_API_KEY", "bk_elsewhere")
	resolveCLIContext(t, logoutCmd)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)

	out := captureStdout(func() {
		if err := runBlocksLogout(); err != nil {
			t.Fatalf("runBlocksLogout: %v", err)
		}
	})

	if !strings.Contains(out, "Logged out of Umbrella Corporation") {
		t.Errorf("logout should name the profile it cleared:\n%s", out)
	}
	if strings.Contains(out, "127.0.0.1:8899") {
		t.Errorf("logout must not name a deployment it did not touch:\n%s", out)
	}
}

// A profile name is attacker-influenceable: it is derived from the host of whatever
// deployment a login reached, and that host can come from a project .env or from the
// deployment's own discovery response. The logout summary knows the name and has to
// report it, but a line the CLI formats as something to run may not be built from it —
// a line presented as a command is a line that gets pasted into a shell, and
// termsafe.Text does not help there: `;`, `&&`, backticks and $(...) pass through it as
// the ordinary printable characters they are.
//
// The scan reuses commandLines, this package's one definition of "reads as something to
// run", so a summary that starts wording its remedy differently in future is covered by
// this case rather than by a report.
func TestTheLogoutSummaryOffersNoPastableCommandBuiltFromTheProfile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	seedProfile(t, injectedProfileName, profiles.Profile{
		BaseURL:    injectedProfileBaseURL,
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	resolveCLIContext(t, logoutCmd)

	out := captureStdout(printLogoutSummary)

	assertNoInjectedValueInCommandLines(t, out, injectedProfileName, injectedProfileBaseURL)
	// The name is still reported — a kept deployment the user cannot identify is not
	// something they can act on — and the removal is still offered.
	if !strings.Contains(out, injectedProfileName) {
		t.Errorf("the summary must still name the profile it kept:\n%s", out)
	}
	if !strings.Contains(out, "blocks profile remove") {
		t.Errorf("the remedy must still name the command that forgets the deployment:\n%s", out)
	}
}

// A surviving legacy credential is a failed logout. The legacy file is the last tier the
// resolver consults, so a `blocks` entry left in it keeps the very next command
// authenticated — which is exactly why a surviving profile key or `.env` key fails the
// command. This used to warn on a delete failure and skip silently when the path could
// not be resolved, so `logout` printed "Logged out." over a credential still on disk.
func TestLogoutFailsWhenTheLegacyCredentialCannotBeRemoved(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	// A credential path that cannot be resolved at all: previously skipped in silence.
	orig := auth.CredentialPathFunc
	t.Cleanup(func() { auth.CredentialPathFunc = orig })
	boom := errors.New("no home directory")
	auth.CredentialPathFunc = func() (string, error) { return "", boom }

	err := runBlocksLogout()
	if err == nil {
		t.Fatal("an unresolvable legacy credential store must fail the logout")
	}
	if !errors.Is(err, boom) {
		t.Errorf("the cause must be wrapped so the user can act on it, got %v", err)
	}
	if !strings.Contains(err.Error(), "logout incomplete") {
		t.Errorf("the failure must say the logout did not complete, got %v", err)
	}
}

// The complement: no legacy file at all is the ordinary case and must stay a success,
// or every install without one would fail to log out.
func TestLogoutSucceedsWhenThereIsNoLegacyCredentialFile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	orig := auth.CredentialPathFunc
	t.Cleanup(func() { auth.CredentialPathFunc = orig })
	auth.CredentialPathFunc = func() (string, error) {
		return filepath.Join(t.TempDir(), "credentials.json"), nil
	}

	if err := runBlocksLogout(); err != nil {
		t.Fatalf("a missing legacy store is not a failure: %v", err)
	}
}
