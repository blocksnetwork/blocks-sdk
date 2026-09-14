package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/auth/partners"
	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

const helpDeployTarget = "  The hosting partner to deploy your web/ directory to.\n" +
	"  Cloudflare Pages, Vercel, and Netlify are built in; user-defined targets\n" +
	"  (from ~/.config/blocks/deploy-targets) also appear here."

// divergenceNoInputError refuses the "continue deploying anyway?" confirmation
// under --no-input.
//
// It is the one refusal in this command that cannot name a flag, because `blocks
// deploy` has none that answers the question and this change does not invent one:
// the backend the bundle calls is frozen into web/app.js at `blocks init` time, so
// the only way to answer "deploy a bundle built for somewhere else?" without being
// asked is to stop the two from disagreeing. It refuses rather than assuming yes
// for the reason the login deployment picker gives: a caller who asked not to be
// asked also refused to be guessed at, and the guess here publishes a page that
// talks to a backend they did not target.
const divergenceNoInputError = "cannot ask whether to deploy a bundle built for a different backend with --no-input — " +
	"no flag answers this prompt: re-run 'blocks init --mode webapp --agent <agent> --backend-url <the backend you are targeting>' " +
	"so web/ is rebuilt for it, or select the profile the bundle was built for, then deploy again"

var (
	deployList         bool
	deployNoCardUpdate bool
	deployCardPaths    []string
)

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().BoolVar(&deployList, "list", false, "List all registered deploy targets (built-in + on-disk) and exit")
	deployCmd.Flags().BoolVar(&deployNoCardUpdate, "no-card-update", false, "Skip the post-deploy prompt to update local agent cards")
	deployCmd.Flags().StringSliceVar(&deployCardPaths, "card-path", nil, "Override agent-card path for one invocation, e.g. --card-path echo=../echo/agent-card.json (repeatable)")

	// Load on-disk plugins at startup. Failures are surfaced when the user
	// runs `blocks deploy`; we don't fail the whole CLI here so unrelated
	// commands still work even if a plugin file is malformed.
	loadDeployPlugins()
}

var deployCmd = &cobra.Command{
	Use:   "deploy [target]",
	Short: "Deploy the webapp to a hosting partner",
	Long: `Deploy the web/ directory to a hosting partner (Cloudflare Pages,
Vercel, Netlify, or a user-defined target).

The target is resolved as follows:
  1. The positional [target] argument, if given.
  2. In a terminal, an interactive picker over the registered targets,
     defaulting to the last-used target from blocks.config.json.
  3. Non-interactive: the "deployTarget" field in blocks.config.json.
  4. Otherwise: error: no target.

Examples:
  blocks deploy cloudflare
  blocks deploy vercel
  blocks deploy netlify
  blocks deploy            # prompt (last target pre-selected), or deployTarget if non-interactive
  blocks deploy --list     # show registered targets`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()

		if deployList {
			printDeployTargets()
			return nil
		}

		var target string
		if len(args) > 0 {
			target = args[0]
		}
		return runDeploy(ctx, target)
	},
}

// loadDeployPlugins discovers user-defined deploy targets in
// $XDG_CONFIG_HOME/blocks/deploy-targets (or ~/.config/blocks/deploy-targets).
func loadDeployPlugins() {
	dir, err := deploy.DefaultPluginDir()
	if err != nil {
		return
	}
	if err := deploy.LoadPlugins(dir); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: deploy plugin load: %v\n", err)
	}
}

// selectDeployTarget presents an interactive picker over the registered
// deploy targets and returns the chosen target name. defaultName (e.g. the
// last-used target) is pre-selected when it matches a registered target. It
// returns "" (no selection) when stdin is not a terminal or no targets are
// registered, so callers fall through to the non-interactive resolution.
//
// It carries the --no-input gate itself rather than relying on its caller to keep
// the picker unreachable, so a second call site cannot reintroduce a prompt the
// caller asked never to see. Returning "" rather than an error is what routes such
// a caller into the same non-interactive resolution a non-terminal session gets.
func selectDeployTarget(defaultName string) (string, error) {
	if !isTTY() || noInputMode {
		return "", nil
	}
	adapters := deploy.List()
	if len(adapters) == 0 {
		return "", nil
	}
	defaultIdx := 0
	labels := make([]string, len(adapters))
	for i, a := range adapters {
		if a.Name == defaultName {
			defaultIdx = i
		}
		if a.Description != "" {
			labels[i] = fmt.Sprintf("%s — %s", a.Name, a.Description)
		} else {
			labels[i] = a.Name
		}
	}
	idx, err := wizard.InteractiveSelect("Deploy target", labels, defaultIdx, helpDeployTarget)
	if err != nil {
		return "", err
	}
	return adapters[idx].Name, nil
}

