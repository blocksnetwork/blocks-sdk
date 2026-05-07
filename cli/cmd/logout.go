package cmd

import (
	"fmt"
	"os"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored Blocks credentials",
	RunE:  runLogout,
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

func runLogout(cmd *cobra.Command, args []string) error {
	// Delete credential file (does not revoke the API key on the server)
	if err := auth.Delete(); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Warning: could not delete credentials: %v\n", err)
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

