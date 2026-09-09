package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
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
	initBackendURL    string
)

// initAgentNameRe enforces the bare agent-name pattern (matches registry column).
var initAgentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func init() {
	initCmd.Flags().BoolVarP(&initYes, "yes", "y", false, "Use defaults (non-interactive)")
	initCmd.Flags().StringVarP(&initLanguage, "language", "l", "", "Project language: node or python (default: python)")
	initCmd.Flags().StringVarP(&initMode, "mode", "m", "", "Project mode: provider (default), consumer, connect-agent, call-agent, or webapp")
	initCmd.Flags().StringVarP(&initType, "type", "t", "", "Deprecated: use --mode")
	_ = initCmd.Flags().MarkDeprecated("type", "use --mode instead")
	initCmd.Flags().StringSliceVar(&initAgents, "agent", nil, "Bare agent name the page calls (repeatable; required with --mode webapp)")
	initCmd.Flags().StringVar(&initBlocksBaseURL, "blocks-base-url", "",
		fmt.Sprintf("Override the Blocks asset base URL; must be https (or http for loopback) (default: %s)", scaffold.DefaultAssetBaseURL))
	initCmd.Flags().StringVar(&initBackendURL, "backend-url", "", "Backend API origin the deployed page calls at runtime (default: BLOCKS_BACKEND_URL, else active profile URL, else the build-time default, else the asset base URL)")

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
			normalizedType, ok := wizard.NormalizeMode(initType)
			if !ok {
				return fmt.Errorf("invalid --type %q — must be one of: provider, consumer, connect-agent, call-agent", initType)
			}
			if initMode != "" {
				normalizedMode, ok := wizard.NormalizeMode(initMode)
				if !ok {
					return fmt.Errorf("invalid --mode %q — must be one of: provider, consumer, connect-agent, call-agent", initMode)
				}
				if normalizedMode != normalizedType {
					return fmt.Errorf("--type and --mode conflict (%q vs %q); use --mode only", initType, initMode)
				}
			}
			initMode = normalizedType
		}

		// Webapp scaffold path (flag-driven, or interactive collection).
		if initMode == "webapp" {
			return runWebapp(cmd.Context(), nameFromArgs)
		}

		// Reject --agent on non-webapp modes (it has no meaning there).
		if len(initAgents) > 0 {
			return fmt.Errorf("--agent is only valid with --mode webapp")
		}

		if initMode != "" {
			if _, ok := wizard.NormalizeMode(initMode); !ok && initMode != "webapp" {
				return fmt.Errorf("invalid --mode %q — must be one of: provider, consumer, connect-agent, call-agent, webapp", initMode)
			}
		}

		// Determine if we should run non-interactively. --no-input counts as
		// non-interactive for the same reason it does in `publish` and
		// `register`: the caller asked not to be prompted, TTY or not. Every
		// answer the wizard collects has a default except the name, which is
		// reported below as an error naming the argument that supplies it.
		nonInteractive := initYes || !interactiveSession()

		// Interactive `blocks init` with no mode and no name: offer the
		// top-level choice between an agent project and a web app. A given
		// name or an explicit --mode keeps the historical agent behavior.
		if isTTY() && !nonInteractive && initMode == "" && nameFromArgs == "" {
			fmt.Printf("Creating a project for %s.\n", clictx.TargetName())
			if !authenticatedForTarget() && !clictx.Enterprise() {
				fmt.Println("  For a Blocks Enterprise deployment, run `blocks login <your-instance>` first.")
			}
			fmt.Println()
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
				return fmt.Errorf("agent name is required in non-interactive mode — pass it as the first argument\nUsage: blocks init <name> [--yes]")
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
			if initMode != "" {
				canonical, _ := wizard.NormalizeMode(initMode)
				cfg.Mode = canonical
				if canonical == "consumer" {
					// DefaultConfig seeds Description with "<name> agent"; for
					// consumers the interactive wizard rewrites it to "<name>
					// consumer" (wizard.go), so mirror that here or the leak
					// shows up in the scaffolded pyproject.toml.
					cfg.Description = cfg.Name + " consumer"
				}
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
			if err := confirmScaffold(); err != nil {
				return err
			}
		}

		if err := scaffold.Project(dir, cfg, nil); err != nil {
			return fmt.Errorf("scaffold failed: %w", err)
		}

		printNextSteps(cfg)
		return nil
	},
}

