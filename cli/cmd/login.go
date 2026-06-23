package cmd

import (
	"bufio"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/auth/partners"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/spf13/cobra"
)

var loginApiKey string
var loginApiKeyStdin bool
var loginWriteEnv bool
var loginProvider string
var loginNoWriteEnv bool
var loginDir string

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&loginApiKey, "api-key", "", "Use a pre-obtained API key")
	loginCmd.Flags().BoolVar(&loginApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	loginCmd.Flags().BoolVar(&loginWriteEnv, "write-env", false, "Also write BLOCKS_API_KEY to the project .env (non-interactive)")
	loginCmd.Flags().StringVar(&loginProvider, "provider", "blocks", "Provider to authenticate: blocks|cloudflare|vercel|netlify")
	loginCmd.Flags().BoolVar(&loginNoWriteEnv, "no-write-env", false, "Skip writing BLOCKS_API_KEY to the project .env and skip the prompt")
	loginCmd.Flags().StringVar(&loginDir, "dir", "", "Directory to write .env into (default: current directory)")
	loginCmd.MarkFlagsMutuallyExclusive("write-env", "no-write-env")
}

var loginCmd = &cobra.Command{
	Use:   "login [instanceUrl]",
	Short: "Authenticate and store API credentials",
	Long: `Authenticates via browser login (or provided key) and stores the API key
in the active profile. An optional instance URL targets a specific deployment;
the CLI discovers whether it is an enterprise instance (and its branding /
OAuth client id) before authenticating. With no argument, the active profile's
deployment is reused (defaults to Blocks Network). Pass --profile <name> to
store the deployment under a custom profile name instead of its host (an
existing profile can be renamed with 'blocks profile rename'). Always performs
a fresh login even if credentials already exist. In an interactive terminal, offers to
write BLOCKS_API_KEY into the project .env; pass --write-env to opt in or
--no-write-env to opt out non-interactively (recommended for coding-agent /
scripted use). When stdin is not a TTY and no flag is given, the prompt is
skipped and .env is left untouched.

Non-interactive usage (CI / automation):
  blocks login --api-key <key> --write-env --dir ./my_agent
  blocks login https://blocks.acme.com --no-write-env
  blocks login https://blocks.acme.com --profile acme`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		instanceURL := ""
		if len(args) > 0 {
			instanceURL = strings.TrimRight(args[0], "/")
		}
		return runLogin(ctx, instanceURL)
	},
}

func runLogin(ctx context.Context, instanceURL string) error {
	switch loginProvider {
	case "blocks", "":
		return runBlocksLogin(ctx, instanceURL)
	case "cloudflare":
		return runPartnerLogin(ctx, &partners.CloudflareFlow{})
	case "vercel":
		return runPartnerLogin(ctx, &partners.VercelFlow{})
	case "netlify":
		return runPartnerLogin(ctx, &partners.NetlifyFlow{})
	default:
		return fmt.Errorf("unknown provider %q — must be one of: blocks, cloudflare, vercel, netlify", loginProvider)
	}
}

// runPartnerLogin runs the Ensure flow for a partner adapter and prints
// a confirmation message. Existing Blocks login is unaffected.
func runPartnerLogin(ctx context.Context, flow auth.CredentialFlow) error {
	creds, err := flow.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	providerName := strings.ToUpper(creds.Provider[:1]) + creds.Provider[1:]
	fmt.Printf("Logged in to %s\n", providerName)
	return nil
}

// runBlocksLogin runs the Blocks login path and reports the result. All
// credential persistence is delegated to loginToProfile; this function owns only
// the user-facing messaging and the optional .env write.
func runBlocksLogin(ctx context.Context, instanceURL string) error {
	apiKey, profileName, err := loginToProfile(ctx, instanceURL)
	if err != nil {
		return err
	}
	fmt.Printf("✓ Logged in to %s (profile: %s)\n", branding.ProductName(), profileName)
	fmt.Printf("  API key: %s\n", registry.MaskAPIKey(apiKey))
	return maybeWriteEnv(apiKey)
}

