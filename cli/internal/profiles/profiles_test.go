package profiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

func withTempPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origCtx := ContextsPathFunc
	origCred := auth.CredentialPathFunc
	ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	auth.CredentialPathFunc = func() (string, error) { return filepath.Join(dir, "credentials.json"), nil }
	t.Cleanup(func() { ContextsPathFunc = origCtx; auth.CredentialPathFunc = origCred })
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempPaths(t)
	c := &Contexts{
		Active: "acme",
		Profiles: map[string]Profile{
			"acme": {BaseURL: "https://blocks.acme.com", Enterprise: true, ProductName: "Acme AI Hub", DefaultOrgID: "o1",
				Orgs: map[string]OrgKey{"o1": {OrgName: "Finance", ApiKey: "bk_x", KeyId: "k1", ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second)}}},
		},
	}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Active != "acme" || !got.Profiles["acme"].Enterprise || got.Profiles["acme"].Orgs["o1"].ApiKey != "bk_x" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestMigratesLegacyCredentials(t *testing.T) {
	withTempPaths(t)
	// No contexts.json, but a legacy credentials.json "blocks" slot exists.
	credPath, _ := auth.CredentialPathFunc()
	if err := auth.SetProviderCredential(credPath, "blocks", &auth.ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     "bk_legacy",
		OrgId:      "o9",
		OrgName:    "Eng",
		KeyId:      "k9",
	}); err != nil {
		t.Fatalf("seed creds: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := c.Profiles["blocks-network"]
	if !ok || c.Active != "blocks-network" {
		t.Fatalf("expected migrated blocks-network profile, got %+v", c)
	}
	if p.DefaultOrgID != "o9" || p.Orgs["o9"].ApiKey != "bk_legacy" {
		t.Fatalf("legacy key not migrated: %+v", p)
	}
}

func TestMigrateDrainsLegacyBlocksSlot(t *testing.T) {
	withTempPaths(t)
	credPath, _ := auth.CredentialPathFunc()
	if err := auth.SetProviderCredential(credPath, "blocks", &auth.ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     "bk_legacy_drain",
		OrgId:      "o1",
		OrgName:    "Eng",
		KeyId:      "k1",
	}); err != nil {
		t.Fatalf("seed creds: %v", err)
	}
	// Keep a partner token to prove only the "blocks" slot is drained.
	if err := auth.SetProviderCredential(credPath, "cloudflare", &auth.ProviderEntry{
		MintMethod: "api_token", AccessToken: "cf_keep",
	}); err != nil {
		t.Fatalf("seed cloudflare: %v", err)
	}

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	blocks, err := auth.GetProviderCredential(credPath, "blocks")
	if err != nil {
		t.Fatalf("GetProviderCredential blocks: %v", err)
	}
	if blocks != nil {
		t.Errorf("legacy blocks slot not drained: %+v", blocks)
	}
	cf, _ := auth.GetProviderCredential(credPath, "cloudflare")
	if cf == nil || cf.AccessToken != "cf_keep" {
		t.Errorf("cloudflare token should be preserved, got %+v", cf)
	}
}

func TestLoadEmptyReturnsDefaultProfile(t *testing.T) {
	withTempPaths(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Active != "blocks-network" || len(c.Profiles) != 1 {
		t.Fatalf("expected a single default profile, got %+v", c)
	}
}

func TestActiveResolution(t *testing.T) {
	withTempPaths(t)
	c := &Contexts{Active: "acme", Profiles: map[string]Profile{
		"blocks-network": {Orgs: map[string]OrgKey{}},
		"acme":           {BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]OrgKey{}},
	}}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	name, p, err := Active()
	if err != nil || name != "acme" || !p.Enterprise {
		t.Fatalf("file active: got name=%q p=%+v err=%v", name, p, err)
	}

	t.Setenv("BLOCKS_PROFILE", "blocks-network")
	name, p, err = Active()
	if err != nil || name != "blocks-network" || p.Enterprise {
		t.Fatalf("env override: got name=%q p=%+v err=%v", name, p, err)
	}

	os.Unsetenv("BLOCKS_PROFILE")
	if _, _, err := SetActive("does-not-exist"); err == nil {
		t.Fatalf("SetActive should reject unknown profile")
	}
}

func TestSetActiveOverrideWins(t *testing.T) {
	withTempPaths(t)
	c := &Contexts{Active: "blocks-network", Profiles: map[string]Profile{
		"blocks-network": {Orgs: map[string]OrgKey{}},
		"acme":           {BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]OrgKey{}},
	}}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	t.Setenv("BLOCKS_PROFILE", "blocks-network")
	SetActiveOverride("acme")
	t.Cleanup(func() { SetActiveOverride("") })

	name, p, err := Active()
	if err != nil || name != "acme" || !p.Enterprise {
		t.Fatalf("override should beat env+file: got name=%q p=%+v err=%v", name, p, err)
	}
}

func TestRemoveRepointsActiveAndProtectsDefault(t *testing.T) {
	withTempPaths(t)
	c := &Contexts{Active: "acme", Profiles: map[string]Profile{
		"blocks-network": {Orgs: map[string]OrgKey{}},
		"acme":           {BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]OrgKey{}},
	}}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Remove(DefaultProfile); err == nil {
		t.Fatalf("Remove should refuse to delete %q", DefaultProfile)
	}
	if err := Remove("acme"); err != nil {
		t.Fatalf("Remove acme: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Profiles["acme"]; ok {
		t.Fatalf("acme should be gone: %+v", got)
	}
	if got.Active != DefaultProfile {
		t.Fatalf("active should re-point to default, got %q", got.Active)
	}
	if err := Remove("acme"); err == nil {
		t.Fatalf("Remove of missing profile should error")
	}
}