// confirmScaffold asks the final "write these files?" question. It is the only
// stdin read init performs outside the wizard, so it carries the --no-input gate
// itself rather than relying on the non-interactive decision above to keep it
// unreachable: the flag's contract is that a prompt becomes an error naming what
// answers it, and that must hold even if this branch is ever reached another way.
func confirmScaffold() error {
	if noInputMode {
		return fmt.Errorf("cannot ask whether to continue with --no-input — pass --yes")
	}
	if !confirmYesNo(os.Stdin, "  Continue? (Y/n): ") {
		return fmt.Errorf("canceled")
	}
	return nil
}

// maxAgentsPerWebapp caps how many agents a single webapp page may wire up.
// Mirrors internal/config.Validate and wizard.maxAgentsPerWebapp.
const maxAgentsPerWebapp = 25

// validateWebappURLFlags validates the two user-supplied webapp URL flags with
// the same scheme/host rule the embed-auth widget enforces at runtime. It is
// the single source of truth for flag validation so the flag-driven
// (runWebapp) and interactive (runWebappWizard) paths cannot drift — a gap
// that previously let the wizard bake an unvalidated cleartext asset host into
// index.html. Empty flags are valid (they resolve to defaults downstream).
func validateWebappURLFlags() error {
	if initBlocksBaseURL != "" {
		if err := config.ValidateBackendBaseURL(initBlocksBaseURL); err != nil {
			return fmt.Errorf("--blocks-base-url %q %s", initBlocksBaseURL, err.Error())
		}
	}
	if initBackendURL != "" {
		if err := config.ValidateBackendBaseURL(initBackendURL); err != nil {
			return fmt.Errorf("--backend-url %q %s", initBackendURL, err.Error())
		}
	}
	return nil
}

