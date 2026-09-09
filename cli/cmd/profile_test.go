package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func withTempProfiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	// Commands that re-resolve the CLI context leave it pointed at this temp store,
	// which is gone by the time the next test runs, so put a real resolution back.
	t.Cleanup(func() {
		profiles.ContextsPathFunc = orig
		clictx.Reset()
		clictx.Resolve(nil)
	})
	return dir
}

func TestProfileUseAndList(t *testing.T) {
	withTempProfiles(t)

	if err := profiles.Upsert("acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := profiles.SetActive("acme"); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	name, _, err := profiles.Active()
	if err != nil || name != "acme" {
		t.Fatalf("expected acme active, got %q (%v)", name, err)
	}
}

func TestProfileUseCommand(t *testing.T) {
	withTempProfiles(t)

	if err := profiles.Upsert("acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := profileUseCmd.RunE(profileUseCmd, []string{"acme"}); err != nil {
		t.Fatalf("profile use acme: %v", err)
	}
	name, _, err := profiles.Active()
	if err != nil || name != "acme" {
		t.Fatalf("expected acme active after 'use', got %q (%v)", name, err)
	}

	if err := profileUseCmd.RunE(profileUseCmd, []string{"does-not-exist"}); err == nil {
		t.Fatalf("profile use of unknown profile should error")
	}
}

func TestProfileListCommand(t *testing.T) {
	withTempProfiles(t)

	if err := profiles.Upsert("acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := profileListCmd.RunE(profileListCmd, nil); err != nil {
		t.Fatalf("profile list: %v", err)
	}
}

func TestProfileRenameCommand(t *testing.T) {
	withTempProfiles(t)

	if err := profiles.Upsert("localhost:3001", profiles.Profile{BaseURL: "http://localhost:3001", Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := profileRenameCmd.RunE(profileRenameCmd, []string{"localhost:3001", "local-enterprise"}); err != nil {
		t.Fatalf("profile rename: %v", err)
	}
	name, p, err := profiles.Active()
	if err != nil || name != "local-enterprise" || !p.Enterprise {
		t.Fatalf("active should follow the rename, got %q p=%+v (%v)", name, p, err)
	}

	if err := profileRenameCmd.RunE(profileRenameCmd, []string{"does-not-exist", "x"}); err == nil {
		t.Fatalf("renaming an unknown profile should error")
	}
}

func TestProfileRemoveCommand(t *testing.T) {
	withTempProfiles(t)

	if err := profiles.Upsert("acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]profiles.OrgKey{}}, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{"acme"}); err != nil {
		t.Fatalf("profile remove acme: %v", err)
	}
	name, _, err := profiles.Active()
	if err != nil || name != profiles.DefaultProfile {
		t.Fatalf("active should re-point to default after removing active, got %q (%v)", name, err)
	}

	if err := profileRemoveCmd.RunE(profileRemoveCmd, []string{profiles.DefaultProfile}); err == nil {
		t.Fatalf("profile remove of default should error")
	}
}

// injectedRemovalProfile is a profile name shaped to be executed rather than read. A
// profile name is attacker-influenceable: it reaches the store from the host of whatever
// deployment a login reached, and that host can come from a project .env or from the
// deployment's own discovery response.
const injectedRemovalProfile = "acme.example.test; id $(id)"

// readOnlyProfileStore makes the store readable and unwritable, which is the one way to
// reach the partial outcome the retry message exists for: the plan and the .env cleanup
// both succeed, and only the store rewrite fails.
func readOnlyProfileStore(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "contexts.json")
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatalf("chmod contexts.json: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	if err := os.WriteFile(path, []byte("{}"), 0600); err == nil {
		t.Skip("this platform writes a read-only file, so a store that resists rewriting cannot be staged this way")
	}
}

// The .env cleaned and the store rewrite failing is the one partial outcome this command
// can leave behind, and the message reporting it told the user to run a command with the
// profile name interpolated into it — the third place in this CLI to build a pasteable
// command out of an untrusted value. %q makes the name safe to display and says nothing
// about whether it is safe to run: `;`, `&&`, backticks and $(...) are ordinary printable
// characters. So the name is reported where it is read, and the retry names the command
// and leaves the value to the user.
//
// The scan reuses commandLines, this package's one definition of "reads as something to
// run", so a retry worded differently in future is covered by this case rather than by a
// report.
func TestTheProfileRemovalRetryOffersNoPastableCommandBuiltFromTheProfile(t *testing.T) {
	restoreCLIState(t)
	dir := withTempProfiles(t)
	seedProfile(t, injectedRemovalProfile, profiles.Profile{
		BaseURL: "https://acme.example.test",
		Orgs:    map[string]profiles.OrgKey{},
	})
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte(blocksBackendURLEnv+"=https://acme.example.test\n"+blocksAPIKeyEnv+"=bk_acme\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	readOnlyProfileStore(t, dir)

	var err error
	out := captureStdout(func() { err = runProfileRemove(injectedRemovalProfile, envPath) })

	if err == nil {
		t.Fatalf("premise: a store that cannot be rewritten must fail the removal:\n%s", out)
	}
	if !strings.Contains(err.Error(), blocksBackendURLEnv) {
		t.Fatalf("premise: the .env must have been cleaned, which is what this message reports: %v", err)
	}
	assertNoInjectedValueInCommandLines(t, err.Error(), injectedRemovalProfile)
	// Both halves have to survive the fix: the name is still reported, because a profile
	// the user cannot identify is not one they can remove, and the retry is still offered.
	if !strings.Contains(err.Error(), injectedRemovalProfile) {
		t.Errorf("the failure must still name the profile that survived: %v", err)
	}
	if !strings.Contains(err.Error(), "blocks profile remove") {
		t.Errorf("the retry must still name the command that finishes the removal: %v", err)
	}
}