func TestRenameProfile(t *testing.T) {
	withTempPaths(t)
	c := &Contexts{Active: "acme", Profiles: map[string]Profile{
		"blocks-network": {Orgs: map[string]OrgKey{}},
		"acme":           {BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]OrgKey{}},
	}}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := Rename("acme", "acme-prod"); err != nil {
		t.Fatalf("Rename acme: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got.Profiles["acme"]; ok {
		t.Fatalf("old name should be gone: %+v", got.Profiles)
	}
	if p, ok := got.Profiles["acme-prod"]; !ok || !p.Enterprise || p.BaseURL != "https://blocks.acme.com" {
		t.Fatalf("renamed profile should preserve data: %+v ok=%v", p, ok)
	}
	if got.Active != "acme-prod" {
		t.Fatalf("active should follow the rename, got %q", got.Active)
	}

	for _, tc := range []struct{ from, to, desc string }{
		{DefaultProfile, "nope", "refuse the default profile"},
		{"missing", "x", "missing source profile"},
		{"acme-prod", DefaultProfile, "onto an existing name"},
		{"acme-prod", "", "empty target name"},
	} {
		if err := Rename(tc.from, tc.to); err == nil {
			t.Fatalf("Rename %q→%q should error (%s)", tc.from, tc.to, tc.desc)
		}
	}
}

func TestSelectedName(t *testing.T) {
	withTempPaths(t)
	SetActiveOverride("")
	if got := SelectedName(); got != "" {
		t.Fatalf("no override/env should yield empty, got %q", got)
	}

	t.Setenv("BLOCKS_PROFILE", "from-env")
	if got := SelectedName(); got != "from-env" {
		t.Fatalf("env should be selected, got %q", got)
	}

	SetActiveOverride("from-flag")
	t.Cleanup(func() { SetActiveOverride("") })
	if got := SelectedName(); got != "from-flag" {
		t.Fatalf("--profile override should beat env, got %q", got)
	}
}

func TestUpsertMakeActive(t *testing.T) {
	withTempPaths(t)
	if err := Upsert("acme", Profile{BaseURL: "https://blocks.acme.com", Enterprise: true, Orgs: map[string]OrgKey{}}, true); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	name, p, err := Active()
	if err != nil || name != "acme" || !p.Enterprise {
		t.Fatalf("upsert+active: got name=%q p=%+v err=%v", name, p, err)
	}
}

func TestDefaultOrgKey(t *testing.T) {
	// Explicit DefaultOrgID wins.
	p := &Profile{DefaultOrgID: "o1", Orgs: map[string]OrgKey{
		"o1": {ApiKey: "bk_1"},
		"o2": {ApiKey: "bk_2"},
	}}
	k, ok := p.DefaultOrgKey()
	if !ok || k.ApiKey != "bk_1" {
		t.Fatalf("expected DefaultOrgID key, got %+v ok=%v", k, ok)
	}

	// Sole org fallback when no DefaultOrgID.
	p = &Profile{Orgs: map[string]OrgKey{"only": {ApiKey: "bk_only"}}}
	k, ok = p.DefaultOrgKey()
	if !ok || k.ApiKey != "bk_only" {
		t.Fatalf("expected sole-org fallback, got %+v ok=%v", k, ok)
	}

	// Ambiguous (multiple orgs, no default) → not ok.
	p = &Profile{Orgs: map[string]OrgKey{"a": {ApiKey: "bk_a"}, "b": {ApiKey: "bk_b"}}}
	if _, ok := p.DefaultOrgKey(); ok {
		t.Fatalf("expected ok=false for ambiguous multi-org profile")
	}

	// No orgs → not ok.
	p = &Profile{Orgs: map[string]OrgKey{}}
	if _, ok := p.DefaultOrgKey(); ok {
		t.Fatalf("expected ok=false for empty profile")
	}
}

func TestLoadRejectsFutureSchema(t *testing.T) {
	dir := withTempPaths(t)
	path := filepath.Join(dir, "contexts.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":4,"active":"blocks-network","profiles":{}}`), 0600); err != nil {
		t.Fatalf("seed future-version file: %v", err)
	}
	if _, err := Load(); err == nil {
		t.Fatalf("Load should reject schema_version > %d", schemaVersion)
	}
}

func TestSaveUsesRestrictivePerms(t *testing.T) {
	dir := withTempPaths(t)
	if err := Save(&Contexts{Active: DefaultProfile, Profiles: map[string]Profile{DefaultProfile: {Orgs: map[string]OrgKey{}}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "contexts.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected 0600, got %o", info.Mode().Perm())
	}
}

func TestOrgKeyIsExpired(t *testing.T) {
	if (OrgKey{}).IsExpired() {
		t.Fatalf("zero ExpiresAt must not be expired")
	}
	if !(OrgKey{ExpiresAt: time.Now().Add(-time.Hour)}).IsExpired() {
		t.Fatalf("past ExpiresAt must be expired")
	}
	if (OrgKey{ExpiresAt: time.Now().Add(time.Hour)}).IsExpired() {
		t.Fatalf("future ExpiresAt must not be expired")
	}
}
