package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
	"github.com/pubnub/blocks-sdk/cli/internal/suggest"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	initYes           bool
	initLanguage      string
	initMode          string
	initType          string
	initAgents        []string
	initBlocksBaseURL string
)

// initAgentNameRe enforces the bare agent-name pattern (matches registry column).
var initAgentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func init() {
	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false, "Use defaults (non-interactive)")
	initCmd.Flags().StringVarP(&initLanguage, "language", "l", "", "Project language: node or python (default: python)")
	initCmd.Flags().StringVarP(&initMode, "mode", "m", "", "Project mode: provider (default), consumer, or webapp")
	initCmd.Flags().StringVarP(&initType, "type", "t", "", "Deprecated: use --mode")
	_ = initCmd.Flags().MarkDeprecated("type", "use --mode instead")
	initCmd.Flags().StringSliceVar(&initAgents, "agent", nil, "Bare agent name the page calls (repeatable; required with --mode webapp)")
	initCmd.Flags().StringVar(&initBlocksBaseURL, "blocks-base-url", "", "Override the Blocks asset base URL (default: https://app.blocks.ai)")

	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Scaffold a new agent, consumer, or webapp project",
	Long: `Create a new Blocks project.

Run 'blocks init' with no arguments in a terminal to launch the interactive
wizard: it first asks whether you're building an agent or a web app, then walks
you through the rest (for a web app, you search for and pick the agents the page
will call). Pass a name and/or flags to skip the wizard.

  --mode provider (default): an agent handler project with handler.{ts,py},
    trigger.{ts,py}, and agent-card.json. Deploy with 'blocks publish' and
    run with 'blocks run'.

  --mode consumer: a script that calls other agents via TaskClient. Produces
    index.ts (Node) or main.py (Python). Run directly (npm run start /
    python main.py) after setting BLOCKS_API_KEY in .env.

  --mode webapp --agent <name> [--agent <name2> ...]: scaffold a static page
    pre-wired with the Blocks embed-auth widget for the named agent(s). The
    generator fetches each agent's card from the registry and emits per-agent
    input/output/stream wiring code. --agent is repeatable or comma-separated.
    Webapp scaffolds require a positional [name] for the project directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var nameFromArgs string
		if len(args) > 0 {
			nameFromArgs = args[0]
		}

		// Deprecated --type alias: fold into --mode. --type predates the
		// webapp mode and only ever accepted provider/consumer, so reject
		// webapp here and refuse a conflicting --mode rather than silently
		// picking one.
		if initType != "" {
			if initType != "provider" && initType != "consumer" {
				return fmt.Errorf("unsupported type %q (use \"provider\" or \"consumer\"; for a web app use --mode webapp)", initType)
			}
			if initMode != "" && initMode != initType {
				return fmt.Errorf("--type and --mode conflict (%q vs %q); use --mode only", initType, initMode)
			}
			initMode = initType
		}

		// Webapp scaffold path (flag-driven, or interactive collection).
		if initMode == "webapp" {
			return runWebapp(cmd.Context(), nameFromArgs)
		}

		// Reject --agent on non-webapp modes (it has no meaning there).
		if len(initAgents) > 0 {
			return fmt.Errorf("--agent is only valid with --mode webapp")
		}

		if initMode != "" && initMode != "provider" && initMode != "consumer" {
			return fmt.Errorf("unsupported mode %q (use \"provider\", \"consumer\", or \"webapp\")", initMode)
		}

		// Determine if we should run non-interactively.
		nonInteractive := initYes || !isInteractive()

		// Interactive `blocks init` with no mode and no name: offer the
		// top-level choice between an agent project and a web app. A given
		// name or an explicit --mode keeps the historical agent behavior.
		if isTTY() && !initYes && initMode == "" && nameFromArgs == "" {
			kind, err := wizard.SelectProjectKind()
			if err != nil {
				return err
			}
			if kind == wizard.ProjectKindWebapp {
				return runWebappWizard(cmd.Context())
			}
			// Agent: fall through to the agent wizard below.
		}

		var cfg wizard.Config
		if nonInteractive {
			if nameFromArgs == "" {
				return fmt.Errorf("agent name is required in non-interactive mode\nUsage: blocks init <name> --yes")
			}
			if err := wizard.ValidateAgentName(nameFromArgs); err != nil {
				return err
			}
			cfg = wizard.DefaultConfig(nameFromArgs)
			if initLanguage == "node" || initLanguage == "python" {
				cfg.Language = initLanguage
			} else if initLanguage != "" {
				return fmt.Errorf("unsupported language %q (use \"node\" or \"python\")", initLanguage)
			}
			if initMode == "consumer" {
				cfg.Mode = "consumer"
				// DefaultConfig seeds Description with "<name> agent"; for
				// consumers the interactive wizard rewrites it to "<name>
				// consumer" (wizard.go), so mirror that here or the leak
				// shows up in the scaffolded pyproject.toml.
				cfg.Description = cfg.Name + " consumer"
			}
		} else {
			// Validate --language flag before starting the wizard
			if initLanguage != "" && initLanguage != "node" && initLanguage != "python" {
				return fmt.Errorf("unsupported language %q (use \"node\" or \"python\")", initLanguage)
			}
			var err error
			cfg, err = wizard.Run(nameFromArgs, initLanguage, initMode)
			if err != nil {
				return err
			}
		}

		dir := filepath.Join(mustCwd(), cfg.Name)
		if _, err := os.Stat(dir); err == nil {
			return fmt.Errorf("directory %q already exists", cfg.Name)
		}

		if !nonInteractive {
			fmt.Printf("\n  This will create ./%s/ with your %s project files.\n", cfg.Name, cfg.Language)
			fmt.Print("  Continue? (Y/n): ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if ans == "n" || ans == "no" {
					return fmt.Errorf("canceled")
				}
			}
		}

		if err := scaffold.Project(dir, cfg, nil); err != nil {
			return fmt.Errorf("scaffold failed: %w", err)
		}

		printNextSteps(cfg)
		return nil
	},
}

// maxAgentsPerWebapp caps how many agents a single webapp page may wire up.
// Mirrors internal/config.Validate and wizard.maxAgentsPerWebapp.
const maxAgentsPerWebapp = 25

// runWebapp handles the --mode webapp path. With one or more --agent flags it
// runs the flag-driven scaffold; without them it runs the interactive webapp
// wizard (TTY) or errors (non-interactive).
func runWebapp(ctx context.Context, nameFromArgs string) error {
	if initLanguage != "" {
		return fmt.Errorf("--language is not valid with --mode webapp (the webapp scaffold has no language axis)")
	}
	if initBlocksBaseURL != "" {
		if _, err := url.ParseRequestURI(initBlocksBaseURL); err != nil {
			return fmt.Errorf("--blocks-base-url %q is not a valid URL: %w", initBlocksBaseURL, err)
		}
	}

	if len(initAgents) == 0 {
		if initYes || !isTTY() {
			return fmt.Errorf("--mode webapp requires at least one --agent")
		}
		return runWebappWizard(ctx)
	}

	// Flag-driven path: validate every agent name before any network call.
	// These checks mirror the constraints that blocks.config.json validation
	// (internal/config/blocks_config.go) and the embed-auth widget
	// (signInAndGetClients) enforce downstream — failing here avoids a network
	// round-trip + half-scaffold for a known-bad list.
	if len(initAgents) > maxAgentsPerWebapp {
		return fmt.Errorf("--mode webapp supports at most %d agents per page; got %d", maxAgentsPerWebapp, len(initAgents))
	}
	seen := make(map[string]struct{}, len(initAgents))
	for _, name := range initAgents {
		if name == "" {
			return fmt.Errorf("--agent values must be non-empty")
		}
		if !initAgentNameRe.MatchString(name) {
			return fmt.Errorf("use the bare agent name (e.g. 'translator'), not the namespaced form (e.g. 'acme/translator'); got %q", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("--agent values must be unique; %q appears more than once", name)
		}
		seen[name] = struct{}{}
	}

	if nameFromArgs == "" {
		return fmt.Errorf("webapp scaffolds require a project name. Try: blocks init <name> --mode webapp --agent <agent>")
	}

	backendURL := resolveBackendURL()
	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}
	// Login is optional here: the registry fetch path supports anonymous public
	// reads, so a fresh machine can scaffold a page that only references public
	// agents. Use stored credentials when present (so private agents the
	// account can access resolve); otherwise fetch anonymously. A private agent
	// requested without credentials surfaces the not-found hint in
	// scaffoldWebappProject, which already points the user at 'blocks login'.
	apiKey := optionalCredentials()

	cfg := wizard.Config{
		Name:          nameFromArgs,
		Mode:          "webapp",
		Agents:        append([]string(nil), initAgents...),
		BlocksBaseURL: initBlocksBaseURL,
	}
	return scaffoldWebappProject(ctx, cfg, blocksapi.NewClient(backendURL, apiKey))
}

// runWebappWizard runs the interactive webapp wizard: it offers login (so
// private agents appear in suggestions), collects a project name + agent list
// via the type-ahead autocomplete, then scaffolds.
func runWebappWizard(ctx context.Context) error {
	apiKey := ensureOrOfferBlocksLogin(ctx)

	backendURL := resolveBackendURL()
	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}
	client := blocksapi.NewClient(backendURL, apiKey)

	cfg, err := wizard.RunWebapp(ctx, makeAgentSuggestFn(client))
	if err != nil {
		return err
	}
	cfg.BlocksBaseURL = initBlocksBaseURL
	return scaffoldWebappProject(ctx, cfg, client)
}

// scaffoldWebappProject fetches the agent cards named in cfg.Agents and writes
// the webapp scaffold. On any failure after the directory is created, the
// directory is removed so no partial scaffold remains. Shared by both the
// flag-driven and interactive webapp paths.
func scaffoldWebappProject(ctx context.Context, cfg wizard.Config, client *blocksapi.Client) error {
	dir := filepath.Join(mustCwd(), cfg.Name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists", cfg.Name)
	}

	// Fetch each card. Fail fast and surface a hint for not-found.
	cards := make([]*cardfetch.AgentCard, 0, len(cfg.Agents))
	for _, name := range cfg.Agents {
		card, err := cardfetch.Fetch(ctx, client, name)
		if err != nil {
			if errors.Is(err, cardfetch.ErrAgentNotFound) {
				return fmt.Errorf("agent %q not found — check spelling, or run 'blocks login' if it's a private agent your account can access", name)
			}
			return fmt.Errorf("failed to fetch agent card for %q: %w", name, err)
		}
		cards = append(cards, card)
	}

	// Pre-create the project directory so we have a single rollback target
	// if any subsequent step fails.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %q: %w", cfg.Name, err)
	}

	if err := scaffold.Project(dir, cfg, cards); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("scaffold failed: %w", err)
	}

	printWebappNextSteps(cfg, cfg.Name)
	return nil
}

// makeAgentSuggestFn adapts the suggest client to the wizard's SuggestFunc.
func makeAgentSuggestFn(client *blocksapi.Client) wizard.SuggestFunc {
	return func(ctx context.Context, q string) ([]wizard.Suggestion, error) {
		results, err := suggest.Agents(ctx, client, q)
		if err != nil {
			return nil, err
		}
		out := make([]wizard.Suggestion, 0, len(results))
		for _, r := range results {
			out = append(out, wizard.Suggestion{Value: r.AgentName, Label: r.DisplayName})
		}
		return out, nil
	}
}

// ensureOrOfferBlocksLogin returns the active profile's API key when logged in.
// It reads the profile (the canonical home) first, then the legacy
// credentials.json slot as a one-release migration fallback — mirroring
// loadCredentials so the wizard never re-prompts a user who is already logged in
// via a profile. Otherwise, in an interactive terminal, it offers to log in (so
// private agents appear in suggestions); declining — or any non-TTY / login
// failure — returns an empty key, i.e. anonymous access to public agents only.
func ensureOrOfferBlocksLogin(ctx context.Context) string {
	if key, ok := activeProfileAPIKey(); ok {
		return key
	}
	if key := optionalCredentials(); key != "" {
		return key
	}
	if !isTTY() {
		return ""
	}
	fmt.Println("You're not logged in — only public agents will appear in suggestions.")
	fmt.Print("  Log in now to access your private agents? (Y/n): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return ""
	}
	ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if ans == "n" || ans == "no" {
		return ""
	}
	apiKey, _, err := loginToProfile(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Login failed: %v\n  Continuing with public agents only.\n", err)
		return ""
	}
	return apiKey
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// isTTY reports whether stdin is a real terminal. Unlike isInteractive (which
// uses the looser ModeCharDevice check and is true for /dev/null), this gates
// the prompts that actually read keystrokes — the wizard entry points — so a
// non-terminal stdin (pipes, /dev/null under `go test`) never launches an
// interactive loop that would spin on EOF.
func isTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func printNextSteps(cfg wizard.Config) {
	lang := "Python"
	if cfg.Language == "node" {
		lang = "Node"
	}

	role := "agent"
	if cfg.Mode == "consumer" {
		role = "consumer"
	}

	fmt.Printf("\n  %s %s '%s' created!\n\n", lang, role, cfg.Name)
	fmt.Println("  Next steps:")
	fmt.Printf("    cd %s\n", cfg.Name)

	if cfg.Language == "node" {
		fmt.Println("    npm install")
	} else {
		fmt.Println("    pip install -e . && pip install blocks-network --upgrade")
	}

	if cfg.Mode == "consumer" {
		fmt.Println("    # 1. Set BLOCKS_API_KEY in .env  (or run 'blocks login --write-env')")
		fmt.Println("    # 2. Edit the script and set the target agent name")
		if cfg.Language == "node" {
			fmt.Println("    npm run start")
		} else {
			fmt.Println("    python main.py")
		}
		return
	}

	fmt.Println("    blocks login --write-env  # authenticate (first time only)")
	fmt.Println("    blocks register           # register the agent privately and free (recommended first step)")
	fmt.Println("    blocks run                # start the agent")
	fmt.Println("    blocks publish            # later: make the agent public or set pricing")
}

func printWebappNextSteps(cfg wizard.Config, dirName string) {
	agentList := strings.Join(cfg.Agents, ", ")
	fmt.Printf("\n  Webapp '%s' created (agents: %s)!\n\n", dirName, agentList)
	fmt.Println("  Next steps:")
	fmt.Printf("    cd %s\n", dirName)
	fmt.Println("    blocks login              # authenticate with Blocks")
	fmt.Println("    blocks dev                # start local dev server at http://localhost:4242")
	fmt.Println("")
	fmt.Println("  When ready to deploy:")
	fmt.Println("    blocks deploy cloudflare        # or vercel / netlify")
}
