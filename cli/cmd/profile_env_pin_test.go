package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// The trap: removing the profile leaves the pin behind, and every later command in
// this directory keeps going to a deployment with no credentials for it. The pin
// is written with a trailing slash to prove the comparison is by deployment, not
// by string.
func TestProfileRemoveDropsThePinForTheRemovedDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	dir := pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://blocks.acme.com/\nBLOCKS_API_KEY=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksBackendURLEnv); got != "" {
		t.Errorf("%s = %q in .env; the pin for the removed deployment must be gone", blocksBackendURLEnv, got)
	}
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "" {
		t.Errorf("%s = %q; the key was minted at the deployment just removed and must go with its pin", blocksAPIKeyEnv, got)
	}
	if !strings.Contains(out, "Removed "+blocksBackendURLEnv+" and "+blocksAPIKeyEnv+" from ./.env") {
		t.Errorf("the removal must name what it removed:\n%s", out)
	}
}

// Login writes the deployment's URL, its CDM endpoint and the key minted there as one
// deployment-bound unit, so forgetting the deployment has to drop all three. A
// surviving CDM pin keeps a directly executed agent or trigger resolving its keysets —
// and its own REST origin — from the deployment the profile list no longer mentions,
// and a surviving key is what the next command in this directory sends to Blocks
// Network, or to whichever profile is now active. Variables that are none of this
// command's business stay.
func TestProfileRemoveDropsEverythingBoundToTheRemovedDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"=https://blocks.acme.com/\n"+
		cdm.URLEnv+"="+cdm.EndpointFor("https://blocks.acme.com")+"\n"+
		blocksAPIKeyEnv+"=bk_acme\n"+
		"UNRELATED=keep\n")
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	for _, key := range []string{blocksBackendURLEnv, cdm.URLEnv, blocksAPIKeyEnv} {
		if got := envValue(t, dir, key); got != "" {
			t.Errorf("%s = %q in .env; everything bound to the removed deployment must be gone", key, got)
		}
	}
	if got := envValue(t, dir, "UNRELATED"); got != "keep" {
		t.Errorf("UNRELATED = %q, want %q; removing a profile must not touch unrelated .env keys", got, "keep")
	}
	for _, want := range []string{blocksBackendURLEnv, cdm.URLEnv, blocksAPIKeyEnv} {
		if !strings.Contains(out, want) {
			t.Errorf("the message must name %s as removed:\n%s", want, out)
		}
	}
}

// The CDM pin is scoped by deployment on its own account: a user who pointed it
// somewhere else meant it, and this command has no business overruling that just
// because the URL beside it matched. The key goes with the URL, which is the line that
// says where it is spent.
func TestProfileRemoveKeepsACDMPinForAnotherDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	elsewhere := cdm.EndpointFor("https://blocks.other.example")
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"=https://blocks.acme.com\n"+
		cdm.URLEnv+"="+elsewhere+"\n"+blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksBackendURLEnv); got != "" {
		t.Errorf("%s = %q; the pin for the removed deployment must be gone", blocksBackendURLEnv, got)
	}
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "" {
		t.Errorf("%s = %q; the key was spent at the deployment the removed pin named", blocksAPIKeyEnv, got)
	}
	if got := envValue(t, dir, cdm.URLEnv); got != elsewhere {
		t.Errorf("%s = %q, want %q; a pin for a different deployment must survive untouched", cdm.URLEnv, got, elsewhere)
	}
}

// The mirror case, and the reason the key follows the backend pin rather than any
// match at all: here the CDM pin named the removed deployment while the URL beside it
// names one that still exists. The CDM pin goes, and the key stays — it is the
// credential for the target it still sits beside, and deleting it would strand a
// working .env.
func TestProfileRemoveKeepsTheKeyWhenTheBackendPinNamesAnotherDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"=https://blocks.other.example\n"+
		cdm.URLEnv+"="+cdm.EndpointFor("https://blocks.acme.com")+"\n"+
		blocksAPIKeyEnv+"=bk_other\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, cdm.URLEnv); got != "" {
		t.Errorf("%s = %q; the pin for the removed deployment must be gone", cdm.URLEnv, got)
	}
	if got := envValue(t, dir, blocksBackendURLEnv); got != "https://blocks.other.example" {
		t.Errorf("%s = %q; a pin for a different deployment must survive untouched", blocksBackendURLEnv, got)
	}
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_other" {
		t.Errorf("%s = %q, want %q; the key belongs to the deployment still pinned beside it", blocksAPIKeyEnv, got, "bk_other")
	}
}

