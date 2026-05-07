package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard [agent-name]",
	Short: "Open the agent dashboard in a browser",
	Long: `Open the Blocks Network dashboard for an agent. If no agent name is
provided, the name is read from agent-card.json in the current directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDashboard,
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
}

func runDashboard(cmd *cobra.Command, args []string) error {
	// Resolve agent name: explicit arg > agent-card.json in cwd
	agentName := ""
	if len(args) > 0 {
		agentName = args[0]
	} else {
		agentName = agentNameFromCard()
	}

	// Resolve dashboard base URL
	baseURL, err := resolveDashboardURL()
	if err != nil {
		return err
	}

	// Build URL path
	dashURL := baseURL
	if agentName != "" {
		dashURL = baseURL + "/agents/" + agentName
	}

	fmt.Println("Opening dashboard...")
	if err := openBrowser(dashURL); err != nil {
		fmt.Printf("Could not open browser. Visit:\n  %s\n", dashURL)
	}
	return nil
}

func resolveDashboardURL() (string, error) {
	if v := resolveAppBaseURL(); v != "" {
		return v, nil
	}
	if cfg, err := cdm.Get(); err == nil && cfg.Api.BaseURL != "" {
		return cfg.Api.BaseURL, nil
	}
	return "", fmt.Errorf("could not resolve dashboard URL - set BLOCKS_APP_BASE_URL or BLOCKS_DASHBOARD_URL, or ensure CDM config is reachable")
}

// agentNameFromCard reads the "name" field from agent-card.json in the cwd.
// Returns "" if the file doesn't exist or can't be parsed.
func agentNameFromCard() string {
	cardPath := filepath.Join(mustCwd(), "agent-card.json")
	data, err := os.ReadFile(cardPath)
	if err != nil {
		return ""
	}
	var card map[string]interface{}
	if err := json.Unmarshal(data, &card); err != nil {
		return ""
	}
	identity, ok := card["identity"].(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := identity["agentName"].(string)
	return name
}
