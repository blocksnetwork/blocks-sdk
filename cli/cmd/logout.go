package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/spf13/cobra"
)

var logoutProvider string

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored Blocks credentials",
	RunE:  runLogout,
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

// runBlocksLogout is the existing Blocks logout path, unchanged.
func runBlocksLogout() error {
	// Remove only the "blocks" namespace so partner credentials are preserved.
	if path, err := auth.CredentialPathFunc(); err == nil {
		if err := auth.DeleteProviderCredential(path, "blocks"); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Warning: could not remove Blocks credentials: %v\n", err)
		}
	}

	// Remove BLOCKS_API_KEY from .env if it exists
	removeEnvApiKey(".env")

	fmt.Println("Logged out.")
	return nil
}

// removeEnvApiKey removes the BLOCKS_API_KEY line from the given .env file.
func removeEnvApiKey(path string) {
	auth.RemoveEnvKey(path, "BLOCKS_API_KEY")
}
