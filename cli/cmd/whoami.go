package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
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
		return whoamiNotLoggedIn()
	}
	k, ok := p.DefaultOrgKey()
	if !ok {
		return whoamiNotLoggedIn()
	}
	override := overridingCredential()

	if jsonOutput {
		output := map[string]interface{}{
			"profile":  name,
			"org_name": k.OrgName,
			"org_id":   p.DefaultOrgID,
			"key_id":   k.KeyId,
		}
		// Where the profile's key lives, and — when something outranks it — which key
		// commands actually send instead. The path is absolute, not the ~/… form the
		// human output shows: a script cannot expand a tilde.
		if path, err := profileStorePath(); err == nil {
			output["credentials_path"] = path
		}
		if override != "" {
			output["key_override"] = override
		}
		if !k.ExpiresAt.IsZero() {
			output["expires_at"] = k.ExpiresAt.UTC().Format(time.RFC3339)
			daysRemaining := int(time.Until(k.ExpiresAt).Hours() / 24)
			output["days_remaining"] = daysRemaining
			output["expired"] = k.IsExpired()
		}
		// A script reading this output has the same blind spot a person does: the
		// profile named above is not where commands go while an override is in
		// force. The key is present only then, so existing consumers see no change.
		if backendOverrideNote() != "" {
			output["backend_url_override"] = clictx.ChosenBackendURL()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Human-readable output. The organization name is whatever the deployment called
	// it — cached locally by login, but authored remotely — so it is rendered inert:
	// these four lines are what a user reads to check which identity later commands
	// will act as, and an erase-line or cursor-up sequence in the name can overwrite
	// the ones above it. The JSON branch needs no equivalent: the encoder escapes
	// control characters itself.
	fmt.Printf("  Profile:  %s\n", name)
	fmt.Printf("  Org:      %s\n", termsafe.Text(k.OrgName))
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
	fmt.Printf("  Stored:   %s\n", profileStoreDisplayPath())
	if override != "" {
		fmt.Printf("  Note: commands run here use %s, not this profile's key.\n", override)
	}

	// Everything above describes the stored profile. When an override displaces it,
	// the identity reported is not the one requests will be made as, and this is
	// one of the two places a user goes looking for that.
	fmt.Print(backendOverrideNoteLine("  "))

	return nil
}

// overridingCredential describes a key supplied for this invocation that outranks the
// active profile's (BLOCKS_API_KEY in .env or the environment, --api-key), or "" when
// commands use the profile's own key.
func overridingCredential() string {
	c := clictx.EffectiveCredential()
	if !c.Source.Supplied() {
		return ""
	}
	return credentialSourceDescription(c)
}

// whoamiNotLoggedIn is whoami's "no stored key" failure. When a key from .env or the
// environment is in effect anyway, it says so: the user is not logged in, yet commands
// run here are authenticated.
func whoamiNotLoggedIn() error {
	err := notLoggedInError()
	if override := overridingCredential(); override != "" {
		return fmt.Errorf("%w\n  Commands run here still authenticate with %s.", err, override)
	}
	return err
}