// pinnedDeploymentProjectDir puts the test in a fresh project directory whose .env
// carries the given assignments and loads it exactly as the root command does at
// startup. It restores the CDM pin as well as the URL and the key, because that pin
// is one of the variables this command removes and a test that leaked it would
// change where the next test in this binary believes a runtime looks.
func pinnedDeploymentProjectDir(t *testing.T, env string) string {
	t.Helper()
	dir := writeProjectEnv(t, t.TempDir(), env)
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv, cdm.URLEnv)
	return dir
}

// A .env targeting some other deployment is deliberate and none of this command's
// business: all three of its values survive, and nothing is said.
func TestProfileRemoveKeepsAPinForAnotherDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	other := "https://blocks.other.example"
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"="+other+"\n"+
		cdm.URLEnv+"="+cdm.EndpointFor(other)+"\n"+
		blocksAPIKeyEnv+"=bk_other\n")
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	for _, want := range []struct{ key, value string }{
		{blocksBackendURLEnv, other},
		{cdm.URLEnv, cdm.EndpointFor(other)},
		{blocksAPIKeyEnv, "bk_other"},
	} {
		if got := envValue(t, dir, want.key); got != want.value {
			t.Errorf("%s = %q, want %q; a .env targeting a different deployment must survive untouched",
				want.key, got, want.value)
		}
	}
	if strings.Contains(out, blocksBackendURLEnv) {
		t.Errorf("nothing should be said about a .env that was not changed:\n%s", out)
	}
}

// No .env at all is the ordinary case: the command must succeed and stay quiet.
func TestProfileRemoveWithoutAProjectEnvSaysNothingAboutPins(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	dir := t.TempDir()
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv)
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if out != "Removed profile: acme\n" {
		t.Errorf("profile remove output:\n%q\nwant only the removal line", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf("no .env should have been created, stat err = %v", err)
	}
}

// A .env this command cannot read is a .env it cannot clean: the pin may well still
// be there, sending every later command in this directory to a deployment with no
// credentials for it. Reporting completion anyway leaves the user no reason to look.
func TestProfileRemoveReportsAnUnreadableProjectEnv(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.example", Orgs: map[string]profiles.OrgKey{}})
	unreadableEnvProjectDir(t)
	resolveCLIContext(t, profileRemoveCmd)

	var err error
	captureStdout(func() { err = profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}) })

	if err == nil {
		t.Fatal("a .env that could not be read must fail the command, not be reported as nothing to remove")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("the failure must name the file it could not clean: %v", err)
	}
}

// A custom-named Network profile is the case no pin can speak for: stock Network is
// deliberately left unpinned, so `login --network --profile acme --write-env` writes the
// key alone. Removing that profile has to take the key with it, or every later command in
// this directory keeps authenticating with a credential whose profile is gone — from the
// user's point of view, with no credentials at all.
func TestProfileRemoveDropsTheKeyItMintedWhenNothingPinsIt(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()

	srv := loginTestServer(t)
	networkRemoteConfigAt(t, srv.URL)
	dir := t.TempDir()
	t.Chdir(dir)
	runLoginArgs(t, "login", "--network", "--profile", "acme", "--api-key", "bk_minted_for_acme", "--write-env", "--dir", dir)

	// The premise, asserted rather than assumed: the key is there and nothing beside it
	// says where it is spent.
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_minted_for_acme" {
		t.Fatalf("%s = %q; the login did not write the key this test is about", blocksAPIKeyEnv, got)
	}
	if got := envValue(t, dir, blocksBackendURLEnv); got != "" {
		t.Fatalf("%s = %q; a Network login must leave no pin, or this is no longer the no-pin case", blocksBackendURLEnv, got)
	}
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksAPIKeyEnv); got != "" {
		t.Errorf("%s = %q; the key was minted for the profile just removed and must go with it", blocksAPIKeyEnv, got)
	}
}