// runWebapp handles the --mode webapp path. With one or more --agent flags it
// runs the flag-driven scaffold; without them it runs the interactive webapp
// wizard (TTY) or errors (non-interactive).
func runWebapp(ctx context.Context, nameFromArgs string) error {
	if initLanguage != "" {
		return fmt.Errorf("--language is not valid with --mode webapp (the webapp scaffold has no language axis)")
	}
	if err := validateWebappURLFlags(); err != nil {
		return err
	}

	if len(initAgents) == 0 {
		// --no-input joins --yes and a non-terminal stdin here: the webapp wizard
		// is a chain of prompts, so a caller who asked not to be prompted is told
		// which flag supplies the missing answer instead of entering it.
		if initYes || noInputMode || !isTTY() {
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

	assetBase, backendURL, err := resolveWebappURLs(initBackendURL, initBlocksBaseURL)
	if err != nil {
		return err
	}

	// Where the cards are looked up, and which credential that lookup carries, are
	// one decision and are resolved together — see resolveCardAccess. Login is
	// optional: the registry fetch path supports anonymous public reads, so a fresh
	// machine can scaffold a page that only references public agents, and a private
	// agent requested without a credential surfaces the not-found hint in
	// scaffoldWebappProject, which already points the user at 'blocks login'.
	access, err := resolveCardAccess()
	if err != nil {
		return err
	}
	if access.backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}
	if access.backendURL != backendURL {
		fmt.Fprintf(os.Stderr,
			"  Note: agent cards will be fetched from %s but the page is wired to %s.\n",
			access.backendURL, backendURL)
	}

	cfg := wizard.Config{
		Name:           nameFromArgs,
		Mode:           "webapp",
		Agents:         append([]string(nil), initAgents...),
		BlocksBaseURL:  assetBase,
		BackendBaseURL: backendURL,
	}
	printWebappResolvedURLs(assetBase, backendURL, backendResolvedFromExplicitSource(initBackendURL))
	return scaffoldWebappProject(ctx, cfg, blocksapi.NewClient(access.backendURL, access.apiKey))
}

// cardAccess is the origin `blocks init --mode webapp` looks agent cards up on, and
// the credential those lookups carry.
//
// The two are one decision, which is why they are answered together: a credential
// resolved for this invocation belongs to the deployment the invocation resolved, so
// whether it may be sent cannot be settled without knowing where the request goes.
type cardAccess struct {
	// backendURL is the origin the card and suggestion requests are sent to. "" means
	// nothing named a deployment at all, which the caller reports.
	backendURL string
	// apiKey is the credential those requests carry, or "" for an anonymous lookup —
	// public agents only, which is a supported outcome here.
	apiKey string
	// resolvedTarget is true when backendURL is the deployment this invocation
	// resolved. Only then does a credential held in local storage describe it, and
	// only then can an offer to log in produce one for it.
	resolvedTarget bool
}

// resolveCardAccess answers both halves from the resolved request context rather than
// by reading the profile store or the legacy credential file.
//
// Those two files are the last two tiers of one precedence that also covers
// --api-key, --api-key-stdin and BLOCKS_API_KEY, and reading either directly answered
// a different question from the one the request asks. The flag-driven path read only
// the legacy file, so an ambient BLOCKS_API_KEY for the deployment being queried was
// ignored and a private card came back not-found; the wizard path preferred the
// active profile, so a profile for deployment A supplied the key for a lookup against
// deployment B. clictx applies the precedence once, and withholds a stored key when
// an ambient backend URL has displaced the profile holding it.
//
// --backend-url is the one input clictx cannot see: it is init's own flag, and it
// steers where this page and this lookup point rather than what the rest of the
// invocation targets. So it is the only thing that can send the lookup to a
// deployment other than the resolved one, and a key that came out of local storage
// does not travel there — it was minted at the deployment its profile describes. A
// key the invocation itself named (--api-key, --api-key-stdin, BLOCKS_API_KEY) does
// travel, because the caller named it for this command, with this flag, in one breath.
//
// The origin follows the rule both webapp paths already applied: the page's own
// backend whenever that came from an explicit source, so the wiring is snapshotted
// from the deployment the page will sign into, and otherwise the CLI's own resolution
// (which may consult remote config). Both are now expressed once — the flag, else the
// resolved target — instead of re-deriving the precedence to ask which tier won.
func resolveCardAccess() (cardAccess, error) {
	access := cardAccess{resolvedTarget: true}
	if flag := trimURL(strings.TrimSpace(initBackendURL)); flag != "" {
		access.backendURL = flag
		// Compared against the *effective* target, not the offline half. BackendURL() stops
		// before the remote tier, so in a build where Blocks Network's backend is learned
		// through CDM it answers "" — and a --backend-url naming exactly that deployment was
		// then classified as foreign, the stored credential withheld, and the private-card
		// lookup done anonymously. A private agent comes back as not found, which reads as
		// the agent not existing rather than as a credential that was never sent.
		//
		// A target that cannot be established at all leaves resolvedTarget false, which is
		// the conservative end: it withholds a stored key rather than sending it to an origin
		// nothing verified. That is not a reason to fail the scaffold.
		if effective, err := clictx.EffectiveBackendURL(); err == nil {
			access.resolvedTarget = profiles.SameBaseURL(flag, effective)
		} else {
			access.resolvedTarget = false
		}
	} else {
		access.backendURL = trimURL(resolveBackendURL())
	}
	c := clictx.EffectiveCredential()
	if c.Err != nil {
		// The resolver sets Err only when this invocation cannot produce a credential it
		// should have: an unreadable --api-key-stdin, or a stored key withheld because an
		// override points somewhere the active profile does not describe. Scaffolding
		// anonymously instead would ignore what the caller asked for and snapshot whatever
		// the public registry happens to hold.
		//
		// The Source.External() guard this replaces let the second case through, because
		// displacedCredentialError carries no Source: the resolver said "you are logged in,
		// just not to this deployment" and init read that as "not logged in".
		return cardAccess{}, c.Err
	}
	if c.StoreErr != nil {
		// An unreadable credential store is not an absent login either. Left unreported it
		// turns a local storage failure into an anonymous lookup, and a private agent then
		// comes back as simply not found — the least actionable outcome available.
		return cardAccess{}, fmt.Errorf("failed to load credentials: %w", c.StoreErr)
	}
	if c.Expired {
		// Same reasoning once more: a stored credential that has expired is a login to
		// renew, not an absence of one, and saying so beats an anonymous lookup that
		// reports the user's own private agent as missing.
		return cardAccess{}, fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate")
	}
	if c.Key != "" && (c.Source.Supplied() || access.resolvedTarget) {
		access.apiKey = c.Key
	}
	return access, nil
}

// authenticatedForTarget reports whether this invocation has a credential for the
// deployment it will actually reach, which is what decides whether init tells the
// user to log in.
//
// It asks the resolved context rather than the active profile. The profile is only
// one tier of the credential precedence and describes the request only while it is
// still the target: a profile key found while an ambient backend URL pointed
// elsewhere reported "logged in" for a deployment holding no key for the caller, so
// the next-steps block dropped the login that deployment requires — and an ambient
// BLOCKS_API_KEY, which every command would send, counted for nothing.
func authenticatedForTarget() bool {
	return clictx.EffectiveCredential().Key != ""
}

// runWebappWizard runs the interactive webapp wizard: it offers login (so
// private agents appear in suggestions), collects a project name + agent list
// via the type-ahead autocomplete, then scaffolds.
func runWebappWizard(ctx context.Context) error {
	if err := validateWebappURLFlags(); err != nil {
		return err
	}

	assetBase, resolvedBackend, err := resolveWebappURLs(initBackendURL, initBlocksBaseURL)
	if err != nil {
		return err
	}

	// Suggestions and card fetches share one origin and one credential with the
	// flag-driven path (resolveCardAccess), and both are settled before the login
	// offer: whether logging in would even help depends on which deployment the
	// lookups are pointed at, so a key cannot be resolved before that is known.
	access, err := resolveCardAccess()
	if err != nil {
		return err
	}
	if access.backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}
	if access.backendURL != resolvedBackend {
		fmt.Fprintf(os.Stderr,
			"  Note: agent cards will be fetched from %s but the page is wired to %s.\n",
			access.backendURL, resolvedBackend)
	}
	access.apiKey, err = ensureOrOfferBlocksLogin(ctx, access)
	if err != nil {
		return err
	}
	client := blocksapi.NewClient(access.backendURL, access.apiKey)

	cfg, err := wizard.RunWebapp(ctx, makeAgentSuggestFn(client))
	if err != nil {
		return err
	}
	cfg.BlocksBaseURL = assetBase
	cfg.BackendBaseURL = resolvedBackend
	printWebappResolvedURLs(assetBase, cfg.BackendBaseURL, backendResolvedFromExplicitSource(initBackendURL))
	return scaffoldWebappProject(ctx, cfg, client)
}