// loginToProfile is the single source of truth for "log in and store a Blocks
// credential". It discovers enterprise/branding/oauth metadata for the target
// deployment, runs the browser (or --api-key) flow, and persists the minted key
// under the resolved profile in contexts.json. Both `blocks login` and the init
// webapp wizard call it, so neither can diverge on where the key is stored. It
// does not print or touch .env — the caller owns UX. Returns the API key and the
// profile name it was stored under.
func loginToProfile(ctx context.Context, instanceURL string) (string, string, error) {
	backendURL := resolveLoginBackend(instanceURL)

	// Discover enterprise/branding/oauth before the OAuth flow. Lenient: a 404
	// or empty URL yields a non-enterprise zero value (nil-safe handling below).
	// Branding must be applied here (not after) so OAuth-flow prompts are branded.
	disco, _ := cliconfig.Fetch(backendURL)
	applyBranding(disco)
	clientID := resolveLoginClientID(disco)

	// Browser path: evict any stale legacy "blocks" credential from
	// credentials.json so the migration-fallback readers (publish's fallback,
	// profiles.Load's migration) can't resurface the old key after this login
	// persists the freshly minted key to the profile store. Remove only the
	// "blocks" namespace so partner credentials are preserved. (--api-key paths
	// skip this — they don't run the browser flow.)
	if loginApiKey == "" && !loginApiKeyStdin {
		if credPath, pathErr := auth.CredentialPathFunc(); pathErr == nil {
			_ = auth.DeleteProviderCredential(credPath, "blocks")
		}
	}

	minted, apiKey, err := auth.EnsureCredentialsProfile(ctx, backendURL, clientID, loginApiKey, loginApiKeyStdin)
	if err != nil {
		return "", "", fmt.Errorf("login failed: %w", err)
	}

	profileName := resolveProfileName(instanceURL)

	// Load the existing profile (if any) and MERGE — so re-login preserves the
	// deployment target and other cached org keys instead of wiping them.
	store, err := profiles.Load()
	if err != nil {
		return "", "", err
	}
	p := store.Profiles[profileName] // zero value if new
	if p.Orgs == nil {
		p.Orgs = map[string]profiles.OrgKey{}
	}
	mergeDiscovery(&p, instanceURL, disco)

	orgId, orgName, err := resolveOrgForKey(backendURL, apiKey, minted)
	if err != nil {
		return "", "", err
	}
	if orgId != "" {
		p.DefaultOrgID = orgId
		p.Orgs[orgId] = profiles.OrgKey{OrgName: orgName, ApiKey: apiKey, KeyId: minted.KeyId, ExpiresAt: minted.ExpiresAt}
	}

	if err := profiles.Upsert(profileName, p, true); err != nil {
		return "", "", fmt.Errorf("failed to save profile: %w", err)
	}
	return apiKey, profileName, nil
}

// resolveLoginBackend picks the target backend for login: an explicit instance
// URL wins, then the active profile's BaseURL, then env/CDM resolution. The
// active-profile check is deliberately ahead of resolveBackendURL so a profile's
// stored deployment beats the BLOCKS_BACKEND_URL env var when no instance is given.
func resolveLoginBackend(instanceURL string) string {
	if instanceURL != "" {
		return instanceURL
	}
	if _, p, err := profiles.Active(); err == nil && p.BaseURL != "" {
		return p.BaseURL
	}
	return resolveBackendURL()
}

// applyBranding sets the global product name from discovery metadata, if present.
func applyBranding(disco *cliconfig.Config) {
	if disco != nil && disco.ProductName != "" {
		branding.Set(disco.ProductName)
	}
}

// resolveLoginClientID resolves the OAuth client id, preferring the deployment's
// discovered id over the env/CDM default.
func resolveLoginClientID(disco *cliconfig.Config) string {
	clientID := resolveClientID()
	if disco != nil && disco.OAuthClientID != "" {
		clientID = disco.OAuthClientID
	}
	return clientID
}

// resolveProfileName chooses where to store the login: an explicit
// --profile / BLOCKS_PROFILE selection names it; otherwise a host slug for an
// explicit instance, else the active profile (default blocks-network).
func resolveProfileName(instanceURL string) string {
	if sel := profiles.SelectedName(); sel != "" {
		return sel
	}
	if instanceURL != "" {
		return hostSlug(instanceURL)
	}
	if name, _, err := profiles.Active(); err == nil {
		return name
	}
	return profiles.DefaultProfile
}

