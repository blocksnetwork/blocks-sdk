package partners

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

const (
	// netlifyTokenURL is the Netlify dashboard page for creating Personal Access Tokens.
	// v1: browser-grant flow deferred until embedding Netlify's CLI browser-token
	// flow in a third-party CLI is verified safe.
	netlifyTokenURL  = "https://app.netlify.com/user/applications"
	netlifyVerifyURL = "https://api.netlify.com/api/v1/user"

	netlifyEnvVar = "NETLIFY_AUTH_TOKEN"
)

// NetlifyFlow implements auth.CredentialFlow for Netlify API tokens.
type NetlifyFlow struct {
	// CredsPath overrides the XDG credentials path. Pass "" to use the default.
	CredsPath string
	// Validator overrides the live token-verification call. Nil uses checkTokenHTTP.
	Validator func(ctx context.Context, token string) error
	// Reader overrides stdin for the interactive token prompt. Nil uses os.Stdin.
	Reader io.Reader
}

// Provider returns the provider identifier stored in the credentials namespace.
func (f *NetlifyFlow) Provider() string { return "netlify" }

// Ensure returns a Netlify API token using the following priority:
//  1. NETLIFY_AUTH_TOKEN environment variable — returned immediately without storage.
//  2. Stored token in the credentials file — returned without prompting.
//  3. Interactive prompt with instructions to create a token at netlifyTokenURL.
//     The token is validated against the Netlify API, then stored and returned.
//
// v1 note: a browser-grant flow is deferred until it is confirmed safe to embed
// Netlify's browser-token flow inside a third-party CLI.
func (f *NetlifyFlow) Ensure(ctx context.Context) (*auth.ProviderCredentials, error) {
	// 1. Environment variable.
	if token := os.Getenv(netlifyEnvVar); token != "" {
		return &auth.ProviderCredentials{
			Provider:    f.Provider(),
			Kind:        auth.CredentialKindAPIToken,
			AccessToken: token,
		}, nil
	}

	credsPath, err := f.resolvedPath()
	if err != nil {
		return nil, err
	}

	// 2. Stored credential.
	entry, err := auth.GetProviderCredential(credsPath, f.Provider())
	if err != nil {
		return nil, fmt.Errorf("netlify: read stored credential: %w", err)
	}
	if entry != nil && entry.AccessToken != "" {
		return &auth.ProviderCredentials{
			Provider:    f.Provider(),
			Kind:        auth.CredentialKindAPIToken,
			AccessToken: entry.AccessToken,
		}, nil
	}

	// 3. Interactive prompt.
	token, err := promptToken(
		fmt.Sprintf(
			"Create a Netlify Personal Access Token at %s.\nPaste the token: ",
			netlifyTokenURL,
		),
		f.readerOrStdin(),
	)
	if err != nil {
		return nil, fmt.Errorf("netlify: read token: %w", err)
	}

	if err := f.validatorFn()(ctx, token); err != nil {
		return nil, fmt.Errorf("netlify: %w", err)
	}

	newEntry := &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: token,
	}
	if err := auth.SetProviderCredential(credsPath, f.Provider(), newEntry); err != nil {
		return nil, fmt.Errorf("netlify: store credential: %w", err)
	}

	return &auth.ProviderCredentials{
		Provider:    f.Provider(),
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}, nil
}

func (f *NetlifyFlow) resolvedPath() (string, error) {
	if f.CredsPath != "" {
		return f.CredsPath, nil
	}
	return auth.CredentialPathFunc()
}

func (f *NetlifyFlow) readerOrStdin() io.Reader {
	if f.Reader != nil {
		return f.Reader
	}
	return os.Stdin
}

func (f *NetlifyFlow) validatorFn() func(ctx context.Context, token string) error {
	if f.Validator != nil {
		return f.Validator
	}
	return func(ctx context.Context, token string) error {
		return checkTokenHTTP(ctx, netlifyVerifyURL, token)
	}
}
