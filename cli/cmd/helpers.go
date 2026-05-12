package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
)

// Build-time defaults injected via ldflags.
var defaultBackendURL = ""
var defaultCliClientID = ""

const (
	blocksAppBaseURLEnv   = "BLOCKS_APP_BASE_URL"
	blocksDashboardURLEnv = "BLOCKS_DASHBOARD_URL"
)

func resolveBackendURL() string {
	if v := os.Getenv("BLOCKS_BACKEND_URL"); v != "" {
		return v
	}
	if defaultBackendURL != "" {
		return defaultBackendURL
	}
	cfg, err := cdm.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch remote config: %v\n", err)
		return ""
	}
	if cfg.Api.BaseURL != "" {
		return cfg.Api.BaseURL
	}
	return ""
}

func resolveAppBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(blocksAppBaseURLEnv)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(blocksDashboardURLEnv)); v != "" {
		return v
	}
	return ""
}

func resolveClientID() string {
	if v := os.Getenv("BLOCKS_CLI_CLIENT_ID"); v != "" {
		return v
	}
	if defaultCliClientID != "" {
		return defaultCliClientID
	}
	cfg, err := cdm.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch remote config: %v\n", err)
		return ""
	}
	if cfg.Api.ClientID != "" {
		return cfg.Api.ClientID
	}
	return ""
}

// openBrowser opens the given URL in the platform's default browser.
func openBrowser(rawURL string) error {
	return auth.OpenBrowser(rawURL)
}

// loadCredentials loads credentials and returns the API key or an error.
// API keys are long-lived and do not require refresh.
func loadCredentials() (string, error) {
	creds, err := auth.Load()
	if err != nil {
		return "", fmt.Errorf("not logged in — run 'blocks login' first")
	}

	if creds.IsExpired() {
		return "", fmt.Errorf("API key has expired — run 'blocks login' to create a new one")
	}

	return creds.ApiKey, nil
}
