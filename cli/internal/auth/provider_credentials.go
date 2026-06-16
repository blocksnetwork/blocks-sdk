package auth

import (
	"context"
	"time"
)

// CredentialKind describes how a provider credential was obtained.
type CredentialKind string

const (
	// CredentialKindBrowserGrant is reserved for v1 browser-grant flows.
	CredentialKindBrowserGrant CredentialKind = "browser_grant"
	// CredentialKindAPIToken represents a paste-in API token (all v0 partners).
	CredentialKindAPIToken CredentialKind = "api_token"
)

// ProviderCredentials holds the resolved credential for a single provider.
// RefreshToken and ExpiresAt are reserved for future browser-grant flows.
type ProviderCredentials struct {
	Provider     string
	Kind         CredentialKind
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
}

// CredentialFlow is implemented by each per-partner adapter.
// Ensure returns an existing or newly-stored credential, prompting the
// user when necessary.
type CredentialFlow interface {
	Provider() string
	Ensure(ctx context.Context) (*ProviderCredentials, error)
}
