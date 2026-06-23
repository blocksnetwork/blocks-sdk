package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/spf13/cobra"
)

// rootProfile is bound to the persistent --profile flag and resolved in
// PersistentPreRun (after flag parsing) so it wins over BLOCKS_PROFILE and the
// saved active profile.
var rootProfile string

func init() {
	// Load .env from cwd before any command runs (don't override existing env).
	loadEnvFile(".env")
	rootCmd.PersistentFlags().StringVar(&rootProfile, "profile", "", "Deployment profile to use; also names the profile created by 'blocks login' (overrides BLOCKS_PROFILE and the saved active profile)")
}

func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if os.Getenv(k) != "" {
			continue
		}
		os.Setenv(k, strings.TrimSpace(v))
	}
}

var rootCmd = &cobra.Command{
	Use:     "blocks",
	Version: Version,
	Short:   "Blocks CLI",
	Long: `Blocks CLI — build and manage AI agents.

Quick start:
  blocks init my_agent                    Scaffold a new agent (provider) project
  blocks init my_consumer --mode consumer Scaffold a new consumer project
  cd my_agent && blocks login --write-env Authenticate (first time only)
  blocks register                         Register the agent privately and free (recommended first step)
  blocks run                              Start the agent locally
  blocks publish                          Later: make the agent public or set pricing

Authentication & publishing:
  blocks login     Authenticate and store API credentials
  blocks register  Register an agent privately and free (recommended first step)
  blocks publish   Publish an agent — public/private, free/paid (requires prior login)
  blocks logout    Remove stored credentials
  blocks whoami    Show current identity

Dashboard:
  blocks dashboard  Open the agent dashboard`,
	SilenceUsage:  true,
	SilenceErrors: true,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
	},
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Branding is set once here, AFTER flag parsing, so --profile wins over
		// BLOCKS_PROFILE and the saved active profile. Static Short/Long strings
		// render before this hook and so cannot reflect runtime branding — they
		// are kept brand-neutral instead.
		if rootProfile != "" {
			profiles.SetActiveOverride(rootProfile)
		}
		if _, p, err := profiles.Active(); err == nil && p.ProductName != "" {
			branding.Set(p.ProductName)
		}
	},
	PersistentPostRun: func(cmd *cobra.Command, args []string) {
		if cmd.Name() != "upgrade" {
			checkForUpdateNotice()
		}
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func mustCwd() string {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not get working directory: %s\n", err)
		os.Exit(1)
	}
	return cwd
}
