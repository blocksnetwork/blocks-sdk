package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// currentSchemaVersion is the credentials file format version.
// v3 is a namespaced multi-provider shape; v2 was a flat single-key shape.
const currentSchemaVersion = 3

// ProviderEntry holds the credential fields for one provider namespace.
// Not all fields apply to every provider; omitempty keeps the file tidy.
type ProviderEntry struct {
	MintMethod   string     `json:"mint_method"`
	AccessToken  string     `json:"access_token,omitempty"`
	ApiKey       string     `json:"api_key,omitempty"`
	OrgId        string     `json:"org_id,omitempty"`
	OrgName      string     `json:"org_name,omitempty"`
	KeyId        string     `json:"key_id,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// AllCredentials is the full v3 on-disk shape.
type AllCredentials struct {
	SchemaVersion int                       `json:"schema_version"`
	Providers     map[string]*ProviderEntry `json:"-"` // serialised via custom marshal below
}

// v3FileShape is the raw JSON structure we read and write. Providers are stored
// as top-level keys alongside schema_version so the file stays human-readable.
type v3FileShape struct {
	SchemaVersion int                       `json:"schema_version"`
	Extra         map[string]*ProviderEntry `json:"-"` // populated by custom unmarshal
}

// MarshalJSON serialises AllCredentials into the flat v3 file shape.
func (a AllCredentials) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{"schema_version": a.SchemaVersion}
	for k, v := range a.Providers {
		m[k] = v
	}
	return json.Marshal(m)
}

// UnmarshalJSON deserialises the flat v3 file shape into AllCredentials.
func (a *AllCredentials) UnmarshalJSON(data []byte) error {
	// First pass: grab schema_version and raw per-provider objects.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if sv, ok := raw["schema_version"]; ok {
		if err := json.Unmarshal(sv, &a.SchemaVersion); err != nil {
			return err
		}
	}
	a.Providers = make(map[string]*ProviderEntry)
	for k, v := range raw {
		if k == "schema_version" {
			continue
		}
		var entry ProviderEntry
		if err := json.Unmarshal(v, &entry); err != nil {
			// Ignore unknown top-level keys that are not objects.
			continue
		}
		a.Providers[k] = &entry
	}
	return nil
}

// Credentials is the legacy single-provider view used by existing callers.
// Fields map to the "blocks" namespace in the v3 file.
type Credentials struct {
	SchemaVersion int
	ApiKey        string
	OrgId         string
	OrgName       string
	KeyId         string
	ExpiresAt     time.Time
}

// CredentialPathFunc returns ~/.config/blocks/credentials.json (XDG convention).
// Respects $XDG_CONFIG_HOME if set. It is a variable to allow overriding in tests.
var CredentialPathFunc = func() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "blocks", "credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "blocks", "credentials.json"), nil
}

// v2Shape is used only during migration to read the old flat format.
type v2Shape struct {
	SchemaVersion int       `json:"schema_version"`
	ApiKey        string    `json:"api_key"`
	OrgId         string    `json:"org_id"`
	OrgName       string    `json:"org_name"`
	KeyId         string    `json:"key_id"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// migrateV2ToV3 reads a v2 flat credential file and returns an AllCredentials
// with the existing fields placed under the "blocks" provider key.
func migrateV2ToV3(data []byte) (*AllCredentials, error) {
	var old v2Shape
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, fmt.Errorf("migrate v2: %w", err)
	}
	var expiresAt *time.Time
	if !old.ExpiresAt.IsZero() {
		t := old.ExpiresAt
		expiresAt = &t
	}
	all := &AllCredentials{
		SchemaVersion: currentSchemaVersion,
		Providers: map[string]*ProviderEntry{
			"blocks": {
				MintMethod: "api_token_via_browser",
				ApiKey:     old.ApiKey,
				OrgId:      old.OrgId,
				OrgName:    old.OrgName,
				KeyId:      old.KeyId,
				ExpiresAt:  expiresAt,
			},
		},
	}
	return all, nil
}

