package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/pubnub/blocks-sdk/cli/internal/schema"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"
)

var publishApiKey string
var publishApiKeyStdin bool
var publishListing string
var publishBillingMode string
var publishPrice string
var publishPricePerTask string
var publishPricePerMinute string
var publishFreeUnits int
var publishFreeTasks int
var publishFreeMinutes int
var publishAcceptTerms bool
var publishOrgName string

func init() {
	rootCmd.AddCommand(publishCmd)
	publishCmd.Flags().StringVar(&publishApiKey, "api-key", "", "Use a pre-obtained API key")
	publishCmd.Flags().BoolVar(&publishApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	publishCmd.Flags().StringVar(&publishListing, "listing", "", "Visibility: public or private")
	publishCmd.Flags().StringVar(&publishBillingMode, "billing-mode", "", "Billing mode: free or paid (required)")
	publishCmd.Flags().StringVar(&publishPrice, "price", "", "Price in USD, decimal string (auto-mapped to per-task or per-minute)")
	publishCmd.Flags().StringVar(&publishPricePerTask, "price-per-task", "", "Per-task price in USD, decimal string (dual-kind agents)")
	publishCmd.Flags().StringVar(&publishPricePerMinute, "price-per-minute", "", "Per-minute price in USD, decimal string (dual-kind agents)")
	publishCmd.Flags().IntVar(&publishFreeUnits, "free-units", 0, "Free trial tasks or minutes per consumer organization, auto-detected from taskKinds")
	publishCmd.Flags().IntVar(&publishFreeTasks, "free-tasks", 0, "Free trial task runs per consumer organization")
	publishCmd.Flags().IntVar(&publishFreeMinutes, "free-minutes", 0, "Free trial minutes per consumer organization")
	publishCmd.Flags().BoolVar(&publishAcceptTerms, "accept-terms", false, "Accept legal attestations non-interactively")
	publishCmd.Flags().StringVar(&publishOrgName, "org-name", "", "Set organization name (prompted on first publish)")
}

var publishCmd = &cobra.Command{
	Use:   "publish [path]",
	Short: "Publish an agent to the Blocks Network registry",
	Long:  "Publish the agent card to the registry. Requires prior authentication via 'blocks login' or --api-key.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPublish(cmd, args)
	},
}

