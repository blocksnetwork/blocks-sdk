package cmd

import (
	"path/filepath"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func withTempProfiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	t.Cleanup(func() { profiles.ContextsPathFunc = orig })
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
