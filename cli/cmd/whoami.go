package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
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

	name, p, err := profiles.Active()
	if err != nil {
		return fmt.Errorf("not logged in — run 'blocks login' first")
	}
	k, ok := p.DefaultOrgKey()
	if !ok {
		return fmt.Errorf("not logged in — run 'blocks login' first")
	}

	if jsonOutput {
		output := map[string]interface{}{
			"profile":  name,
			"org_name": k.OrgName,
			"org_id":   p.DefaultOrgID,
			"key_id":   k.KeyId,
		}
		if !k.ExpiresAt.IsZero() {
			output["expires_at"] = k.ExpiresAt.UTC().Format(time.RFC3339)
			daysRemaining := int(time.Until(k.ExpiresAt).Hours() / 24)
			output["days_remaining"] = daysRemaining
			output["expired"] = k.IsExpired()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output
	fmt.Printf("  Profile:  %s\n", name)
	fmt.Printf("  Org:      %s\n", k.OrgName)
	fmt.Printf("  Org ID:   %s\n", p.DefaultOrgID)
	if k.KeyId != "" {
		fmt.Printf("  Key ID:   %s\n", k.KeyId)
	}

	if !k.ExpiresAt.IsZero() {
		expiresAt := k.ExpiresAt.UTC()
		expiryStr := expiresAt.Format(time.RFC3339)
		if k.IsExpired() {
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