func runPublish(cmd *cobra.Command, args []string) error {
	backendURL := resolveBackendURL()

	apiKey, err := resolvePublishApiKey()
	if err != nil {
		return err
	}

	cardPath := "agent-card.json"
	if len(args) > 0 {
		cardPath = args[0]
	}
	if !filepath.IsAbs(cardPath) {
		cardPath = filepath.Join(mustCwd(), cardPath)
	}

	result := schema.Validate(cardPath)
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  [FAIL] %s\n", e)
		}
		return fmt.Errorf("fix validation errors in %s before publishing", cardPath)
	}
	for _, s := range result.Successes {
		fmt.Printf("  [OK] %s\n", s)
	}

	card := result.Card

	identity, _ := card["identity"].(map[string]interface{})
	agentName, _ := identity["agentName"].(string)
	if agentName == "" {
		return fmt.Errorf("agent-card.json must contain identity.agentName")
	}

	envelope := map[string]interface{}{
		"agentName":                agentName,
		"card":                     card,
		"cliVersion":               Version,
		"protocolVersions":         []string{registry.ProtocolVersion},
		"preferredProtocolVersion": registry.ProtocolVersion,
	}

	capabilities, _ := card["capabilities"].(map[string]interface{})
	taskKindsRaw, _ := capabilities["taskKinds"].([]interface{})
	isStreaming := false
	isRequest := false
	for _, k := range taskKindsRaw {
		if k == "pipe" {
			isStreaming = true
		}
		if k == "request" {
			isRequest = true
		}
	}
	if !isStreaming && !isRequest {
		isRequest = true
	}

	interactive := isInteractive()
	if interactive && publishNeedsInteractivePrompt(cmd) {
		printPublishIntro(agentName)
	}

	// Org name prompt: on first agent publish, let the user set their org name.
	pubCtx := registry.FetchPublishContext(backendURL, apiKey)
	orgNameFlags := registry.OrgNameFlags{NonInteractive: !interactive}
	if cmd.Flags().Changed("org-name") {
		orgNameFlags.OrgName = &publishOrgName
	}
	chosenOrgName, err := registry.PromptOrgName(pubCtx, orgNameFlags, nil)
	if err != nil {
		return err
	}

	flags := registry.PromotionFlags{
		AcceptTerms:    publishAcceptTerms,
		NonInteractive: !interactive,
	}
	if cmd.Flags().Changed("listing") {
		flags.Listing = &publishListing
	}
	if cmd.Flags().Changed("billing-mode") {
		flags.BillingMode = &publishBillingMode
	}
	if cmd.Flags().Changed("price") {
		if publishPrice == "" {
			return fmt.Errorf("--price requires a non-empty decimal value")
		}
		flags.Price = &publishPrice
	}
	if cmd.Flags().Changed("price-per-task") {
		if publishPricePerTask == "" {
			return fmt.Errorf("--price-per-task requires a non-empty decimal value")
		}
		flags.PricePerTask = &publishPricePerTask
	}
	if cmd.Flags().Changed("price-per-minute") {
		if publishPricePerMinute == "" {
			return fmt.Errorf("--price-per-minute requires a non-empty decimal value")
		}
		flags.PricePerMinute = &publishPricePerMinute
	}
	if cmd.Flags().Changed("free-units") {
		flags.FreeUnits = &publishFreeUnits
	}
	if cmd.Flags().Changed("free-tasks") {
		flags.FreeTasks = &publishFreeTasks
	}
	if cmd.Flags().Changed("free-minutes") {
		flags.FreeMinutes = &publishFreeMinutes
	}

	limits := registry.FetchPricingLimits(backendURL)

	promInput, err := registry.CollectPromotionInput(isStreaming, isRequest, flags, limits, nil)
	if err != nil {
		return err
	}

	envelope["listing"] = promInput.Listing
	envelope["billingMode"] = promInput.BillingMode
	if promInput.TcAcceptedAt != "" {
		envelope["tcAcceptedAt"] = promInput.TcAcceptedAt
	}
	if promInput.PricePerTask != nil {
		envelope["pricePerTask"] = *promInput.PricePerTask
	}
	if promInput.PricePerMinute != nil {
		envelope["pricePerMinute"] = *promInput.PricePerMinute
	}
	if promInput.FreeTasksPerConsumer != nil {
		envelope["freeTasksPerConsumer"] = *promInput.FreeTasksPerConsumer
	}
	if promInput.FreeMinutesPerConsumer != nil {
		envelope["freeMinutesPerConsumer"] = *promInput.FreeMinutesPerConsumer
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	if backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}
	registryURL := backendURL + "/api/v1/registry/agents"

	// Apply org name update right before publishing (after all prompts succeed).
	// If --org-name was explicitly provided, treat conflicts as hard errors (no re-prompt).
	orgNameInteractive := interactive && !cmd.Flags().Changed("org-name")
	if chosenOrgName != "" && pubCtx != nil {
		if err := applyOrgNameUpdate(backendURL, apiKey, pubCtx, chosenOrgName, orgNameInteractive); err != nil {
			return err
		}
	}

	printPublishSummary(agentName, promInput)

	req, err := http.NewRequest("POST", registryURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Blocks-Protocol-Version", registry.ProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 {
		if publishApiKey != "" {
			return fmt.Errorf("authentication failed — the provided --api-key was rejected; replace it with a valid key and retry")
		}
		if publishApiKeyStdin {
			return fmt.Errorf("authentication failed — the API key provided via --api-key-stdin was rejected; replace it with a valid key and retry")
		}
		return fmt.Errorf("authentication failed — run 'blocks login' to re-authenticate, then retry 'blocks publish'")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp map[string]interface{}
		if json.Unmarshal(respBody, &errResp) == nil {
			return publishFailedError(agentName, promInput, errResp)
		}
		return fmt.Errorf("publish failed: HTTP %d", resp.StatusCode)
	}

	agentURL := publishedAgentURL(respBody, agentName, interactive)
	printPublishSuccess(agentName, promInput, agentURL)

	if agentURL != "" && interactive {
		_ = openBrowser(agentURL)
	}
	return nil
}

