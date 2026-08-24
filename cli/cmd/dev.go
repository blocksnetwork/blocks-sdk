package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/devserver"
	"github.com/spf13/cobra"
)

var devPort int

func init() {
	rootCmd.AddCommand(devCmd)
	devCmd.Flags().IntVar(&devPort, "port", 4242, "Port for the local dev server (auto-increments up to 5 ports if busy)")
}

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Start a local dev server with hot reload",
	Long: `Start a loopback-bound HTTP server that serves the web/ directory with hot
reload. Injects a small dev script (window.__BLOCKS_EMBED_DEV__) that the
embedded-auth widget reads to point at the local backend during development.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runDev(ctx)
	},
}

func runDev(ctx context.Context) error {
	// Load and validate blocks.config.json from the current directory.
	// `blocks dev` does NOT require `blocks login` — the dev server is
	// a static-file host with hot reload, the embed-auth popup it serves
	// authenticates the end user directly against the backend, and the
	// CLI's API key never reaches the dev server.
	cfgPath := filepath.Join(mustCwd(), "blocks.config.json")
	blocksCfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("blocks.config.json not found or invalid — run 'blocks init <name> --mode webapp --agent <agent>' first: %w", err)
	}
	if err := config.Validate(blocksCfg); err != nil {
		return fmt.Errorf("blocks.config.json: %w", err)
	}

	// Resolve backend URL.
	backendURL := resolveBackendURL()
	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set (or configure via CDM)")
	}

	// Build and run server.
	srv := devserver.New(devserver.Config{
		Port:           devPort,
		BackendBaseURL: backendURL,
		Agents:         blocksCfg.Agents,
	})

	return srv.Run(ctx)
}
