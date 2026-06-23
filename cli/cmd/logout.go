package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

var logoutProvider string

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored Blocks credentials",
	Long: `Remove stored credentials for the active profile. Clears only the active
profile's cached org keys; its deployment target and branding are preserved so
'blocks login' (no URL) resolves the same instance afterward. Other profiles
are left untouched. Does not revoke the API key on the server.`,
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
	// Best-effort even when the profile clear failed, so the legacy fallback and
	// .env are scrubbed where possible.
	if path, err := auth.CredentialPathFunc(); err == nil {
		if err := auth.DeleteProviderCredential(path, "blocks"); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: could not remove Blocks credentials: %v\n", err)
		}
	}

	// Remove BLOCKS_API_KEY from .env if it exists
	removeEnvApiKey(".env")

	if clearErr != nil {
		return fmt.Errorf("logout incomplete — Blocks credentials remain on disk: %w", clearErr)
	}

	fmt.Println("Logged out.")
	return nil
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

// removeEnvApiKey removes the BLOCKS_API_KEY line from the given .env file.
func removeEnvApiKey(path string) {
	auth.RemoveEnvKey(path, "BLOCKS_API_KEY")
}