func resolvePublishApiKey() (string, error) {
	if publishApiKey != "" {
		return publishApiKey, nil
	}
	if publishApiKeyStdin {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return "", fmt.Errorf("--api-key-stdin: no input received on stdin")
		}
		key := scanner.Text()
		if key == "" {
			return "", fmt.Errorf("--api-key-stdin: empty API key received")
		}
		return key, nil
	}
	if envKey := os.Getenv("BLOCKS_API_KEY"); envKey != "" {
		return envKey, nil
	}
	creds, err := auth.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("not authenticated — run 'blocks login' first, or provide --api-key")
		}
		return "", fmt.Errorf("failed to load credentials: %w", err)
	}
	if creds.ApiKey == "" {
		return "", fmt.Errorf("not authenticated — run 'blocks login' first, or provide --api-key")
	}
	if creds.IsExpired() {
		return "", fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate")
	}
	return creds.ApiKey, nil
}

func publishFailedError(agentName string, input registry.PromotionInput, payload map[string]interface{}) error {
	if publishErrorCode(payload) == "BillingModeInvalid" && input.BillingMode == "free" {
		return fmt.Errorf("publish failed: %s", existingPriceFreePublishMessage(agentName))
	}
	if msg := publishErrorMessage(payload); msg != "" {
		return fmt.Errorf("publish failed: %s", msg)
	}
	if code := publishErrorCode(payload); code != "" {
		return fmt.Errorf("publish failed: %s", code)
	}
	return fmt.Errorf("publish failed")
}

func publishErrorMessage(payload map[string]interface{}) string {
	if msg := stringField(payload, "error", "message"); msg != "" {
		return msg
	}
	if errorPayload, ok := payload["error"].(map[string]interface{}); ok {
		return stringField(errorPayload, "message", "error")
	}
	return ""
}

func publishErrorCode(payload map[string]interface{}) string {
	if code := stringField(payload, "code"); code != "" {
		return code
	}
	if dataPayload, ok := payload["data"].(map[string]interface{}); ok {
		if code := stringField(dataPayload, "code"); code != "" {
			return code
		}
	}
	if errorPayload, ok := payload["error"].(map[string]interface{}); ok {
		if code := stringField(errorPayload, "code"); code != "" {
			return code
		}
		if dataPayload, ok := errorPayload["data"].(map[string]interface{}); ok {
			return stringField(dataPayload, "code")
		}
	}
	return ""
}

func existingPriceFreePublishMessage(agentName string) string {
	agentLabel := "This agent"
	if strings.TrimSpace(agentName) != "" {
		agentLabel = fmt.Sprintf("Agent %s", agentName)
	}
	return fmt.Sprintf("%s is already configured as a Paid agent. Please delete via the Blocks portal before re-publishing as a Free agent.", agentLabel)
}

const blocksWordmark = ` ____  _     ___   ____ _  __ ____
| __ )| |   / _ \ / ___| |/ // ___|
|  _ \| |  | | | | |   | ' / \___ \
| |_) | |__| |_| | |___| . \  ___) |
|____/|_____\___/ \____|_|\_\|____/`

const blocksSuccessLogoWidth = 51
const ansiBold = "\x1b[1m"
const ansiReset = "\x1b[0m"
const agentAppRoute = "/agents"

const blocksSuccessLogoTop = `                      ####
                  #############
              #######      ########
          ########             #######
       #######                    ########
     ######           ####            ######
    ####              #####             #####
    ###              ######              ####
    ###             #########
    ###          ######   ######
    ################        #################`