func printDeployTargets() {
	fmt.Println("Available deploy targets:")
	for _, a := range deploy.List() {
		fmt.Printf("  %-12s  [%s]  %s\n", a.Name, a.Source, a.Description)
	}
}

func runDeploy(ctx context.Context, target string) error {
	// Parsed before anything is uploaded. A malformed --card-path is a mistake in the
	// invocation, knowable without deploying anything, and the card walk that consumes
	// it runs after a non-idempotent upload — so discovering it there would report a
	// completed deploy as a failure and invite a retry that uploads again.
	cardOverrides, err := parseCardPathFlags(deployCardPaths)
	if err != nil {
		return err
	}

	cfgPath := filepath.Join(mustCwd(), "blocks.config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("blocks.config.json: %w", err)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("blocks.config.json: %w", err)
	}

	// Name the directory being deployed before anything uploads: the scaffold
	// creates a subdirectory, so it is easy to be one directory off and not
	// notice until the wrong page is live. The path is termsafe'd — a
	// directory name can carry control or bidi characters, and this is a
	// line the operator reads to confirm what is about to be published.
	fmt.Printf("Project: %s\n", termsafe.Text(mustCwd()))

	// Surface what backend the bundle will talk to, and loudly warn if the
	// current environment intends a different backend than what was baked in
	// at `blocks init` time (the profile-switch footgun). We never rewrite —
	// the URL and derived partition keys are frozen in web/app.js.
	fmt.Printf("Backend API (baked at init): %s\n", termsafe.Text(cfg.BackendBaseUrl))
	intended, err := currentIntendedBackendURL()
	if err != nil {
		return fmt.Errorf("cannot determine the intended backend for the divergence check: %w", err)
	}
	// Compared semantically, not as trimmed text: `https://HOST:443` and `https://host`
	// are one origin, and a textual difference between them raised a divergence warning
	// for a bundle that was built for exactly the backend now active — and under
	// --no-input turned that false positive into a hard failure.
	if intended != "" && !profiles.SameBaseURL(intended, cfg.BackendBaseUrl) {
		// Both URLs are reported, and neither is interpolated into the command that
		// remedies the divergence. One arrives from blocks.config.json and the other
		// from a profile or a project .env, so a hostile project could otherwise get
		// extra shell words into a line the user is invited to copy — and termsafe.Text
		// does nothing about that, since `;`, `&&`, backticks and $(...) are ordinary
		// printable characters it passes through unchanged. It is applied because the
		// two protections are orthogonal: it keeps the values safe to display, and
		// naming the flags rather than filling them in keeps the line safe to paste.
		fmt.Fprintf(os.Stderr,
			"\nWarning: this bundle was built to call %s, but your active backend is %s.\n"+
				"  The deployed page will keep talking to the backend it was built for.\n"+
				"  To retarget, select the profile the bundle was built for, or re-scaffold\n"+
				"  web/ for the backend you are targeting and deploy again:\n"+
				"    run: blocks init --mode webapp --agent <agent> --backend-url <backend-url>\n\n",
			termsafe.Text(cfg.BackendBaseUrl), termsafe.Text(intended))
		// --no-input is checked ahead of the TTY guard because the two mean
		// different things: not being on a terminal means the CLI cannot ask, so it
		// takes the documented default of warning and continuing, whereas
		// --no-input means the caller refused to be asked — which is also a refusal
		// to be guessed at. Nothing about the plain non-terminal path changes.
		if noInputMode {
			return errors.New(divergenceNoInputError)
		}
		if isTTY() {
			if !confirmYesNo(os.Stdin, "  Continue deploying anyway? (Y/n): ") {
				return fmt.Errorf("canceled")
			}
		}
	}

	// Target resolution:
	//   1. Explicit positional arg wins (scripted / intentional).
	//   2. Interactive terminal: always prompt, defaulting the picker to the
	//      last-used target so Enter reuses it and arrows pick another. This
	//      is why a prior deploy persisting deployTarget doesn't silently lock
	//      you into that target.
	//   3. Non-interactive: fall back to the saved deployTarget (no prompt).
	//
	// --no-input joins case 3 rather than becoming a refusal of its own, because
	// this question — unlike the confirmation above — has a documented answer the
	// caller supplied themselves: the positional argument, or the deployTarget in
	// blocks.config.json, which is there only because a previous deploy of this
	// project chose it. Nothing is invented, and when neither exists the error
	// below already names both. Sending it down the same branch a non-terminal
	// session takes also means --no-input has one meaning here, not two.
	if target == "" {
		if isTTY() && !noInputMode {
			selected, err := selectDeployTarget(cfg.DeployTarget)
			if err != nil {
				// Esc at the picker is a deliberate walk-away, not a failure:
				// report it as a cancel and deploy nothing.
				if errors.Is(err, wizard.ErrCanceled) {
					return fmt.Errorf("canceled")
				}
				return err
			}
			if selected != "" {
				// A picked target is one Enter away from an upload the user may
				// not have intended — the picker accepts the highlighted row on
				// Enter, and the default row is the last-used target. One
				// explicit confirmation before anything leaves the machine.
				// A positional target confirms nothing: it names the target in
				// the invocation itself.
				if !confirmYesNo(os.Stdin, fmt.Sprintf("  Deploy web/ to %s? (Y/n): ", selected)) {
					return fmt.Errorf("canceled")
				}
				target = selected
			}
			// selected == "" (nothing registered to pick) falls through to
			// the no-target error below, which names --list.
		} else {
			target = cfg.DeployTarget
		}
	}
	if target == "" {
		return fmt.Errorf("no deploy target specified; pass <target> as a positional arg or set deployTarget in blocks.config.json (try 'blocks deploy --list' to see options)")
	}

	adapter, ok := deploy.Resolve(target)
	if !ok {
		return fmt.Errorf("unsupported deploy target %q; try 'blocks deploy --list' for the registered set", target)
	}

	creds, err := ensureDeployCredentials(ctx, adapter)
	if err != nil {
		return fmt.Errorf("credentials for %s: %w", target, err)
	}

	assetsDir := filepath.Join(mustCwd(), "web")
	if _, err := os.Stat(assetsDir); err != nil {
		return fmt.Errorf("web/ directory not found — run 'blocks init <name> --mode webapp --agent <agent>' first")
	}

	fmt.Printf("Deploying web/ to %s...\n", target)
	deployedURL, err := adapter.Upload(ctx, creds, assetsDir)
	if err != nil {
		return fmt.Errorf("deploy to %s: %w", target, err)
	}
	fmt.Printf("Deployed: %s\n", deployedURL)

	cfg.LastDeployedUrl = deployedURL
	cfg.DeployTarget = target
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("save blocks.config.json: %w", err)
	}

	// The upload and the config write above have already happened, so nothing the card
	// walk runs into can change what this command reports: it warns, notes and skips,
	// and the deploy stays a success. Its exit code describes the deployment.
	if !deployNoCardUpdate {
		maybeUpdateLocalAgentCards(cfg, deployedURL, cardOverrides, os.Stdin, os.Stdout, os.Stderr)
	}

	return nil
}

