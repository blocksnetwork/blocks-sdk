package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var logoutProvider string

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored Blocks credentials",
	Long: `Remove stored credentials for the active profile. Clears only the active
profile's cached org keys; its deployment target and branding are preserved so
'blocks login' (no URL) resolves the same instance afterward. Other profiles
are left untouched. Does not revoke the API key on the server. Use 'blocks
profile remove' to forget the deployment entirely.`,
	RunE: runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
	logoutCmd.Flags().StringVar(&logoutProvider, "provider", "blocks", "Provider to log out of: blocks|cloudflare|vercel|netlify")
}

func runLogout(cmd *cobra.Command, args []string) error {
	switch logoutProvider {
	case "blocks", "":
		return runBlocksLogout()
	case "cloudflare", "vercel", "netlify":
		return runPartnerLogout(logoutProvider)
	default:
		return fmt.Errorf("unknown provider %q — must be one of: blocks, cloudflare, vercel, netlify", logoutProvider)
	}
}

// runPartnerLogout removes the named provider's credential from the credentials
// file and prints a confirmation message.
func runPartnerLogout(provider string) error {
	path, err := auth.CredentialPathFunc()
	if err != nil {
		return fmt.Errorf("resolve credentials path: %w", err)
	}
	if err := auth.DeleteProviderCredential(path, provider); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logout %s: %w", provider, err)
	}
	providerName := strings.ToUpper(provider[:1]) + provider[1:]
	fmt.Printf("Logged out of %s\n", providerName)
	return nil
}

// runBlocksLogout clears the active profile's cached org keys (preserving its
// deployment target and branding) and removes the "blocks" credential namespace
// so partner credentials are preserved. Does not revoke the API key on the server.
func runBlocksLogout() error {
	// Clear the active profile's cached org keys first. This is the PRIMARY
	// authenticating credential (loadCredentials reads it before the legacy
	// credentials.json fallback), so a failure here means the user is NOT
	// actually logged out — we must report it rather than print "Logged out.".
	clearErr := clearActiveProfileKeys()

	// Remove only the "blocks" namespace so partner credentials are preserved.
	// Attempted even when the profile clear failed, so everything reachable is
	// scrubbed — but its outcome is reported rather than warned about.
	//
	// The legacy file is a credential tier of its own (clictx's SourceStore, the last
	// one it consults), so a surviving `blocks` entry there keeps the very next command
	// authenticated. That makes it a failed logout for exactly the reason a surviving
	// profile key or `.env` key is, and it joins the error below on the same footing.
	// It used to warn on a delete failure and skip silently when the path could not be
	// resolved, so `logout` could print "Logged out." over a credential still on disk
	// and still usable. A file that simply is not there is not a failure: most
	// installations have no legacy store at all.
	legacyErr := clearLegacyBlocksCredential()

	// Remove BLOCKS_API_KEY from the project .env. A .env this command could not read
	// or could not rewrite still authenticates every later command in this directory,
	// so it is a failed logout for the same reason a surviving profile key is: the
	// credential is still on disk and still usable.
	envErr := removeEnvApiKey(".env")

	if err := errors.Join(clearErr, legacyErr, envErr); err != nil {
		// The profile is named through the resolved context so the suggestion
		// names the same profile every other command would act on (--profile and
		// BLOCKS_PROFILE included), not just the store's saved active name.
		return logoutIncompleteError(clictx.Profile(), err)
	}

	printLogoutSummary()
	return nil
}

// clearLegacyBlocksCredential removes the "blocks" namespace from the legacy credential
// file, leaving partner namespaces alone. An absent file is success; anything else is
// reported, because the caller cannot tell a swept store from an unreadable one and the
// difference decides whether they are still logged in.
func clearLegacyBlocksCredential() error {
	path, err := auth.CredentialPathFunc()
	if err != nil {
		return fmt.Errorf("could not locate the legacy credential store: %w", err)
	}
	if err := auth.DeleteProviderCredential(path, "blocks"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not remove Blocks credentials from the legacy store: %w", err)
	}
	return nil
}

