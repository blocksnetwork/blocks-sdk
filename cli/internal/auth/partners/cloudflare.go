// Package partners provides per-partner CredentialFlow adapters.
// All v0 adapters are API-token-first; browser-grant flows are deferred to v1.
package partners

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

const (
	// cloudflareTokenURL is the Cloudflare dashboard page for creating API tokens.
	// Required scopes: "Cloudflare Pages: Edit" and "Account: Read".
	cloudflareTokenURL  = "https://dash.cloudflare.com/profile/api-tokens"
	// cloudflareVerifyURL lists accounts, which requires the "Account: Read" scope
	// that we already ask users to add. The user/tokens/verify endpoint requires
	// "User: API Tokens: Read" — a scope Pages tokens don't normally include.
	cloudflareVerifyURL = "https://api.cloudflare.com/client/v4/accounts"

	cloudflareEnvVar = "CLOUDFLARE_API_TOKEN"
)

// CloudflareFlow implements auth.CredentialFlow for Cloudflare API tokens.
type CloudflareFlow struct {
	// CredsPath overrides the XDG credentials path. Pass "" to use the default.
	CredsPath string
	// Validator overrides the live token-verification call. Nil uses checkTokenHTTP.
	Validator func(ctx context.Context, token string) error
	// Reader overrides stdin for the interactive token prompt. Nil uses os.Stdin.
	Reader io.Reader
}

// Provider returns the provider identifier stored in the credentials namespace.
func (f *CloudflareFlow) Provider() string { return "cloudflare" }

// Ensure returns a Cloudflare API token using the following priority:
//  1. CLOUDFLARE_API_TOKEN environment variable — returned immediately without storage.
//  2. Stored token in the credentials file — returned without prompting.
//  3. Interactive prompt with instructions to create a token at cloudflareTokenURL.
//     The token is validated against the Cloudflare API, then stored and returned.
func (f *CloudflareFlow) Ensure(ctx context.Context) (*auth.ProviderCredentials, error) {
	// 1. Environment variable.
	if token := os.Getenv(cloudflareEnvVar); token != "" {
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
		return nil, fmt.Errorf("cloudflare: read stored credential: %w", err)
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
			"To deploy to Cloudflare Pages, create an API token at %s\n"+
				"with 'Cloudflare Pages: Edit' and 'Account: Read' scopes.\n"+
				"Paste the token here: ",
			cloudflareTokenURL,
		),
		f.readerOrStdin(),
	)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: read token: %w", err)
	}

	if err := f.validatorFn()(ctx, token); err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err)
	}

	newEntry := &auth.ProviderEntry{
		MintMethod:  string(auth.CredentialKindAPIToken),
		AccessToken: token,
	}
	if err := auth.SetProviderCredential(credsPath, f.Provider(), newEntry); err != nil {
		return nil, fmt.Errorf("cloudflare: store credential: %w", err)
	}

	return &auth.ProviderCredentials{
		Provider:    f.Provider(),
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}, nil
}

func (f *CloudflareFlow) resolvedPath() (string, error) {
	if f.CredsPath != "" {
		return f.CredsPath, nil
	}
	return auth.CredentialPathFunc()
}

func (f *CloudflareFlow) readerOrStdin() io.Reader {
	if f.Reader != nil {
		return f.Reader
	}
	return os.Stdin
}

func (f *CloudflareFlow) validatorFn() func(ctx context.Context, token string) error {
	if f.Validator != nil {
		return f.Validator
	}
	return func(ctx context.Context, token string) error {
		return checkTokenHTTP(ctx, cloudflareVerifyURL, token)
	}
}

// promptToken prints prompt to stdout and reads a single line from r.
func promptToken(prompt string, r io.Reader) (string, error) {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no input received")
	}
	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return "", fmt.Errorf("token cannot be empty")
	}
	return token, nil
}
