package partners

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

const (
	// vercelTokenURL is the Vercel dashboard page for creating API tokens.
	// v1: browser-grant flow via a Blocks-hosted token exchange once
	// Vercel's "Sign in with Vercel" OAuth path is operationalised for CLI use.
	vercelTokenURL  = "https://vercel.com/account/tokens"
	vercelVerifyURL = "https://api.vercel.com/v2/user"

	vercelEnvVar = "VERCEL_TOKEN"
)

// VercelFlow implements auth.CredentialFlow for Vercel API tokens.
type VercelFlow struct {
	// CredsPath overrides the XDG credentials path. Pass "" to use the default.
	CredsPath string
	// Validator overrides the live token-verification call. Nil uses checkTokenHTTP.
	Validator func(ctx context.Context, token string) error
	// Reader overrides stdin for the interactive token prompt. Nil uses os.Stdin.
	Reader io.Reader
}

// Provider returns the provider identifier stored in the credentials namespace.
func (f *VercelFlow) Provider() string { return "vercel" }

// Ensure returns a Vercel API token using the following priority:
//  1. VERCEL_TOKEN environment variable — returned immediately without storage.
//  2. Stored token in the credentials file — returned without prompting.
//  3. Interactive prompt with instructions to create a token at vercelTokenURL.
//     The token is validated against the Vercel API, then stored and returned.
//
// v1 note: a browser-grant flow via a Blocks-hosted token exchange is deferred
// (Vercel's "Sign in with Vercel" OAuth embeds a client secret server-side and
// cannot run safely in a CLI without a server-side exchange endpoint).
func (f *VercelFlow) Ensure(ctx context.Context) (*auth.ProviderCredentials, error) {
	// 1. Environment variable.
	if token := os.Getenv(vercelEnvVar); token != "" {
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
		return nil, fmt.Errorf("vercel: read stored credential: %w", err)
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
			"Create a Vercel API token at %s.\nPaste the token: ",
			vercelTokenURL,
		),
		f.readerOrStdin(),
	)
	if err != nil {
		return nil, fmt.Errorf("vercel: read token: %w", err)
	}

	if err := f.validatorFn()(ctx, token); err != nil {
		return nil, fmt.Errorf("vercel: %w", err)
	}

	newEntry := &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: token,
	}
	if err := auth.SetProviderCredential(credsPath, f.Provider(), newEntry); err != nil {
		return nil, fmt.Errorf("vercel: store credential: %w", err)
	}

	return &auth.ProviderCredentials{
		Provider:    f.Provider(),
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}, nil
}

func (f *VercelFlow) resolvedPath() (string, error) {
	if f.CredsPath != "" {
		return f.CredsPath, nil
	}
	return auth.CredentialPathFunc()
}

func (f *VercelFlow) readerOrStdin() io.Reader {
	if f.Reader != nil {
		return f.Reader
	}
	return os.Stdin
}

func (f *VercelFlow) validatorFn() func(ctx context.Context, token string) error {
	if f.Validator != nil {
		return f.Validator
	}
	return func(ctx context.Context, token string) error {
		return checkTokenHTTP(ctx, vercelVerifyURL, token)
	}
}