const blocksSuccessLogoBottom = `    ################        #################
                 ######   ######         ####
                   ##########            ####
    ###              #######             ####
    ####              #####             #####
     #####            ####            ######
       #######                     #######
          ########             #######
              #######      ########
                 ##############
                     ######`

func publishNeedsInteractivePrompt(cmd *cobra.Command) bool {
	if !cmd.Flags().Changed("listing") || !cmd.Flags().Changed("billing-mode") {
		return true
	}
	if publishBillingMode != "paid" {
		return false
	}
	if publishAcceptTerms {
		return false
	}
	return true
}

func printPublishIntro(agentName string) {
	fmt.Println(blocksWordmark)
	fmt.Println()
	fmt.Println("Publish an Agent")
	if agentName != "" {
		fmt.Println()
		fmt.Printf("Agent: %s\n", agentName)
	}
	fmt.Println()
	fmt.Println("We'll collect the details we need, then publish your agent.")
}

func printPublishSummary(agentName string, input registry.PromotionInput) {
	fmt.Println()
	fmt.Printf("Publishing %s...\n", agentName)
	fmt.Printf("Visibility: %s\n", displayMode(input.Listing))
	fmt.Printf("Billing: %s\n", displayMode(input.BillingMode))
	if input.BillingMode == "paid" {
		if moneyGtZero(input.PricePerTask) {
			fmt.Printf("Price per task: %s\n", formatUSD(*input.PricePerTask))
		}
		if moneyGtZero(input.PricePerMinute) {
			fmt.Printf("Price per minute: %s\n", formatUSD(*input.PricePerMinute))
		}
		if input.FreeTasksPerConsumer != nil && *input.FreeTasksPerConsumer > 0 {
			fmt.Printf("Free trial task runs per consumer organization: %d\n", *input.FreeTasksPerConsumer)
		}
		if input.FreeMinutesPerConsumer != nil && *input.FreeMinutesPerConsumer > 0 {
			fmt.Printf("Free trial minutes per consumer organization: %d\n", *input.FreeMinutesPerConsumer)
		}
	}
}

func printPublishSuccess(agentName string, input registry.PromotionInput, agentURL string) {
	fmt.Println()
	fmt.Println(blocksSuccessLogo(agentName))
	fmt.Println()
	fmt.Printf("Congratulations! %s is published to the Blocks Network.\n", boldText(agentName))
	fmt.Printf("Visibility: %s\n", boldText(displayMode(input.Listing)))
	fmt.Printf("Billing: %s\n", boldText(displayMode(input.BillingMode)))
	if agentURL != "" {
		fmt.Printf("View: %s\n", agentURL)
	}
	for _, line := range publishNextSteps(input) {
		fmt.Println(line)
	}
}

func blocksSuccessLogo(agentName string) string {
	message := "Agent published!"
	if strings.TrimSpace(agentName) != "" {
		message = boldText(agentName)
	}
	return blocksSuccessLogoTop + "\n" + centerText(message, blocksSuccessLogoWidth) + "\n" + blocksSuccessLogoBottom
}

func centerText(text string, width int) string {
	visibleLen := visibleRuneCount(text)
	if visibleLen >= width {
		return text
	}
	return strings.Repeat(" ", (width-visibleLen)/2) + text
}

func boldText(text string) string {
	return ansiBold + text + ansiReset
}

func visibleRuneCount(text string) int {
	count := 0
	inEscape := false
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		if r == '\x1b' {
			inEscape = true
			continue
		}
		count++
	}
	return count
}

func publishNextSteps(input registry.PromotionInput) []string {
	if input.Listing == "private" {
		if input.BillingMode == "paid" {
			return []string{
				"Next: invite organizations before they can use this agent.",
				"Next: keep your agent running so it can accept paid tasks.",
			}
		}
		return []string{"Next: invite organizations before they can use this agent."}
	}
	if input.BillingMode == "paid" {
		return []string{"Next: keep your agent running so it can accept paid tasks."}
	}
	return []string{"Next: keep your agent running so consumers can use it."}
}

