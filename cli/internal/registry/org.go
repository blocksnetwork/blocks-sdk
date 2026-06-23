package registry

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
)

// HelpOrgNameText is the help text shown when the user types '?' at the org
// name prompt. It reads the active product name at call time so enterprise
// deployments brand it correctly (the org-name prompt is skipped in enterprise,
// so this only renders on Network — branding it keeps it consistent if shown).
func HelpOrgNameText() string {
	return "  Your organization name appears on " + branding.ProductName() + " alongside your agents.\n" +
		"  It must be globally unique (like agent names). Leave blank to keep the current\n" +
		"  name. You can change it later from the dashboard."
}

// PublishContext is the response from GET /api/v1/registry/publish-context.
type PublishContext struct {
	OrgID      string `json:"orgId"`
	OrgName    string `json:"orgName"`
	AgentCount int    `json:"agentCount"`
}

// FetchPublishContext fetches the org's publish context (name + agent count).
// Returns nil on any error (endpoint unavailable, auth failure, etc.) so
// the caller can skip the org-name prompt gracefully.
func FetchPublishContext(backendURL, apiKey string) *PublishContext {
	if backendURL == "" || apiKey == "" {
		return nil
	}

	url := backendURL + "/api/v1/registry/publish-context"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Blocks-Protocol-Version", ProtocolVersion)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var ctx PublishContext
	if err := json.NewDecoder(resp.Body).Decode(&ctx); err != nil {
		return nil
	}
	return &ctx
}

// OrgNameTakenError is returned when the chosen org name is already in use.
type OrgNameTakenError struct {
	Name string
}

func (e *OrgNameTakenError) Error() string {
	return fmt.Sprintf("organization name %q is already taken", e.Name)
}

// UpdateOrgName sends a PATCH to update the org name.
// Returns OrgNameTakenError if the backend rejects with 409 (name conflict).
func UpdateOrgName(backendURL, apiKey, orgID, newName string) error {
	if backendURL == "" || apiKey == "" {
		return fmt.Errorf("backend URL and API key required")
	}

	resp, err := sendOrgNamePatch(backendURL, apiKey, orgID, newName)
	if err != nil {
		return fmt.Errorf("failed to update organization name: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return &OrgNameTakenError{Name: newName}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return parseOrgUpdateError(resp)
}

func sendOrgNamePatch(backendURL, apiKey, orgID, newName string) (*http.Response, error) {
	payload, _ := json.Marshal(map[string]string{"name": newName})
	url := fmt.Sprintf("%s/api/v1/orgs/%s", backendURL, orgID)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Blocks-Protocol-Version", ProtocolVersion)
	return httpClient.Do(req)
}

func parseOrgUpdateError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("failed to update organization name: %s", errResp.Error)
	}
	return fmt.Errorf("failed to update organization name: HTTP %d", resp.StatusCode)
}

// OrgNameFlags holds CLI flag values for the org name prompt.
type OrgNameFlags struct {
	OrgName        *string
	NonInteractive bool
}

// PromptOrgName prompts for the organization name on first publish.
// Returns the chosen name (possibly empty string meaning "keep current").
// The interactive prompt only appears when agentCount == 0 (first agent for this org),
// but an explicit --org-name flag is always honored regardless of agent count.
func PromptOrgName(ctx *PublishContext, flags OrgNameFlags, scanner *bufio.Scanner) (string, error) {
	if ctx == nil {
		return "", nil
	}
	if flags.OrgName != nil {
		return *flags.OrgName, nil
	}
	if ctx.AgentCount > 0 {
		return "", nil
	}
	if flags.NonInteractive {
		return "", nil
	}

	if scanner == nil {
		scanner = bufio.NewScanner(os.Stdin)
	}

	printOrgNameIntro()
	return readOrgName(scanner, ctx.OrgName)
}

func printOrgNameIntro() {
	fmt.Println()
	fmt.Println(promptAnsiBold + "Organization name" + promptAnsiReset)
	fmt.Println()
	fmt.Println("Your organization name is shown publicly alongside your agents.")
	fmt.Println("Names must be globally unique.")
}

func readOrgName(scanner *bufio.Scanner, defaultName string) (string, error) {
	for {
		fmt.Printf("\nOrganization name [%s] (? for help): ", defaultName)

		if !scanner.Scan() {
			return "", nil
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(HelpOrgNameText())
			continue
		}
		return text, nil
	}
}