// ensureDeployCredentials acquires partner credentials for the named adapter.
//
// Source-aware dispatch: built-in adapters delegate to the per-partner flows
// in internal/auth/partners; disk-defined adapters use the generic plugin
// credential path regardless of name. This matters because the registry lets
// a disk plugin override a built-in by name (`~/.config/blocks/deploy-targets/cloudflare.yml`
// shadows the built-in `cloudflare`) — a name-only switch would still run
// `CloudflareFlow` against the override, defeating the override's whole point.
func ensureDeployCredentials(ctx context.Context, a deploy.Adapter) (*auth.ProviderCredentials, error) {
	if a.Source == deploy.SourceBuiltin {
		r := noInputTokenReader(a)
		switch a.Name {
		case "cloudflare":
			return (&partners.CloudflareFlow{Reader: r}).Ensure(ctx)
		case "vercel":
			return (&partners.VercelFlow{Reader: r}).Ensure(ctx)
		case "netlify":
			return (&partners.NetlifyFlow{Reader: r}).Ensure(ctx)
		}
	}
	return ensureGenericPluginCredentials(a)
}

// noInputTokenReader returns what a built-in partner flow should read its token
// prompt from: os.Stdin (nil) normally, or a reader that fails with the --no-input
// refusal when the caller asked not to be prompted.
//
// The refusal is handed to the flow as its reader rather than checked before
// calling it because the flow owns the precedence — environment variable, then
// stored credential, then prompt — and only the last tier touches stdin. Refusing
// up front would fail invocations that had a token all along; refusing through the
// reader fires exactly where the prompt would have. It is also why this does not
// gate partners.promptToken itself: that prompt is shared with 'blocks login
// --provider', a separate question with its own answer.
func noInputTokenReader(a deploy.Adapter) io.Reader {
	if !noInputMode {
		return nil
	}
	return errorReader{err: errors.New(tokenPromptNoInputError(a))}
}

