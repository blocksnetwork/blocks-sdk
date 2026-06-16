package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/auth/partners"
	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

const helpDeployTarget = "  The hosting partner to deploy your web/ directory to.\n" +
	"  Cloudflare Pages, Vercel, and Netlify are built in; user-defined targets\n" +
	"  (from ~/.config/blocks/deploy-targets) also appear here."

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
func selectDeployTarget(defaultName string) (string, error) {
	if !isTTY() {
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
	cfgPath := filepath.Join(mustCwd(), "blocks.config.json")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("blocks.config.json: %w", err)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("blocks.config.json: %w", err)
	}

	// Target resolution:
	//   1. Explicit positional arg wins (scripted / intentional).
	//   2. Interactive terminal: always prompt, defaulting the picker to the
	//      last-used target so Enter reuses it and arrows pick another. This
	//      is why a prior deploy persisting deployTarget doesn't silently lock
	//      you into that target.
	//   3. Non-interactive: fall back to the saved deployTarget (no prompt).
	if target == "" {
		if isTTY() {
			selected, err := selectDeployTarget(cfg.DeployTarget)
			if err != nil {
				return err
			}
			target = selected
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

	if !deployNoCardUpdate {
		overrides, err := parseCardPathFlags(deployCardPaths)
		if err != nil {
			return err
		}
		maybeUpdateLocalAgentCards(cfg, deployedURL, overrides, os.Stdin, os.Stdout, os.Stderr)
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
		switch a.Name {
		case "cloudflare":
			return (&partners.CloudflareFlow{}).Ensure(ctx)
		case "vercel":
			return (&partners.VercelFlow{}).Ensure(ctx)
		case "netlify":
			return (&partners.NetlifyFlow{}).Ensure(ctx)
		}
	}
	return ensureGenericPluginCredentials(a)
}

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
