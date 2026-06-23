package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeV2File writes a v2-format credentials file to path with the given fields.
func writeV2File(t *testing.T, path string, apiKey, orgID, orgName, keyID string, expiresAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	v2 := map[string]interface{}{
		"schema_version": 2,
		"api_key":        apiKey,
		"org_id":         orgID,
		"org_name":       orgName,
		"key_id":         keyID,
		"expires_at":     expiresAt.Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(v2, "", "  ")
	if err != nil {
		t.Fatalf("marshal v2: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestV2ToV3MigrationRoundTrip verifies the full migration path:
//   - write a v2 file
//   - Load() succeeds and returns the Blocks credential fields
//   - the on-disk file is rewritten to v3
//   - a second Load() succeeds without triggering a second migration
func TestV2ToV3MigrationRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, "blocks", "credentials.json")

	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) { return credPath, nil }
	defer func() { CredentialPathFunc = origFunc }()

	expiry := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	writeV2File(t, credPath, "bk_migrate_key", "org-v2", "Migrate Org", "key-v2", expiry)

	// First load triggers migration.
	creds, err := Load()
	if err != nil {
		t.Fatalf("Load after v2 write: %v", err)
	}

	// Legacy fields must be populated.
	if creds.ApiKey != "bk_migrate_key" {
		t.Errorf("ApiKey = %q, want %q", creds.ApiKey, "bk_migrate_key")
	}
	if creds.OrgId != "org-v2" {
		t.Errorf("OrgId = %q, want %q", creds.OrgId, "org-v2")
	}
	if creds.OrgName != "Migrate Org" {
		t.Errorf("OrgName = %q, want %q", creds.OrgName, "Migrate Org")
	}
	if creds.KeyId != "key-v2" {
		t.Errorf("KeyId = %q, want %q", creds.KeyId, "key-v2")
	}
	if creds.SchemaVersion != currentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", creds.SchemaVersion, currentSchemaVersion)
	}

	// On-disk file must now be v3.
	raw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("ReadFile after migration: %v", err)
	}
	var probe struct {
		SchemaVersion int              `json:"schema_version"`
		Blocks        *json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal migrated file: %v", err)
	}
	if probe.SchemaVersion != currentSchemaVersion {
		t.Errorf("on-disk schema_version = %d, want %d", probe.SchemaVersion, currentSchemaVersion)
	}
	if probe.Blocks == nil {
		t.Error("on-disk file has no 'blocks' key after migration")
	}

	// File permissions must still be 0600.
	info, err := os.Stat(credPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}

	// Second load must not trigger a second migration (schema_version is already 3).
	creds2, err := Load()
	if err != nil {
		t.Fatalf("Load (second call): %v", err)
	}
	if creds2.ApiKey != "bk_migrate_key" {
		t.Errorf("second Load ApiKey = %q, want %q", creds2.ApiKey, "bk_migrate_key")
	}
	if creds2.SchemaVersion != currentSchemaVersion {
		t.Errorf("second Load SchemaVersion = %d, want %d", creds2.SchemaVersion, currentSchemaVersion)
	}
}

// TestV2ToV3MigrationFieldsUnderBlocksKey checks that migration stores all v2
// fields under the "blocks" provider key with mint_method set correctly.
func TestV2ToV3MigrationFieldsUnderBlocksKey(t *testing.T) {
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, "blocks", "credentials.json")

	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) { return credPath, nil }
	defer func() { CredentialPathFunc = origFunc }()

	expiry := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)
	writeV2File(t, credPath, "bk_key_x", "org-x", "Org X", "kid-x", expiry)

	all, err := LoadAll(credPath)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	entry := all.Providers["blocks"]
	if entry == nil {
		t.Fatal("blocks provider entry is nil after migration")
	}
	if entry.MintMethod != "api_token_via_browser" {
		t.Errorf("mint_method = %q, want api_token_via_browser", entry.MintMethod)
	}
	if entry.ApiKey != "bk_key_x" {
		t.Errorf("api_key = %q, want bk_key_x", entry.ApiKey)
	}
	if entry.OrgId != "org-x" {
		t.Errorf("org_id = %q, want org-x", entry.OrgId)
	}
	if entry.OrgName != "Org X" {
		t.Errorf("org_name = %q, want Org X", entry.OrgName)
	}
	if entry.KeyId != "kid-x" {
		t.Errorf("key_id = %q, want kid-x", entry.KeyId)
	}
	if entry.ExpiresAt == nil {
		t.Fatal("expires_at is nil after migration")
	}
	if !entry.ExpiresAt.Equal(expiry) {
		t.Errorf("expires_at = %v, want %v", entry.ExpiresAt, expiry)
	}
}