// scaffoldWebappProject fetches the agent cards named in cfg.Agents and writes
// the webapp scaffold. On any failure after the directory is created, the
// directory is removed so no partial scaffold remains. Shared by both the
// flag-driven and interactive webapp paths.
func scaffoldWebappProject(ctx context.Context, cfg wizard.Config, client *blocksapi.Client) error {
	// Validate the fully-resolved backend origin before writing anything. The
	// flags are checked earlier in runWebapp, but the resolved value can also
	// come from BLOCKS_BACKEND_URL, the active profile, or the ldflag default —
	// none of which the flag checks cover. Failing here keeps an origin the
	// embed-auth widget would reject at sign-in from ever being baked in.
	if err := config.ValidateBackendBaseURL(cfg.BackendBaseURL); err != nil {
		return fmt.Errorf("resolved backend URL %q %s — set --backend-url, or fix BLOCKS_BACKEND_URL / your active profile", cfg.BackendBaseURL, err.Error())
	}

	// Defense in depth: the asset host is baked into index.html as the
	// widget-bundle <script src> origin. When --blocks-base-url is unset it
	// mirrors the resolved backend origin (profile / env / --backend-url), so
	// this value can come from any of those sources — validate it here too,
	// not just at the flag check in runWebapp. Empty means "use the default".
	if cfg.BlocksBaseURL != "" {
		if err := config.ValidateBackendBaseURL(cfg.BlocksBaseURL); err != nil {
			return fmt.Errorf("resolved asset host %q %s — set --blocks-base-url, or fix BLOCKS_BACKEND_URL / your active profile", cfg.BlocksBaseURL, err.Error())
		}
	}

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
				// The name and the remedy are on separate lines because the name comes
				// from the registry — the suggestion list this wizard populates from the
				// backend — and the remedy is worded as a command. One line carrying
				// both is a line a user can paste with an attacker's text inside it,
				// which no amount of escaping the display makes safe to run.
				return fmt.Errorf("agent %q not found — check the spelling.\n  If it is a private agent your account can access, log in first: run 'blocks login'.", name)
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

// ensureOrOfferBlocksLogin returns the credential the webapp wizard's lookups will
// carry, offering a login when there is none and one would help.
//
// The credential arrives in access, resolved once for the whole invocation, so the
// wizard never re-prompts a caller who already has a usable key — from a profile, the
// legacy credential file, or the environment — and never sends one deployment's stored
// key to another. It reads neither store itself: both are tiers of that one
// precedence, and a second reading of them is what sent A's key to B.
//
// A lookup pointed away from the deployment this invocation resolved (--backend-url)
// gets no offer at all: a login authenticates against the resolved deployment, so the
// key it mints would not describe the one being queried, and asking would trade a
// prompt for nothing. That is the same outcome as declining — anonymous access to
// public agents. So is a non-TTY session, --yes, or a failed login.
//
// Returns an error when --no-input is set and a prompt would be required.
func ensureOrOfferBlocksLogin(ctx context.Context, access cardAccess) (string, error) {
	if access.apiKey != "" {
		return access.apiKey, nil
	}
	if !access.resolvedTarget {
		return "", nil
	}
	if initYes {
		return "", nil // --yes means no prompts, decline login
	}
	if !isTTY() {
		return "", nil
	}
	fmt.Println("You're not logged in — only public agents will appear in suggestions.")
	fmt.Print("  Log in now to access your private agents? (Y/n): ")

	// Use shared scanner instead of private one
	line, ok, noInputRequested := readStdinLine()
	if noInputRequested {
		return "", fmt.Errorf("cannot ask whether to log in with --no-input — run 'blocks login' first, or pass --agent to scaffold public agents without the wizard")
	}
	if !ok {
		return "", nil
	}
	ans := strings.TrimSpace(strings.ToLower(line))
	if ans == "n" || ans == "no" {
		return "", nil
	}
	out, err := loginToProfile(ctx, deploymentChoice{instanceURL: "", explicitNetwork: false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Login failed: %v\n  Continuing with public agents only.\n", err)
		return "", nil
	}
	return out.apiKey, nil
}

// isInteractive reports whether stdin is a terminal. It is a var so tests
// can exercise interactive-only paths.
var isInteractive = func() bool {
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
// interactive loop that would spin on EOF. It is a var so tests can exercise
// TTY-only paths.
var isTTY = func() bool {
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

	// Print login line only if not already logged in.
	//
	// The enterprise step names a placeholder rather than the active profile, which it
	// knows. A profile name reaches the store from the host slug of whatever deployment
	// a login reached, and that host can come from a project .env or from a
	// deployment's own discovery payload — so it is attacker-influenceable text, and
	// this is a line the user is invited to copy. termsafe.Text would not make it safe
	// to paste: `;`, `&&`, backticks and $(...) are ordinary printable characters that
	// survive it intact. The rule is the one declinedBackendPinNote (cmd/root.go)
	// states — a line formatted as a command carries no value but the user's own — and
	// the user knows their own instance.
	//
	// Whether a login is still needed is asked of the credential this invocation would
	// actually send (authenticatedForTarget), not of the active profile: the profile
	// can hold a key for a deployment this project is not pointed at, in which case the
	// login it omitted is exactly the one the user has to run.
	if !authenticatedForTarget() {
		if clictx.Enterprise() {
			fmt.Println("    blocks login <your-instance> --write-env  # authenticate (first time only)")
		} else {
			fmt.Println("    blocks login --write-env  # authenticate with Blocks Network (first time only)")
			fmt.Println("                              # for Blocks Enterprise: blocks login <your-instance> --write-env")
		}
	}

	if clictx.Enterprise() {
		fmt.Println("    blocks register           # register the agent privately (recommended first step)")
	} else {
		fmt.Println("    blocks register           # register the agent privately and free (recommended first step)")
	}
	fmt.Println("    blocks run                # start the agent")
	if clictx.Enterprise() {
		fmt.Println("    blocks publish            # later: change visibility with --listing public")
	} else {
		fmt.Println("    blocks publish            # later: make the agent public or set pricing")
	}
}

// printWebappResolvedURLs surfaces the two URLs the scaffold froze in, so a
// user who scaffolded under the wrong profile sees it immediately rather than
// discovering a bundle pointed at the wrong backend after deploy. The fallback
// hint fires only when no backend was explicitly resolved (flag/env/profile/
// ldflag all empty) so it is not shown to a user who deliberately targeted the
// asset host.
func printWebappResolvedURLs(assetBase, backendURL string, resolvedFromExplicitSource bool) {
	fmt.Printf("\n  Resolved URLs (frozen into the scaffold):\n")
	fmt.Printf("    Backend API: %s\n", backendURL)
	fmt.Printf("    Asset host:  %s\n", assetBase)
	if !resolvedFromExplicitSource {
		fmt.Printf("    (No backend was specified, so the asset host above will be used. To\n")
		fmt.Printf("     target another backend, pass --backend-url, set BLOCKS_BACKEND_URL,\n")
		fmt.Printf("     or switch profiles with 'blocks profile use'.)\n")
	}
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