func publishedAgentURL(respBody []byte, agentName string, allowNetworkFallback bool) string {
	var payload map[string]interface{}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return agentAppURL(agentName, allowNetworkFallback)
	}
	if url := urlField(payload, "agentUrl", "agentURL", "url"); url != "" {
		return url
	}
	if agentPayload, ok := payload["agent"].(map[string]interface{}); ok {
		if url := urlField(agentPayload, "agentUrl", "agentURL", "url"); url != "" {
			return url
		}
	}
	return agentAppURL(agentName, allowNetworkFallback)
}

// agentAppURL builds the dashboard URL for an agent. Resolution order:
// BLOCKS_APP_BASE_URL env var → CDM api.baseUrl (only when allowNetworkFallback
// is true). In production CDM returns the app origin (https://app.blocks.ai);
// in local dev where frontend and backend are split, set BLOCKS_APP_BASE_URL
// to the frontend origin. The CDM fallback is skipped in non-interactive mode
// to avoid a potential 21s timeout stall in CI/offline environments.
func agentAppURL(agentName string, allowNetworkFallback bool) string {
	if strings.TrimSpace(agentName) == "" {
		return ""
	}
	baseURL := resolveAppBaseURL()
	if baseURL == "" && allowNetworkFallback {
		if cfg, err := cdm.Get(); err == nil && cfg.Api.BaseURL != "" {
			baseURL = cfg.Api.BaseURL
		}
	}
	if baseURL == "" {
		return ""
	}
	agentURL, err := url.JoinPath(baseURL, agentAppRoute, agentName)
	if err != nil {
		return ""
	}
	return safeHTTPURL(agentURL)
}

func urlField(payload map[string]interface{}, keys ...string) string {
	return safeHTTPURL(stringField(payload, keys...))
}

func safeHTTPURL(raw string) string {
	cleaned := stripControlChars(strings.TrimSpace(raw))
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func stripControlChars(raw string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
}

func stringField(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func displayMode(value string) string {
	switch value {
	case "public":
		return "Public"
	case "private":
		return "Private"
	case "free":
		return "Free"
	case "paid":
		return "Paid"
	default:
		return value
	}
}

func formatUSD(raw string) string {
	v, err := decimal.NewFromString(strings.TrimPrefix(strings.TrimSpace(raw), "$"))
	if err != nil {
		return "$" + raw
	}
	s := strings.TrimRight(strings.TrimRight(v.StringFixed(6), "0"), ".")
	if s == "" {
		s = "0"
	}
	return "$" + s
}

func moneyGtZero(raw *string) bool {
	if raw == nil {
		return false
	}
	v, err := decimal.NewFromString(strings.TrimPrefix(strings.TrimSpace(*raw), "$"))
	return err == nil && v.Sign() > 0
}

func applyOrgNameUpdate(backendURL, apiKey string, pubCtx *registry.PublishContext, name string, interactive bool) error {
	chosenName := name
	for {
		updateErr := registry.UpdateOrgName(backendURL, apiKey, pubCtx.OrgID, chosenName)
		if updateErr == nil {
			return nil
		}
		taken, ok := updateErr.(*registry.OrgNameTakenError)
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: could not update organization name: %v\n", updateErr)
			return nil
		}
		if !interactive {
			return fmt.Errorf("organization name %q is already taken", taken.Name)
		}
		fmt.Printf("\n  Name %q is already taken. Please choose a different name.\n", taken.Name)
		chosenName = retryOrgNamePrompt(pubCtx.OrgName)
		if chosenName == "" {
			return nil
		}
	}
}

func retryOrgNamePrompt(defaultName string) string {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("\nOrganization name [%s] (? for help): ", defaultName)
		if !scanner.Scan() {
			return ""
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(registry.HelpOrgName)
			continue
		}
		return text
	}
}
