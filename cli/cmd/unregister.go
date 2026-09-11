package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/spf13/cobra"
)

var unregisterApiKey string
var unregisterApiKeyStdin bool
var unregisterYes bool

func init() {
	rootCmd.AddCommand(unregisterCmd)
	unregisterCmd.Flags().StringVar(&unregisterApiKey, "api-key", "", "Use a pre-obtained API key")
	unregisterCmd.Flags().BoolVar(&unregisterApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	unregisterCmd.Flags().BoolVar(&unregisterYes, "yes", false, "Skip the confirmation prompt")
}

var unregisterCmd = &cobra.Command{
	Use:   "unregister [agentName]",
	Short: "Remove an agent from the active deployment",
	Long: "Remove an agent from the deployment you are currently targeting — the\n" +
		"inverse of 'blocks register'.\n\n" +
		"With no argument the agent name is read from identity.agentName in\n" +
		"agent-card.json in the current directory. Pass a name explicitly to remove\n" +
		"an agent from anywhere. The deployment is shown before the removal and the\n" +
		"confirmation names the agent; check both if you registered somewhere\n" +
		"unintended.\n\n" +
		"This cannot be undone — the agent must be registered again to restore it.\n" +
		"Requires prior authentication via 'blocks login' or --api-key.\n\n" +
		"Non-interactive use (CI, scripts) requires --yes to confirm the removal.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runUnregister(ctx, args)
	},
}

func runUnregister(ctx context.Context, args []string) error {
	agentName := ""
	if len(args) > 0 {
		agentName = strings.TrimSpace(args[0])
	}
	if agentName == "" {
		name, err := readAgentNameFromCard(filepath.Join(mustCwd(), "agent-card.json"))
		if err != nil {
			return fmt.Errorf("%w — pass the name explicitly: blocks unregister <agentName>", err)
		}
		agentName = name
	}

	apiKey, err := resolvePublishApiKey()
	if err != nil {
		return err
	}
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return backendNotConfigured("BLOCKS_BACKEND_URL must be set — run 'blocks login' first")
	}

	// The organization the key belongs to does not scope this removal: the deployment
	// authorizes it by this caller's rights over this agent, so a multi-organization
	// user can delete an agent owned by an organization other than the one the key was
	// minted for. Naming that organization here would describe the credential while
	// reading as a promise about what can be reached, so the banner states the
	// deployment only. The subject is left to the confirmation prompt below, which
	// already asks about the agent by name — repeating it here would only pad the line
	// above the one the operator actually answers.
	clictx.PrintBanner(clictx.OrgIsNotTheScope())
	if !unregisterYes && !isTTY() {
		return fmt.Errorf("refusing to remove %s without confirmation — pass --yes to confirm in a non-interactive session", shownName(agentName))
	}
	if err := confirmUnregister(agentName); err != nil {
		if errors.Is(err, errUnregisterCancelled) {
			fmt.Println("Cancelled — nothing was removed.")
			return nil
		}
		return err
	}

	client := blocksapi.NewClient(backendURL, apiKey)
	var resp agentDeleteResponse
	if err := client.DoJSON(ctx, "DELETE", "/api/v1/registry/agents?agentName="+url.QueryEscape(agentName), nil, &resp); err != nil {
		return fmt.Errorf("could not remove %s from %s: %w", shownName(agentName), clictx.TargetName(), err)
	}
	if err := resp.confirms(agentName); err != nil {
		return fmt.Errorf("could not confirm %s was removed from %s: %w", shownName(agentName), clictx.TargetName(), err)
	}

	fmt.Printf("✓ %s removed from %s\n", shownName(agentName), clictx.TargetName())
	return nil
}

// agentDeleteResponse is the body of DELETE /api/v1/registry/agents. agentName and
// status are required by the response contract and status is only ever "deleted"; ts is
// declared but optional, so a response omitting it is still valid.
type agentDeleteResponse struct {
	AgentName string `json:"agentName"`
	Status    string `json:"status"`
	TS        int64  `json:"ts"`
}

// confirms reports whether the deployment actually said it removed the agent this
// command asked about.
//
// The command used to decode into a map it never read, so every 2xx printed success —
// including a 2xx that is not this endpoint's answer at all. A proxy or captive portal
// returning 200 with an HTML or empty body, or a deployment reporting a different
// agent, all read as "removed". For a destructive command whose whole value is telling
// the operator what happened, an unconfirmed success is worse than an error: the agent
// may still be there and nothing said so.
//
// Agent names are case-sensitive (they are embedded in channel names), so the echo is
// compared exactly rather than folded.
func (r agentDeleteResponse) confirms(agentName string) error {
	if r.Status != "deleted" {
		return fmt.Errorf("the deployment reported status %q rather than \"deleted\"", termsafe.Text(r.Status))
	}
	if r.AgentName != agentName {
		return fmt.Errorf("the deployment reported agent %q rather than the one asked about", termsafe.Text(r.AgentName))
	}
	return nil
}

// shownName renders an agent name for the terminal. The name reaching this command
// is deliberately unvalidated — it comes from an argument or straight out of a
// possibly mid-edit agent card, because unregister has to work on exactly the card a
// user wants to undo — so it is the one input here that can carry an escape sequence
// into the confirmation prompt this command prints to be checked. The request itself
// keeps using the raw name: only the rendering is sanitized.
func shownName(agentName string) string { return termsafe.Text(agentName) }

// errUnregisterCancelled reports that the user declined the removal, which is a
// successful outcome for the command rather than a failure. It is a sentinel
// because runUnregister has to tell a decline from a genuine error to decide the
// exit status of a destructive command, and matching on the message text would
// turn any rewording of it into a non-zero exit.
var errUnregisterCancelled = errors.New("cancelled")

// confirmUnregister asks before an irreversible removal. --yes skips the prompt.
// This is only called after non-interactive sessions have been handled.
// Returns nil when proceeding, errUnregisterCancelled when the user declines,
// or an error naming --yes when unable to read input.
func confirmUnregister(agentName string) error {
	if unregisterYes {
		return nil
	}
	fmt.Printf("Remove %s from %s?\n", shownName(agentName), clictx.TargetName())
	fmt.Print("This cannot be undone. (y/N): ")
	line, ok, noInputRequested := readStdinLine()
	if noInputRequested {
		return fmt.Errorf("cannot ask for confirmation with --no-input — pass --yes")
	}
	if !ok {
		return fmt.Errorf("refusing to remove %s without confirmation — pass --yes to confirm in a non-interactive session", shownName(agentName))
	}
	ans := strings.ToLower(line)
	if ans == "y" || ans == "yes" {
		return nil
	}
	return errUnregisterCancelled
}

// readAgentNameFromCard pulls identity.agentName out of an agent card without
// validating it against the schema. Unregister must work on a card that is
// mid-edit or invalid — that state often coincides with wanting to undo a
// registration — so full validation would block the very recovery the user needs.
func readAgentNameFromCard(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("could not read %s", path)
	}
	var card struct {
		Identity struct {
			AgentName string `json:"agentName"`
		} `json:"identity"`
	}
	if err := json.Unmarshal(data, &card); err != nil {
		return "", fmt.Errorf("could not parse %s", path)
	}
	if card.Identity.AgentName == "" {
		return "", fmt.Errorf("%s has no identity.agentName", path)
	}
	return card.Identity.AgentName, nil
}
