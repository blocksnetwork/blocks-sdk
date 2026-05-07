package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// currentSchemaVersion is the credentials file format version.
// v2 uses API key authentication instead of JWT tokens.
const currentSchemaVersion = 2

type Credentials struct {
	SchemaVersion int       `json:"schema_version"`
	ApiKey        string    `json:"api_key"`
	OrgId         string    `json:"org_id"`
	OrgName       string    `json:"org_name"`
	KeyId         string    `json:"key_id"`
	ExpiresAt     time.Time `json:"expires_at"`
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

func Load() (*Credentials, error) {
	path, err := CredentialPathFunc()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	if creds.SchemaVersion < currentSchemaVersion {
		return nil, fmt.Errorf("credentials file is schema v%d — run 'blocks publish' to re-authenticate", creds.SchemaVersion)
	}
	if creds.SchemaVersion > currentSchemaVersion {
		return nil, fmt.Errorf("credentials file version %d is not supported by this CLI — please upgrade blocks", creds.SchemaVersion)
	}
	return &creds, nil
}

func Save(creds *Credentials) error {
	if creds.SchemaVersion == 0 {
		creds.SchemaVersion = currentSchemaVersion
	}
	path, err := CredentialPathFunc()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func Delete() error {
	path, err := CredentialPathFunc()
	if err != nil {
		return err
	}
	return os.Remove(path)
}

func (c *Credentials) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}