// The guard on that rule: a key the removed profile never held is somebody else's
// credential, and this command has no business deleting it. Here the .env carries a key
// from another deployment with nothing pinned beside it — the same shape as the case
// above, differing only in whose key it is.
func TestProfileRemoveKeepsAKeyItNeverMinted(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.example",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_minted_for_acme"}},
	})
	dir := pinnedDeploymentProjectDir(t, blocksAPIKeyEnv+"=bk_minted_elsewhere\n")
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_minted_elsewhere" {
		t.Errorf("%s = %q, want %q; a key this profile never held belongs to another deployment and must survive",
			blocksAPIKeyEnv, got, "bk_minted_elsewhere")
	}
	if strings.Contains(out, blocksAPIKeyEnv) {
		t.Errorf("nothing should be said about a key that was not removed:\n%s", out)
	}
}

// A key can be recognised as one profile's only because no other profile holds it. Two
// profiles for one deployment — an alias and its host, as a second `login` can produce —
// share the cached key, and it is still the credential for a deployment that still has a
// profile after this one goes.
func TestProfileRemoveKeepsAKeyAnotherProfileAlsoHolds(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	shared := profiles.OrgKey{OrgName: "Acme", ApiKey: "bk_shared"}
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: "https://blocks.acme.example", DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	seedProfile(t, "blocks.acme.example", profiles.Profile{
		BaseURL: "https://blocks.acme.example", DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	dir := pinnedDeploymentProjectDir(t, blocksAPIKeyEnv+"=bk_shared\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_shared" {
		t.Errorf("%s = %q, want bk_shared; a key another profile still holds must survive", blocksAPIKeyEnv, got)
	}
}

// A deployment can have two names — an alias and its host, as a second `login` produces —
// and removing one of them forgets a name, not a deployment. The surviving profile still
// targets it, still holds a credential for it, and `blocks profile list` still shows it,
// so a .env pinned there is still exactly right: all three values stay, and nothing is
// said about them.
func TestProfileRemoveKeepsAPinAnotherProfileStillDescribes(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	deployment := "https://blocks.acme.example"
	shared := profiles.OrgKey{OrgName: "Acme", ApiKey: "bk_acme"}
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	seedProfile(t, "blocks.acme.example", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"="+deployment+"\n"+
		cdm.URLEnv+"="+cdm.EndpointFor(deployment)+"\n"+
		blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	for _, want := range []struct{ key, value string }{
		{blocksBackendURLEnv, deployment},
		{cdm.URLEnv, cdm.EndpointFor(deployment)},
		{blocksAPIKeyEnv, "bk_acme"},
	} {
		if got := envValue(t, dir, want.key); got != want.value {
			t.Errorf("%s = %q, want %q; a profile that still describes this deployment survives, so the .env is still correct",
				want.key, got, want.value)
		}
	}
	if strings.Contains(out, "./.env") {
		t.Errorf("nothing should be said about a .env that was not changed:\n%s", out)
	}
}

// The other end of that rule: it is the last name for a deployment that takes the
// deployment with it. The same store and the same .env as above, removing both profiles
// in turn — the first removal changes nothing, and the second, with no profile left
// describing the deployment, drops all three values.
func TestProfileRemoveDropsThePinsWithTheLastProfileForTheDeployment(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	deployment := "https://blocks.acme.example"
	shared := profiles.OrgKey{OrgName: "Acme", ApiKey: "bk_acme"}
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	seedProfile(t, "blocks.acme.example", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"="+deployment+"\n"+
		cdm.URLEnv+"="+cdm.EndpointFor(deployment)+"\n"+
		blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})
	if got := envValue(t, dir, blocksBackendURLEnv); got != deployment {
		t.Fatalf("%s = %q; the premise of this test is that removing a spare name changed nothing", blocksBackendURLEnv, got)
	}

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"blocks.acme.example"}); err != nil {
			t.Fatalf("profile remove blocks.acme.example: %v", err)
		}
	})

	for _, key := range []string{blocksBackendURLEnv, cdm.URLEnv, blocksAPIKeyEnv} {
		if got := envValue(t, dir, key); got != "" {
			t.Errorf("%s = %q; the last profile for this deployment is gone, so nothing in the .env can reach it", key, got)
		}
		if !strings.Contains(out, key) {
			t.Errorf("the message must name %s as removed:\n%s", key, out)
		}
	}
}

