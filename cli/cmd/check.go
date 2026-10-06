package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(checkCmd)
}

var checkCmd = &cobra.Command{
	Use:   "check [path]",
	Short: "Validate agent-card.json and handler",
	Long: "Validate the agent-card.json file against the Blocks schema and verify the handler file exists.\n\n" +
		"When you are logged in, also compare the card with the version registered on the\n" +
		"deployment and warn about fields that differ: card changes reach callers only after\n" +
		"'blocks register' or 'blocks publish'. The comparison is skipped when offline, not\n" +
		"logged in, or before the agent is registered.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cardPath, cardArg := "agent-card.json", ""
		if len(args) > 0 {
			cardPath, cardArg = args[0], args[0]
		} else if err := callerProjectError(mustCwd(), "blocks check"); err != nil {
			return err
		}

		if !filepath.IsAbs(cardPath) {
			cardPath = filepath.Join(mustCwd(), cardPath)
		}

		// Run the legacy `skills` → `tags` shim before validation so a
		// stale card surfaces the deprecation warning instead of a
		// confusing schema rejection (parity with `publish` and `run`).
		result := validateCardWithLegacyShim(cardPath)

		for _, msg := range result.Successes {
			fmt.Printf("[OK] %s\n", msg)
		}
		for _, msg := range result.Errors {
			fmt.Fprintf(os.Stderr, "[FAIL] %s\n", msg)
		}

		if len(result.Errors) == 0 {
			drifted := checkRegistryDrift(cmd.Context(), result.Card, cardArg)
			fmt.Println()
			if drifted {
				fmt.Println("All checks passed with 1 warning.")
			} else {
				fmt.Println("All checks passed.")
			}
			return nil
		}

		fmt.Println()
		return fmt.Errorf("%d check(s) failed", len(result.Errors))
	},
}
