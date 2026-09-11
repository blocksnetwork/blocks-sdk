package cmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard [agent-name]",
	Short: "Open the agent dashboard in a browser",
	Long: `Open the dashboard for an agent. If no agent name is
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

	// Built, and refused, before anything reaches the operating system's URL handler.
	dashURL, err := dashboardURL(baseURL, agentName)
	if err != nil {
		return err
	}

	fmt.Println("Opening dashboard...")
	if err := openBrowser(dashURL); err != nil {
		// Sanitized on the way to the terminal even though it is a URL this command
		// just validated: the rule below constrains the scheme and the authority, not
		// every byte of a path prefix, and a path is where a control sequence would
		// hide. The opener still receives the URL itself.
		fmt.Printf("Could not open browser. Visit:\n  %s\n", termsafe.Text(dashURL))
	}
	return nil
}

// dashboardURL builds the URL `blocks dashboard` hands to the operating system's URL
// handler, or refuses to build one.
//
// Every input is attacker-influenceable and the sink is unusually forgiving. The
// browser opener does not fetch anything itself: it asks the OS to dispatch a URL to
// whichever handler is registered for its scheme, so a value that is not a web address
// is still acted on — `file:` reads a local path, a `\\host\share` UNC path reaches an
// SMB server and leaks credentials on Windows, and a custom protocol starts whatever
// application claims it. BLOCKS_APP_BASE_URL and BLOCKS_DASHBOARD_URL are both imported
// from a project .env, which arrives with a cloned repository; a profile's
// DashboardBaseURL was supplied by a deployment's own discovery payload; and the
// fallback origin can come from a CDM payload. None of them is evidence of a scheme.
//
// The rule applied is deploymentURL's, unchanged: https, or http for loopback only, an
// authority that is a host and nothing else — not user@host — a dialable port, and no
// query or fragment. It is the same question `blocks login` asks of an instance
// argument and internal/deploy asks of a webapp URL, and a third spelling of one rule
// is a third answer to it. Keeping the two overrides on the .env allowlist and
// validating here is deliberate: a self-hosted deployment has to be able to name its
// own dashboard, and what makes a value dangerous is not where it came from but that
// nothing checked it before the OS was asked to act on it.
//
// The agent name becomes a path segment, so it is held to the registry's own pattern —
// wizard.ValidateAgentName, the single definition, as `blocks invite` uses — and then
// escaped. The pattern already excludes every character that could end the segment, so
// the escape is redundant today; it stays because "this is one path segment" is the
// property the URL needs, and it must not depend on the pattern remaining this strict.
func dashboardURL(baseURL, agentName string) (string, error) {
	origin := deploymentURL(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if origin == "" {
		return "", fmt.Errorf("not opening %q — %s; set %s to your deployment's dashboard origin",
			termsafe.Text(baseURL), instanceURLForms, blocksAppBaseURLEnv)
	}
	if agentName == "" {
		return origin, nil
	}
	if err := wizard.ValidateAgentName(agentName); err != nil {
		return "", fmt.Errorf("%q does not name an agent: %w", termsafe.Text(agentName), err)
	}
	return origin + "/agents/" + url.PathEscape(agentName), nil
}

// resolveDashboardURL picks the dashboard origin. Explicit dashboard overrides
// (BLOCKS_APP_BASE_URL / BLOCKS_DASHBOARD_URL / active profile DashboardBaseURL)
// win via resolveAppBaseURL; otherwise fall back to the deployment origin from
// resolveBackendURL (BLOCKS_BACKEND_URL → active profile BaseURL → ldflag
// default → CDM) so the dashboard opens on the active profile's deployment
// rather than always stock https://app.blocks.ai.
//
// It only picks a tier. Whether the value it picked is a URL this CLI may hand to the
// operating system is dashboardURL's question, asked of whatever comes out of here, so
// that no tier can be added later that skips the check.
func resolveDashboardURL() (string, error) {
	if v := resolveAppBaseURL(); v != "" {
		return v, nil
	}
	if v := resolveBackendURL(); v != "" {
		return v, nil
	}
	return "", dashboardURLUnresolvableError()
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