// Two profiles for stock Blocks Network is the case the deployment test cannot answer:
// neither records a deployment, so nothing can be compared, and the shared key can only
// be recognised as shared by its bytes. Removing one of them must still leave the key,
// because the other profile is still spending it.
func TestProfileRemoveKeepsAKeyASecondNetworkProfileAlsoHolds(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	shared := profiles.OrgKey{OrgName: "Acme", ApiKey: "bk_network"}
	seedProfile(t, "acme", profiles.Profile{
		DefaultOrgID: "org-1", Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	seedProfile(t, "acme-ci", profiles.Profile{
		DefaultOrgID: "org-1", Orgs: map[string]profiles.OrgKey{"org-1": shared},
	})
	dir := pinnedDeploymentProjectDir(t, blocksAPIKeyEnv+"=bk_network\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_network" {
		t.Errorf("%s = %q, want bk_network; a key another Network profile still holds must survive", blocksAPIKeyEnv, got)
	}
}

// Recognising a key by identity gives the refused removals something to strip, which is
// why they are refused before the .env is touched: the default profile cannot be removed,
// so the key it minted must still be there afterwards.
func TestProfileRemoveOfTheDefaultProfileLeavesItsKeyAlone(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_network"}},
	})
	dir := pinnedDeploymentProjectDir(t, blocksAPIKeyEnv+"=bk_network\n")
	resolveCLIContext(t, profileRemoveCmd)

	var err error
	captureStdout(func() { err = profileRemoveCmd.RunE(profileRemoveCmd, []string{profiles.DefaultProfile}) })

	if err == nil {
		t.Fatal("the default profile cannot be removed")
	}
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "bk_network" {
		t.Errorf("%s = %q; a removal that was refused must leave the .env untouched", blocksAPIKeyEnv, got)
	}
}

// The two halves of the removal must both happen or neither: a profile deleted while
// its targeting and its key are still in the .env is the one end state the cleanup
// exists to prevent — live credentials for a deployment `blocks profile list` no longer
// mentions, and a command that already said it was done. So a .env this command cannot
// clean leaves the profile exactly where it was, and claims nothing.
func TestProfileRemoveKeepsTheProfileWhenTheProjectEnvCannotBeCleaned(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.example", Orgs: map[string]profiles.OrgKey{}})
	unreadableEnvProjectDir(t)
	resolveCLIContext(t, profileRemoveCmd)

	var err error
	out := captureStdout(func() { err = profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}) })

	if err == nil {
		t.Fatal("a .env that could not be cleaned must fail the removal")
	}
	if strings.Contains(out, "Removed profile") {
		t.Errorf("the removal must not be announced when it did not happen:\n%s", out)
	}
	if !strings.Contains(err.Error(), `profile "acme" was not removed`) {
		t.Errorf("the failure must say which profile survived: %v", err)
	}
	store, loadErr := profiles.Load()
	if loadErr != nil {
		t.Fatalf("profiles.Load: %v", loadErr)
	}
	if _, ok := store.Profiles["acme"]; !ok {
		t.Error("the profile was deleted anyway; a removal that could not clean the .env must leave the profile listed")
	}
}

// The pre-flight refusal exists so a project .env is never rewritten for a removal that
// was never going to happen, which means it has to refuse exactly the names the store
// refuses — no more, and no fewer. Comparing the two answers directly is what keeps
// them from drifting apart as the store's rules change.
func TestProfileRemoveRefusesExactlyWhatTheStoreRefuses(t *testing.T) {
	for _, name := range []string{profiles.DefaultProfile, "never-created"} {
		t.Run(name, func(t *testing.T) {
			restoreCLIState(t)
			withTempProfiles(t)
			seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.example", Orgs: map[string]profiles.OrgKey{}})

			_, planErr := planProfileRemoval(name)
			storeErr := profiles.Remove(name)
			if planErr == nil || storeErr == nil {
				t.Fatalf("both must refuse %q: plan = %v, store = %v", name, planErr, storeErr)
			}
			if planErr.Error() != storeErr.Error() {
				t.Errorf("plan refused with %q, store with %q; the two answers must agree", planErr, storeErr)
			}
		})
	}
}

