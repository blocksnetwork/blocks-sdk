package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The drained store is the standard post-migration shape: the profile migration
// moves the Blocks key into contexts.json and deletes the "blocks" namespace,
// leaving the file itself (and any partner namespaces) in place. Load must say
// so with the sentinel, not an ad-hoc error, so the CLI's credential adapter can
// tell "no Blocks key here" apart from "could not read the store".
func TestLoadReturnsTheSentinelWhenTheBlocksNamespaceIsDrained(t *testing.T) {
	credPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"schema_version":3}`), 0600); err != nil {
		t.Fatalf("write drained store: %v", err)
	}
	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) { return credPath, nil }
	defer func() { CredentialPathFunc = origFunc }()

	if _, err := Load(); !errors.Is(err, ErrNoBlocksCredential) {
		t.Fatalf("Load = %v, want ErrNoBlocksCredential", err)
	}
}

func TestLoadDeleteRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) {
		return filepath.Join(tmpDir, "blocks", "credentials.json"), nil
	}
	defer func() { CredentialPathFunc = origFunc }()

	creds := &Credentials{
		ApiKey:    "bk_test_abc123",
		OrgId:     "org-12345",
		OrgName:   "Test Org",
		KeyId:     "key-67890",
		ExpiresAt: time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second),
	}

	// Seed a legacy "blocks" entry directly (the v3 shape a migrated file holds).
	credPath, _ := CredentialPathFunc()
	exp := creds.ExpiresAt
	if err := SetProviderCredential(credPath, "blocks", &ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     creds.ApiKey,
		OrgId:      creds.OrgId,
		OrgName:    creds.OrgName,
		KeyId:      creds.KeyId,
		ExpiresAt:  &exp,
	}); err != nil {
		t.Fatalf("seed legacy blocks slot: %v", err)
	}

	// Verify file permissions
	path, _ := CredentialPathFunc()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}

	// Load
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.SchemaVersion != currentSchemaVersion {
		t.Errorf("expected schema_version %d, got %d", currentSchemaVersion, loaded.SchemaVersion)
	}
	if loaded.ApiKey != creds.ApiKey {
		t.Errorf("expected ApiKey %q, got %q", creds.ApiKey, loaded.ApiKey)
	}
	if loaded.OrgId != creds.OrgId {
		t.Errorf("expected OrgId %q, got %q", creds.OrgId, loaded.OrgId)
	}
	if loaded.OrgName != creds.OrgName {
		t.Errorf("expected OrgName %q, got %q", creds.OrgName, loaded.OrgName)
	}
	if loaded.KeyId != creds.KeyId {
		t.Errorf("expected KeyId %q, got %q", creds.KeyId, loaded.KeyId)
	}

	// Delete
	if err := Delete(); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Load after delete should fail
	_, err = Load()
	if err == nil {
		t.Error("expected Load to fail after Delete")
	}
}

func TestLoadRejectsV1Credentials(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) {
		return filepath.Join(tmpDir, "blocks", "credentials.json"), nil
	}
	defer func() { CredentialPathFunc = origFunc }()

	path, _ := CredentialPathFunc()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	data := []byte(`{"schema_version":1,"blocks_token":"old-jwt"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for v1 credentials")
	}
}

func TestLoadRejectsZeroVersion(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) {
		return filepath.Join(tmpDir, "blocks", "credentials.json"), nil
	}
	defer func() { CredentialPathFunc = origFunc }()

	path, _ := CredentialPathFunc()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	data := []byte(`{"schema_version":0,"blocks_token":"old-jwt"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for v0 credentials")
	}
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	tmpDir := t.TempDir()
	origFunc := CredentialPathFunc
	CredentialPathFunc = func() (string, error) {
		return filepath.Join(tmpDir, "blocks", "credentials.json"), nil
	}
	defer func() { CredentialPathFunc = origFunc }()

	path, _ := CredentialPathFunc()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	data := []byte(`{"schema_version":99,"api_key":"test"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	_, err := Load()
	if err == nil {
		t.Error("expected error for unsupported version")
	}
}

func TestIsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"future", time.Now().Add(1 * time.Hour), false},
		{"past", time.Now().Add(-1 * time.Hour), true},
		{"just past", time.Now().Add(-1 * time.Second), true},
		{"zero value (no expiry)", time.Time{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Credentials{ExpiresAt: tt.expiresAt}
			if got := c.IsExpired(); got != tt.want {
				t.Errorf("IsExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
