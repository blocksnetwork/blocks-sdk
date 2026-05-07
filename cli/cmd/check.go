package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pubnub/blocks-sdk/cli/internal/schema"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(checkCmd)
}

var checkCmd = &cobra.Command{
	Use:   "check [path]",
	Short: "Validate agent-card.json and handler",
	Long:  "Validate the agent-card.json file against the Blocks schema and verify the handler file exists.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cardPath := "agent-card.json"
		if len(args) > 0 {
			cardPath = args[0]
		}

		if !filepath.IsAbs(cardPath) {
			cardPath = filepath.Join(mustCwd(), cardPath)
		}

		result := schema.Validate(cardPath)

		for _, msg := range result.Successes {
			fmt.Printf("[OK] %s\n", msg)
		}
		for _, msg := range result.Errors {
			fmt.Fprintf(os.Stderr, "[FAIL] %s\n", msg)
		}

		fmt.Println()
		if len(result.Errors) == 0 {
			fmt.Println("All checks passed.")
			return nil
		}

		return fmt.Errorf("%d check(s) failed", len(result.Errors))
	},
}
