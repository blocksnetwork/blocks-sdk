package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	// Load .env from cwd before any command runs (don't override existing env).
	loadEnvFile(".env")
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
	Use:           "blocks",
	Version:       Version,
	Short:         "Blocks CLI",
	Long: `Blocks CLI — build and manage AI agents on the Blocks Network.

Quick start:
  blocks init my_agent                    Scaffold a new agent (provider) project
  blocks init my_consumer --type consumer Scaffold a new consumer project
  cd my_agent && blocks publish           Publish the agent to the registry
  blocks run                              Start the agent locally

Authentication:
  blocks login     Authenticate and store API credentials
  blocks publish   Authenticate (if needed) and publish
  blocks logout    Remove stored credentials
  blocks whoami    Show current identity

Dashboard:
  blocks dashboard  Open the agent dashboard`,
	SilenceUsage:  true,
	SilenceErrors: true,
	CompletionOptions: cobra.CompletionOptions{
		DisableDefaultCmd: true,
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