// mergeDiscovery folds the resolved instance URL and discovery metadata into the
// profile, preserving existing values when the new ones are empty. An explicit
// instance sets BaseURL; otherwise it is left untouched (DO NOT overwrite with the
// empty instanceURL) so re-login against the default profile keeps CDM resolution.
func mergeDiscovery(p *profiles.Profile, instanceURL string, disco *cliconfig.Config) {
	if instanceURL != "" {
		p.BaseURL = instanceURL
	}
	if disco == nil {
		return
	}
	p.Enterprise = disco.Enterprise
	if disco.ProductName != "" {
		p.ProductName = disco.ProductName
	}
	if disco.OAuthClientID != "" {
		p.OAuthClientID = disco.OAuthClientID
	}
	if disco.DashboardBaseURL != "" {
		p.DashboardBaseURL = disco.DashboardBaseURL
	}
}

// resolveOrgForKey determines the org for the minted/supplied key. The OAuth path
// returns org metadata directly; the --api-key / --stdin path does not, so it is
// resolved from the backend (publish-context returns {orgId, orgName}). A key with
// no resolvable org can't be cached (DefaultOrgKey can't surface it), so storing
// nothing while printing success would strand the user with an unusable login —
// fail loudly instead. (The browser/OAuth path always resolves an org, so this
// only guards the --api-key / --api-key-stdin path when publish-context lookup
// fails — e.g. an invalid key or wrong URL.)
func resolveOrgForKey(backendURL, apiKey string, minted *auth.Credentials) (string, string, error) {
	orgId, orgName := minted.OrgId, minted.OrgName
	if orgId == "" && apiKey != "" && backendURL != "" {
		if pc := registry.FetchPublishContext(backendURL, apiKey); pc != nil {
			orgId, orgName = pc.OrgID, pc.OrgName
		}
	}
	if orgId == "" && apiKey != "" {
		target := backendURL
		if target == "" {
			target = "the target instance"
		}
		return "", "", fmt.Errorf("could not determine the organization for this API key from %s — the key was not stored; verify the key is valid and the instance URL is correct, then retry", target)
	}
	return orgId, orgName, nil
}

// maybeWriteEnv injects BLOCKS_API_KEY into the project .env when shouldWriteEnv
// allows it. A write failure is fatal only when explicitly requested via
// --write-env; otherwise it degrades to a warning so the login still succeeds.
func maybeWriteEnv(apiKey string) error {
	if !shouldWriteEnv() {
		return nil
	}
	envDir := loginDir
	if envDir == "" {
		envDir = mustCwd()
	}
	if err := auth.InjectEnvAt(envDir, "BLOCKS_API_KEY", apiKey); err != nil {
		if loginWriteEnv {
			return fmt.Errorf("failed to write .env: %w", err)
		}
		fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
	}
	return nil
}

// hostSlug turns https://blocks.acme.com into "blocks.acme.com". It falls back
// to the raw URL when it cannot be parsed into a host.
func hostSlug(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

// shouldWriteEnv decides whether to inject BLOCKS_API_KEY into the project .env.
// Order of precedence:
//  1. --no-write-env flag set -> no, unconditionally (skip the prompt too).
//  2. --write-env flag set -> yes, unconditionally.
//  3. --api-key or --api-key-stdin provided (implies automation) -> no, unless --write-env.
//  4. Non-interactive stdin (piped / CI) -> no, keep login side-effect-free.
//  5. Interactive terminal -> prompt the user (default yes).
func shouldWriteEnv() bool {
	if loginNoWriteEnv {
		return false
	}
	if loginWriteEnv {
		return true
	}
	if loginApiKey != "" || loginApiKeyStdin {
		return false
	}
	if !isInteractive() {
		return false
	}
	fmt.Print("  Write BLOCKS_API_KEY to project .env? (Y/n): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return true
	}
	ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if ans == "" {
		return true
	}
	return ans != "n" && ans != "no"
}
