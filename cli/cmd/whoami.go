package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Display the current authenticated identity",
	RunE:  runWhoami,
}

func init() {
	rootCmd.AddCommand(whoamiCmd)
	whoamiCmd.Flags().Bool("json", false, "Output structured JSON")
}

func runWhoami(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")

	creds, err := auth.Load()
	if err != nil {
		return fmt.Errorf("not logged in — run 'blocks login' or 'blocks publish' first")
	}

	if jsonOutput {
		output := map[string]interface{}{
			"org_name": creds.OrgName,
			"org_id":   creds.OrgId,
			"key_id":   creds.KeyId,
		}
		if !creds.ExpiresAt.IsZero() {
			output["expires_at"] = creds.ExpiresAt.UTC().Format(time.RFC3339)
			daysRemaining := int(time.Until(creds.ExpiresAt).Hours() / 24)
			output["days_remaining"] = daysRemaining
			output["expired"] = creds.IsExpired()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	fmt.Printf("  Org:      %s\n", creds.OrgName)
	fmt.Printf("  Org ID:   %s\n", creds.OrgId)
	if creds.KeyId != "" {
		fmt.Printf("  Key ID:   %s\n", creds.KeyId)
	}

	if !creds.ExpiresAt.IsZero() {
		expiresAt := creds.ExpiresAt.UTC()
		expiryStr := expiresAt.Format(time.RFC3339)
		if creds.IsExpired() {
			expiryStr += " (expired)"
		} else {
			daysRemaining := int(time.Until(expiresAt).Hours() / 24)
			if daysRemaining > 0 {
				expiryStr += fmt.Sprintf(" (%dd remaining)", daysRemaining)
			} else {
				hoursRemaining := int(time.Until(expiresAt).Hours())
				expiryStr += fmt.Sprintf(" (%dh remaining)", hoursRemaining)
			}
		}
		fmt.Printf("  Expires:  %s\n", expiryStr)
	} else {
		fmt.Printf("  Expires:  never\n")
	}

	return nil
}
