package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/spf13/cobra"
)

var loginApiKey string
var loginApiKeyStdin bool
var loginWriteEnv bool
var loginNoWriteEnv bool
var loginDir string

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&loginApiKey, "api-key", "", "Use a pre-obtained API key")
	loginCmd.Flags().BoolVar(&loginApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	loginCmd.Flags().BoolVar(&loginWriteEnv, "write-env", false, "Also write BLOCKS_API_KEY to the project .env (non-interactive)")
	loginCmd.Flags().BoolVar(&loginNoWriteEnv, "no-write-env", false, "Skip writing BLOCKS_API_KEY to the project .env and skip the prompt")
	loginCmd.Flags().StringVar(&loginDir, "dir", "", "Directory to write .env into (default: current directory)")
	loginCmd.MarkFlagsMutuallyExclusive("write-env", "no-write-env")
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate and store API credentials",
	Long: `Authenticates via browser login (or provided key) and stores the API key
for future commands. Always performs a fresh login even if credentials already
exist. In an interactive terminal, offers to write BLOCKS_API_KEY into the
project .env; pass --write-env to opt in or --no-write-env to opt out
non-interactively (recommended for coding-agent / scripted use). When stdin
is not a TTY and no flag is given, the prompt is skipped and .env is left
untouched.

Non-interactive usage (CI / automation):
  blocks login --api-key <key> --write-env --dir ./my_agent
  blocks login --no-write-env`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runLogin(ctx)
	},
}

func runLogin(ctx context.Context) error {
	backendURL := resolveBackendURL()
	clientID := resolveClientID()

	// For the browser path (no --api-key flags), we need to delete cached
	// creds so EnsureCredentials runs the browser flow instead of returning
	// the existing key. Back up first so we can restore on failure.
	var backedUp *auth.Credentials
	if loginApiKey == "" && !loginApiKeyStdin {
		if existing, err := auth.Load(); err == nil {
			backedUp = existing
		}
		auth.Delete() // ignore error — file may not exist
	}

	apiKey, err := auth.EnsureCredentials(ctx, backendURL, clientID, loginApiKey, loginApiKeyStdin)
	if err != nil {
		// Restore previous credentials so the user isn't left logged out
		if backedUp != nil {
			_ = auth.Save(backedUp)
		}
		return fmt.Errorf("login failed: %w", err)
	}

	// Load saved credentials for display (org info if available)
	creds, loadErr := auth.Load()

	credPath, _ := auth.CredentialPathFunc()

	if loadErr == nil && creds.OrgName != "" {
		fmt.Printf("✓ Logged in (org: %s)\n", creds.OrgName)
	} else {
		fmt.Println("✓ API key saved")
	}

	fmt.Printf("  API key: %s\n", registry.MaskAPIKey(apiKey))
	fmt.Printf("  Saved to %s\n", credPath)

	if shouldWriteEnv() {
		envDir := loginDir
		if envDir == "" {
			envDir = mustCwd()
		}
		if err := auth.InjectEnvAt(envDir, "BLOCKS_API_KEY", apiKey); err != nil {
			if loginWriteEnv {
				return fmt.Errorf("failed to write .env: %w", err)
			}
			fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
		}
	}

	return nil
}

// shouldWriteEnv decides whether to inject BLOCKS_API_KEY into the project .env.
// Order of precedence:
//  1. --no-write-env flag set -> no, unconditionally (skip the prompt too).
//  2. --write-env flag set -> yes, unconditionally.
//  3. --api-key or --api-key-stdin provided (implies automation) -> no, unless --write-env.
//  4. Non-interactive stdin (piped / CI) -> no, keep login side-effect-free.
//  5. Interactive terminal -> prompt the user (default yes).
func shouldWriteEnv() bool {
	if loginNoWriteEnv {
		return false
	}
	if loginWriteEnv {
		return true
	}
	if loginApiKey != "" || loginApiKeyStdin {
		return false
	}
	if !isInteractive() {
		return false
	}
	fmt.Print("  Write BLOCKS_API_KEY to project .env? (Y/n): ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return true
	}
	ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if ans == "" {
		return true
	}
	return ans != "n" && ans != "no"
}