// The credential is the security-relevant half: a logout that could not read the .env
// cannot know whether BLOCKS_API_KEY is still in it, and every later command in this
// directory would keep authenticating with it. Saying "Logged out" there is the claim
// that must not be made.
func TestLogoutReportsAnUnreadableProjectEnv(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.example",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	isolateCredentials(t)
	unreadableEnvProjectDir(t)
	resolveCLIContext(t, logoutCmd)

	var err error
	out := captureStdout(func() { err = runBlocksLogout() })

	if err == nil {
		t.Fatal("a .env that could not be read must fail the logout, not be reported as success")
	}
	if !strings.Contains(err.Error(), blocksAPIKeyEnv) {
		t.Errorf("the failure must name the credential it could not remove: %v", err)
	}
	if strings.Contains(out, "Logged out") {
		t.Errorf("logout must not claim success it cannot prove:\n%s", out)
	}
}

// The ordinary case stays quiet and succeeds: most projects have no .env at all, and
// there is genuinely nothing in one to remove.
func TestLogoutWithoutAProjectEnvSucceeds(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.example",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	isolateCredentials(t)
	dir := t.TempDir()
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv, cdm.URLEnv)
	resolveCLIContext(t, logoutCmd)

	var err error
	out := captureStdout(func() { err = runBlocksLogout() })

	if err != nil {
		t.Fatalf("runBlocksLogout with no .env: %v", err)
	}
	if !strings.Contains(out, "Logged out") {
		t.Errorf("logout output:\n%s\nwant the success summary", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(statErr) {
		t.Errorf("no .env should have been created, stat err = %v", statErr)
	}
}

// unreadableEnvProjectDir puts the test in a project directory whose .env exists and
// cannot be read — it is a directory, so the read itself fails for every user — and
// runs the same startup load the root command runs, which finds nothing in it. It is
// how a command is faced with a file it can neither inspect nor rewrite.
func unreadableEnvProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("create unreadable .env: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this platform reads a directory as a file, so an unreadable .env cannot be staged this way")
	}
	t.Chdir(dir)
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv, cdm.URLEnv)
	return dir
}

// logout deliberately keeps the deployment target: the profile survives and
// `blocks login` is meant to return to it, so both halves of that target stay — the
// URL and the CDM endpoint — and only the credential goes. Only `profile remove`,
// which forgets the deployment, removes them.
func TestLogoutKeepsTheDeploymentPins(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.com",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	isolateCredentials(t)
	endpoint := cdm.EndpointFor("https://blocks.acme.com")
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"=https://blocks.acme.com\n"+
		cdm.URLEnv+"="+endpoint+"\n"+blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, logoutCmd)

	captureStdout(func() {
		if err := runBlocksLogout(); err != nil {
			t.Fatalf("runBlocksLogout: %v", err)
		}
	})

	if got := envValue(t, dir, blocksBackendURLEnv); got != "https://blocks.acme.com" {
		t.Errorf("%s = %q; logout must keep the pin so `blocks login` returns here", blocksBackendURLEnv, got)
	}
	if got := envValue(t, dir, cdm.URLEnv); got != endpoint {
		t.Errorf("%s = %q, want %q; the kept target includes the endpoint a runtime resolves from", cdm.URLEnv, got, endpoint)
	}
	if got := envValue(t, dir, blocksAPIKeyEnv); got != "" {
		t.Errorf("%s = %q; logout must still remove the key", blocksAPIKeyEnv, got)
	}
}

// storeAccessCounter counts accesses to the profile store from the point it is installed,
// and runs a hook on a chosen access so a test can place a change to the store at an exact
// moment in a command's sequence of reads and writes. It hooks profiles.ContextsPathFunc,
// which every Load and every Save asks for the path before touching the file, so an
// access is either of those.
//
// It is how the freshness boundary is tested without a real second process: a genuinely
// concurrent CLI invocation is racy by construction, so what is asserted is the property
// the fix actually establishes — that the store the removal decides from is read after the
// pre-flight and after the project .env has been read, rather than being a snapshot taken
// before either.
//
// The hook receives the store path so it can write the file directly, rather than asking
// for the path again and inflating the count it is being placed by.
func storeAccessCounter(t *testing.T, on int, hook func(path string)) *int {
	t.Helper()
	accesses := 0
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) {
		path, err := orig()
		accesses++
		if err == nil && accesses == on {
			hook(path)
		}
		return path, err
	}
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })
	return &accesses
}