// printLogoutSummary reports what logout removed AND what it kept. The
// deployment target and branding survive on purpose (see
// clearActiveProfileKeys) so a later bare `blocks login` returns to the same
// instance — but saying nothing about that left users believing enterprise
// branding after logout was a bug.
//
// This is the one place that names the active profile's product rather than the
// deployment the invocation would otherwise reach: logout sends no request and
// clears the credentials of that profile, so the profile — and its brand — is
// exactly what the sentence is about. An ambient backend URL redirects requests,
// not the store this command edits.
func printLogoutSummary() {
	fmt.Printf("Logged out of %s.\n", branding.ProductName())
	name := clictx.Profile()
	if name == "" || name == profiles.DefaultProfile {
		return
	}
	// The profile name is reported on its own line and never inside a command. It
	// reaches the store from the host of whatever deployment a login reached, and that
	// host can come from a project .env or from a deployment's own discovery response,
	// so a line formatted as something to run must not be built from it: termsafe.Text
	// makes the name safe to display and says nothing about whether it is safe to run,
	// because `;`, `&&`, backticks and $(...) pass through it as the ordinary printable
	// characters they are. So the remedy names the command and the user supplies the
	// name they just read.
	fmt.Printf("  Deployment kept (profile: %s) — logging in again without a URL returns here.\n", termsafe.Text(name))
	fmt.Printf("  To forget it, run: blocks profile remove <profile>  # the profile named above\n")
}

// clearActiveProfileKeys removes the cached org keys and default-org pointer from
// the selected profile (--profile → BLOCKS_PROFILE → active), preserving its
// deployment target and branding (base_url/enterprise/product_name/dashboard) so
// `blocks login` (no URL) still resolves the same instance afterward. It returns
// an error if the store cannot be read or the cleared state cannot be persisted —
// callers MUST treat that as a failed logout, since the org key remains usable.
func clearActiveProfileKeys() error {
	c, err := profiles.Load()
	if err != nil {
		return fmt.Errorf("read credentials store: %w", err)
	}
	// Resolve the target the same way every other command does. Using c.Active
	// directly would ignore an explicit --profile flag and clear the wrong profile.
	name := profiles.SelectedName()
	if name == "" {
		name = c.Active
	}
	p, ok := c.Profiles[name]
	if !ok {
		return nil // no such profile — nothing cached to clear
	}
	if len(p.Orgs) == 0 && p.DefaultOrgID == "" {
		return nil // already clear — no write (and no failure surface) needed
	}
	p.Orgs = map[string]profiles.OrgKey{}
	p.DefaultOrgID = ""
	c.Profiles[name] = p
	if err := profiles.Save(c); err != nil {
		return fmt.Errorf("write credentials store: %w", err)
	}
	return nil
}

// removeEnvApiKey removes the BLOCKS_API_KEY line from the given .env file. Its
// failure is returned rather than warned about: the logout summary says the key is
// gone, and a logout that cannot prove it removed the key must not claim it did. No
// .env at all is not a failure — most projects have none — but a .env that exists and
// resisted reading or rewriting leaves the credential exactly where it was.
//
// The credential is all that goes. The deployment target login wrote beside it —
// BLOCKS_BACKEND_URL and the BLOCKS_CDM_URL that belongs with it — is kept on
// purpose, matching the profile's own surviving deployment, so a later bare
// `blocks login` returns to the same instance. `blocks profile remove` is what
// forgets a deployment: it judges the two pins independently and, when the backend
// pin was one of the matches, drops BLOCKS_API_KEY with them — because the key
// records where it is spent, not where it was minted.
func removeEnvApiKey(path string) error {
	if err := auth.RemoveEnvKey(path, blocksAPIKeyEnv); err != nil {
		return fmt.Errorf("could not remove %s from %s: %w", blocksAPIKeyEnv, displayEnvPath(path), err)
	}
	return nil
}