// TestAlreadyV3NoMigration confirms that a v3 file is loaded directly without
// triggering any migration path.
func TestAlreadyV3NoMigration(t *testing.T) {
	tmpDir := t.TempDir()
	credPath := filepath.Join(tmpDir, "blocks", "credentials.json")

	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) { return credPath, nil }
	defer func() { CredentialPathFunc = origFunc }()

	// Write a pre-formed v3 file.
	v3 := map[string]interface{}{
		"schema_version": 3,
		"blocks": map[string]interface{}{
			"mint_method": "api_token_via_browser",
			"api_key":     "bk_v3_key",
			"org_id":      "org-v3",
			"org_name":    "V3 Org",
			"key_id":      "kid-v3",
		},
	}
	data, _ := json.MarshalIndent(v3, "", "  ")
	os.MkdirAll(filepath.Dir(credPath), 0700)
	os.WriteFile(credPath, data, 0600)

	// Record mtime before load.
	info1, _ := os.Stat(credPath)

	creds, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if creds.ApiKey != "bk_v3_key" {
		t.Errorf("ApiKey = %q, want bk_v3_key", creds.ApiKey)
	}

	// File should not have been rewritten (mtime unchanged).
	info2, _ := os.Stat(credPath)
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("file was rewritten on a v3 load — migration should not have run")
	}
}

// TestLoadAllEmptyWhenNoFile confirms LoadAll returns an empty AllCredentials
// when the credentials file does not exist (no error).
func TestLoadAllEmptyWhenNoFile(t *testing.T) {
	all, err := LoadAll(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("LoadAll with no file: %v", err)
	}
	if all.SchemaVersion != currentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", all.SchemaVersion, currentSchemaVersion)
	}
	if len(all.Providers) != 0 {
		t.Errorf("Providers = %v, want empty map", all.Providers)
	}
}

// TestSetAndGetProviderCredential verifies the partner-namespace helpers.
func TestSetAndGetProviderCredential(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")

	entry := &ProviderEntry{
		MintMethod:  "api_token",
		AccessToken: "cf_tok_abc",
	}
	if err := SetProviderCredential(credPath, "cloudflare", entry); err != nil {
		t.Fatalf("SetProviderCredential: %v", err)
	}

	got, err := GetProviderCredential(credPath, "cloudflare")
	if err != nil {
		t.Fatalf("GetProviderCredential: %v", err)
	}
	if got == nil {
		t.Fatal("GetProviderCredential returned nil")
	}
	if got.AccessToken != "cf_tok_abc" {
		t.Errorf("AccessToken = %q, want cf_tok_abc", got.AccessToken)
	}

	// Other providers must be absent.
	other, _ := GetProviderCredential(credPath, "vercel")
	if other != nil {
		t.Errorf("vercel entry = %v, want nil", other)
	}
}

// TestDeleteProviderCredential verifies that only the named provider is removed.
func TestDeleteProviderCredential(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")

	SetProviderCredential(credPath, "cloudflare", &ProviderEntry{AccessToken: "cf_tok"})
	SetProviderCredential(credPath, "vercel", &ProviderEntry{AccessToken: "vc_tok"})

	if err := DeleteProviderCredential(credPath, "cloudflare"); err != nil {
		t.Fatalf("DeleteProviderCredential: %v", err)
	}

	cf, _ := GetProviderCredential(credPath, "cloudflare")
	if cf != nil {
		t.Errorf("cloudflare entry still present after delete: %v", cf)
	}

	vc, _ := GetProviderCredential(credPath, "vercel")
	if vc == nil || vc.AccessToken != "vc_tok" {
		t.Errorf("vercel entry lost: %v", vc)
	}
}