// addProfileBehindTheStore writes a profile straight into contexts.json, bypassing
// profiles.Upsert so it does not re-enter the loader whose call count is what places it
// in time. It stands in for a `blocks login` that ran in another process.
func addProfileBehindTheStore(t *testing.T, path, name string, p profiles.Profile) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read contexts: %v", err)
	}
	var c profiles.Contexts
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parse contexts: %v", err)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]profiles.Profile{}
	}
	c.Profiles[name] = p
	out, err := json.Marshal(&c)
	if err != nil {
		t.Fatalf("encode contexts: %v", err)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatalf("write contexts: %v", err)
	}
}

// Whether the .env may be stripped turns on whether any profile still describes the
// deployment, and that answer has to describe the store at the moment the file is
// rewritten. Deciding it from the pre-flight's snapshot let a `blocks login` that added a
// second name for the same deployment survive the removal while its backend pin, its CDM
// pin and its key — all still served by that surviving profile, all still exactly right —
// were deleted on the strength of a store that predated it.
//
// The concurrent login is placed on the store read the removal decides from: the second
// access of the invocation, which happens after the pre-flight refusals and after the .env
// has been read. Before the decision was moved there, no such read existed — the second
// access was profiles.Remove's own — so the alias arrived only after the .env had already
// been stripped.
func TestProfileRemoveDecidesFromTheStoreAsItIsWhenTheEnvIsWritten(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	deployment := "https://blocks.acme.example"
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"="+deployment+"\n"+
		cdm.URLEnv+"="+cdm.EndpointFor(deployment)+"\n"+
		blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	accesses := storeAccessCounter(t, 2, func(path string) {
		addProfileBehindTheStore(t, path, "blocks.acme.example", profiles.Profile{
			BaseURL: deployment, DefaultOrgID: "org-1",
			Orgs: map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
		})
	})

	out := captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	// The premise: the store really was read again after the pre-flight, which is where
	// the login was placed. The removal's own sequence is the pre-flight read, the
	// decision read, then profiles.Remove's read and its write — four accesses. Three
	// means the decision read is missing and the login landed too late to matter.
	if *accesses < 4 {
		t.Fatalf("store accesses = %d; the removal must re-read the store between the pre-flight and profiles.Remove", *accesses)
	}
	for _, want := range []struct{ key, value string }{
		{blocksBackendURLEnv, deployment},
		{cdm.URLEnv, cdm.EndpointFor(deployment)},
		{blocksAPIKeyEnv, "bk_acme"},
	} {
		if got := envValue(t, dir, want.key); got != want.value {
			t.Errorf("%s = %q, want %q; a profile that describes this deployment by the time the .env is rewritten keeps every value in it",
				want.key, got, want.value)
		}
	}
	if strings.Contains(out, "./.env") {
		t.Errorf("nothing should be said about a .env that was not changed:\n%s", out)
	}

	// The removal itself still happened, and the profile that arrived mid-flight is
	// untouched: the fix is about what the .env loses, not about refusing the removal.
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if _, ok := store.Profiles["acme"]; ok {
		t.Error("the named profile must still be removed")
	}
	if _, ok := store.Profiles["blocks.acme.example"]; !ok {
		t.Error("the profile added mid-removal must survive it")
	}
}

// The mirror case, and the reason the fresh read cannot simply be trusted to find a
// survivor: with nothing added, the same removal still drops all three values. Without
// this, a fresh read that always reported "survives" would pass the test above.
func TestProfileRemoveStillDropsTheEnvWhenNoProfileArrives(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	deployment := "https://blocks.acme.example"
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: deployment, DefaultOrgID: "org-1",
		Orgs: map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	dir := pinnedDeploymentProjectDir(t, blocksBackendURLEnv+"="+deployment+"\n"+
		cdm.URLEnv+"="+cdm.EndpointFor(deployment)+"\n"+
		blocksAPIKeyEnv+"=bk_acme\n")
	resolveCLIContext(t, profileRemoveCmd)

	captureStdout(func() {
		if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile remove acme: %v", err)
		}
	})

	for _, key := range []string{blocksBackendURLEnv, cdm.URLEnv, blocksAPIKeyEnv} {
		if got := envValue(t, dir, key); got != "" {
			t.Errorf("%s = %q; with no profile left for this deployment nothing in the .env can reach it", key, got)
		}
	}
}
