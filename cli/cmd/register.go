package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var registerApiKey string
var registerApiKeyStdin bool
var registerOrgName string

func init() {
	rootCmd.AddCommand(registerCmd)
	registerCmd.Flags().StringVar(&registerApiKey, "api-key", "", "Use a pre-obtained API key")
	registerCmd.Flags().BoolVar(&registerApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	registerCmd.Flags().StringVar(&registerOrgName, "org-name", "", "Set organization name (prompted on first publish)")
}

var registerCmd = &cobra.Command{
	Use:   "register [path]",
	Short: "Register an agent privately and free on the Blocks Network",
	Long: "Register an agent so it can be used by you and the organizations you invite.\n\n" +
		"This is the recommended first step. The agent is published as private (only\n" +
		"organizations you invite can discover or use it) and free (no charge). There\n" +
		"is no public or paid option here — register and test privately first, then run\n" +
		"'blocks publish' when you're ready to make the agent public or set pricing.\n\n" +
		"Requires prior authentication via 'blocks login' or --api-key.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runRegister(ctx, cmd, args)
	},
}

// runRegister publishes an agent as free + private. It reuses the same
// validation, envelope, org-name, and submit pipeline as `blocks publish`;
// the only difference is that listing and billing are fixed (no prompts, no
// public/paid flags) so the suggested first-publish flow cannot reach the
// public or paid paths.
func runRegister(ctx context.Context, cmd *cobra.Command, args []string) error {
	prep, err := preparePublish(args)
	if err != nil {
		return err
	}

	// --no-input counts as non-interactive here for the same reason it does in
	// `publish`: the caller asked not to be prompted, TTY or not.
	interactive := interactiveSession()
	if interactive && !prep.enterprise {
		printRegisterIntro(prep.agentName)
	}

	if err := applyEnterpriseOrgPicker(prep, interactive); err != nil {
		return err
	}

	// After the organization picker, for the reason given in `publish`: the picker
	// is the last input that can change which organization this registration acts
	// as, and a banner naming the wrong one is worse than none. The deployment half
	// of the target is settled earlier and the picker states it before it asks, so
	// nothing here is the user's first sight of where the command is pointed.
	clictx.PrintBanner()

	org, err := resolveOrgNameInput(cmd, prep, interactive, "org-name", registerOrgName)
	if err != nil {
		return err
	}

	// Fixed promotion params: private + free, no pricing, no T&C. This is the
	// entire behavioral difference from `blocks publish`.
	promInput := registry.PromotionInput{Listing: "private", BillingMode: "free"}

	return finalizePublish(ctx, prep, promInput, org, interactive, submitOptions{
		commandName: "blocks register",
		promoteHint: true,
	})
}

func printRegisterIntro(agentName string) {
	fmt.Println(blocksWordmark)
	fmt.Println()
	fmt.Println("Register an Agent")
	if agentName != "" {
		fmt.Println()
		fmt.Printf("Agent: %s\n", termsafe.Text(agentName))
	}
	fmt.Println()
	fmt.Println("Your agent will be private (only organizations you invite can use it) and free.")
}