// LoadAll loads the full v3 credential blob. If a v2 file is found it is
// migrated to v3 and written back atomically before returning. If no file
// exists, an empty v3 structure is returned (no error).
func LoadAll(path string) (*AllCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AllCredentials{
				SchemaVersion: currentSchemaVersion,
				Providers:     make(map[string]*ProviderEntry),
			}, nil
		}
		return nil, err
	}

	// Peek at schema_version to decide the parse path.
	var probe struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("credentials file is malformed: %w", err)
	}

	switch {
	case probe.SchemaVersion == 2:
		all, err := migrateV2ToV3(data)
		if err != nil {
			return nil, err
		}
		// Write back the migrated v3 file.
		if werr := SaveAll(path, all); werr != nil {
			return nil, fmt.Errorf("migrate v2→v3 write-back: %w", werr)
		}
		return all, nil

	case probe.SchemaVersion == currentSchemaVersion:
		var all AllCredentials
		if err := json.Unmarshal(data, &all); err != nil {
			return nil, fmt.Errorf("credentials file is malformed: %w", err)
		}
		return &all, nil

	case probe.SchemaVersion < currentSchemaVersion:
		return nil, fmt.Errorf("credentials file is schema v%d — run 'blocks login' to re-authenticate", probe.SchemaVersion)

	default:
		return nil, fmt.Errorf("credentials file version %d is not supported by this CLI — please upgrade blocks", probe.SchemaVersion)
	}
}

// SaveAll writes the full v3 credential blob atomically (temp file + rename)
// with mode 0600.
func SaveAll(path string, all *AllCredentials) error {
	all.SchemaVersion = currentSchemaVersion
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to a temp file then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// GetProviderCredential returns the stored ProviderEntry for the named
// provider, or (nil, nil) when no entry exists for that provider.
func GetProviderCredential(path, provider string) (*ProviderEntry, error) {
	all, err := LoadAll(path)
	if err != nil {
		return nil, err
	}
	return all.Providers[provider], nil
}

// SetProviderCredential upserts a ProviderEntry for the named provider and
// persists the change.
func SetProviderCredential(path, provider string, entry *ProviderEntry) error {
	all, err := LoadAll(path)
	if err != nil {
		return err
	}
	if all.Providers == nil {
		all.Providers = make(map[string]*ProviderEntry)
	}
	all.Providers[provider] = entry
	return SaveAll(path, all)
}

// DeleteProviderCredential removes the entry for the named provider from the
// credentials file. It is a no-op if the provider is not present.
func DeleteProviderCredential(path, provider string) error {
	all, err := LoadAll(path)
	if err != nil {
		return err
	}
	delete(all.Providers, provider)
	return SaveAll(path, all)
}

// credentialsPath returns the XDG credentials path using CredentialPathFunc.
func credentialsPath() (string, error) {
	return CredentialPathFunc()
}

// Load returns the "blocks" namespace as a legacy *Credentials struct.
// If a v2 file is found it is migrated to v3 first.
// Returns an error when no credentials file exists (matching existing behaviour).
func Load() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}

	// Ensure the file exists before calling LoadAll so we can return the
	// original "file not found" error rather than an empty struct.
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	all, err := LoadAll(path)
	if err != nil {
		return nil, err
	}

	entry := all.Providers["blocks"]
	if entry == nil {
		return nil, fmt.Errorf("no Blocks credentials found — run 'blocks login'")
	}

	var expiresAt time.Time
	if entry.ExpiresAt != nil {
		expiresAt = *entry.ExpiresAt
	}
	return &Credentials{
		SchemaVersion: all.SchemaVersion,
		ApiKey:        entry.ApiKey,
		OrgId:         entry.OrgId,
		OrgName:       entry.OrgName,
		KeyId:         entry.KeyId,
		ExpiresAt:     expiresAt,
	}, nil
}

// Save persists a *Credentials into the "blocks" namespace of the v3 file.
// Other provider namespaces are preserved.
// It sets creds.SchemaVersion to currentSchemaVersion as a side effect,
// matching the behaviour expected by existing callers.
func Save(creds *Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	// Load existing file (or start fresh) to preserve other providers.
	all, err := LoadAll(path)
	if err != nil {
		return err
	}
	if all.Providers == nil {
		all.Providers = make(map[string]*ProviderEntry)
	}

	var expiresAt *time.Time
	if !creds.ExpiresAt.IsZero() {
		t := creds.ExpiresAt
		expiresAt = &t
	}
	all.Providers["blocks"] = &ProviderEntry{
		MintMethod: "api_token_via_browser",
		ApiKey:     creds.ApiKey,
		OrgId:      creds.OrgId,
		OrgName:    creds.OrgName,
		KeyId:      creds.KeyId,
		ExpiresAt:  expiresAt,
	}
	if err := SaveAll(path, all); err != nil {
		return err
	}
	creds.SchemaVersion = currentSchemaVersion
	return nil
}

// Delete removes the credentials file entirely.
func Delete() error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// IsExpired reports whether the credential has passed its expiry time.
func (c *Credentials) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}
