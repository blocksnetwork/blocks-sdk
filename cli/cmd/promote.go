package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/spf13/cobra"
)

var promoteListing string
var promotePrice string
var promotePricePerTask string
var promotePricePerMinute string
var promoteFreeUnits int
var promoteFreeTasks int
var promoteFreeMinutes int
var promoteAcceptTerms bool
var promoteApiKey string
var promoteApiKeyStdin bool

func init() {
	rootCmd.AddCommand(promoteCmd)
	promoteCmd.Flags().StringVar(&promoteListing, "listing", "", "Target listing: public or private")
	promoteCmd.Flags().StringVar(&promotePrice, "price", "", "Price in USD, decimal string")
	promoteCmd.Flags().StringVar(&promotePricePerTask, "price-per-task", "", "Per-task price in USD, decimal string")
	promoteCmd.Flags().StringVar(&promotePricePerMinute, "price-per-minute", "", "Per-minute price in USD, decimal string")
	promoteCmd.Flags().IntVar(&promoteFreeUnits, "free-units", 0, "Free tasks or minutes")
	promoteCmd.Flags().IntVar(&promoteFreeTasks, "free-tasks", 0, "Free tasks")
	promoteCmd.Flags().IntVar(&promoteFreeMinutes, "free-minutes", 0, "Free minutes")
	promoteCmd.Flags().BoolVar(&promoteAcceptTerms, "accept-terms", false, "Accept legal attestations non-interactively")
	promoteCmd.Flags().StringVar(&promoteApiKey, "api-key", "", "Use a pre-obtained API key")
	promoteCmd.Flags().BoolVar(&promoteApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
}

var promoteCmd = &cobra.Command{
	Use:   "promote [agent-name]",
	Short: "Promote a playground agent to public or private",
	Long:  "Promotes an existing playground agent to the Blocks Network with public or private listing.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runPromote(ctx, cmd, args)
	},
}

func runPromote(ctx context.Context, cmd *cobra.Command, args []string) error {
	backendURL := resolveBackendURL()
	clientID := resolveClientID()

	apiKey, err := auth.EnsureCredentials(ctx, backendURL, clientID, promoteApiKey, promoteApiKeyStdin)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	var selected *registry.Agent

	if len(args) > 0 {
		// Agent name provided — fetch and verify it's playground
		// (ownership is enforced by the backend PATCH call, which returns 403 if not owned)
		agent, err := registry.FetchAgent(ctx, backendURL, apiKey, args[0])
		if err != nil {
			return err
		}
		if agent.Listing != "playground" {
			return fmt.Errorf("agent %q is already %s — only playground agents can be promoted", args[0], agent.Listing)
		}
		selected = agent
	} else if promoteAcceptTerms {
		// Non-interactive mode: interactive agent selection is not valid.
		// Fail fast instead of listing and blocking on stdin.
		return fmt.Errorf("agent name is required in non-interactive mode (--accept-terms)")
	} else {
		// List playground agents and let user pick
		agents, err := registry.FetchPlaygroundAgents(ctx, backendURL, apiKey)
		if err != nil {
			return err
		}
		if len(agents) == 0 {
			return fmt.Errorf("no playground agents to promote")
		}

		fmt.Println("\nYour playground agents:")
		for i, a := range agents {
			name := a.DisplayName
			if name == "" {
				name = a.AgentName
			}
			fmt.Printf("  %d. %-20s %s\n", i+1, a.AgentName, name)
		}

		fmt.Print("Select agent: ")
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		choice, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || choice < 1 || choice > len(agents) {
			return fmt.Errorf("invalid selection — enter a number between 1 and %d", len(agents))
		}
		picked := agents[choice-1]
		selected = &picked
	}

	// Build flags
	flags := registry.PromotionFlags{AcceptTerms: promoteAcceptTerms}
	if promoteListing != "" {
		flags.Listing = &promoteListing
	}
	if cmd.Flags().Changed("price") {
		if promotePrice == "" {
			return fmt.Errorf("--price requires a non-empty decimal value")
		}
		flags.Price = &promotePrice
	}
	if cmd.Flags().Changed("price-per-task") {
		if promotePricePerTask == "" {
			return fmt.Errorf("--price-per-task requires a non-empty decimal value")
		}
		flags.PricePerTask = &promotePricePerTask
	}
	if cmd.Flags().Changed("price-per-minute") {
		if promotePricePerMinute == "" {
			return fmt.Errorf("--price-per-minute requires a non-empty decimal value")
		}
		flags.PricePerMinute = &promotePricePerMinute
	}
	if cmd.Flags().Changed("free-units") {
		flags.FreeUnits = &promoteFreeUnits
	}
	if cmd.Flags().Changed("free-tasks") {
		flags.FreeTasks = &promoteFreeTasks
	}
	if cmd.Flags().Changed("free-minutes") {
		flags.FreeMinutes = &promoteFreeMinutes
	}

	promInput, err := registry.CollectPromotionInput(selected.IsStreaming(), selected.IsRequest(), flags, scanner)
	if err != nil {
		return err
	}

	if err := registry.Promote(ctx, backendURL, apiKey, selected.AgentName, promInput); err != nil {
		return err
	}

	fmt.Printf("✓ Promoted %s to %s\n", selected.AgentName, promInput.Listing)
	return nil
}