// tokenPromptNoInputError words the refusal for an API-token prompt, for the
// built-in partner flows and on-disk plugins alike. The variable it names comes off
// the adapter, so the message cannot name a different one than the credential path
// actually reads, and 'blocks login --provider' is offered only for built-ins,
// which are the only targets it can store a token for.
func tokenPromptNoInputError(a deploy.Adapter) string {
	remedy := fmt.Sprintf("declare credentialEnvVar in the %s plugin manifest and set it in the environment", a.Name)
	if a.CredentialEnvVar != "" {
		remedy = "set " + a.CredentialEnvVar
	}
	if a.Source == deploy.SourceBuiltin {
		remedy += fmt.Sprintf(", or run 'blocks login --provider %s' once to store a token", a.Name)
	}
	return fmt.Sprintf("cannot ask for a %s API token with --no-input — %s", a.Name, remedy)
}

// errorReader fails every read with err. It stands in for stdin at a prompt that
// must not happen, where the code that would prompt is not this command's to gate.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

// ensureGenericPluginCredentials handles credentials for on-disk plugins.
// Honors the configured CredentialEnvVar (env-var first); for api-token flow
// it prompts interactively if neither env nor stored token is available.
// For now stored-token persistence for plugins is deferred — users either
// export the env var or paste the token each run.
func ensureGenericPluginCredentials(a deploy.Adapter) (*auth.ProviderCredentials, error) {
	switch a.Credential {
	case deploy.CredentialFlowNone, "":
		return &auth.ProviderCredentials{Provider: a.Name, Kind: auth.CredentialKindAPIToken}, nil
	case deploy.CredentialFlowAPIToken:
		if a.CredentialEnvVar != "" {
			if v := os.Getenv(a.CredentialEnvVar); v != "" {
				return &auth.ProviderCredentials{
					Provider:    a.Name,
					Kind:        auth.CredentialKindAPIToken,
					AccessToken: v,
				}, nil
			}
		}
		// Checked after the environment variable and before the prompt — the same
		// place the built-in flows refuse — so --no-input only ever declines a read
		// that was actually going to happen.
		if noInputMode {
			return nil, errors.New(tokenPromptNoInputError(a))
		}
		prompt := a.CredentialPrompt
		if prompt == "" {
			prompt = fmt.Sprintf("Paste your %s API token: ", a.Name)
		}
		token, err := readLineFromStdin(prompt)
		if err != nil {
			return nil, err
		}
		return &auth.ProviderCredentials{
			Provider:    a.Name,
			Kind:        auth.CredentialKindAPIToken,
			AccessToken: token,
		}, nil
	case deploy.CredentialFlowBrowserGrant:
		return nil, fmt.Errorf("credentialFlow browser-grant is not yet supported for plugin %s", a.Name)
	}
	return nil, fmt.Errorf("plugin %s: unknown credentialFlow %q", a.Name, a.Credential)
}

// trimURL normalizes a URL for comparison by stripping trailing slashes. It
// delegates to config.TrimURL so the normalization rule lives in one place.
func trimURL(s string) string { return config.TrimURL(s) }

func readLineFromStdin(prompt string) (string, error) {
	fmt.Print(prompt)
	var line string
	if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("token cannot be empty")
	}
	return line, nil
}
