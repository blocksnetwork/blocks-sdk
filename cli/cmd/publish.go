package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
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
}

var publishCmd = &cobra.Command{
	Use:   "publish [path]",
	Short: "Publish an agent to the Blocks Network registry",
	Long:  "Authenticates (if needed) and publishes the agent card to the registry.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runPublish(ctx, cmd, args)
	},
}

func runPublish(ctx context.Context, cmd *cobra.Command, args []string) error {
	backendURL := resolveBackendURL()
	clientID := resolveClientID()

	apiKey, err := auth.EnsureCredentials(ctx, backendURL, clientID, publishApiKey, publishApiKeyStdin)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	auth.InjectEnv("BLOCKS_API_KEY", apiKey)

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

	promInput, err := registry.CollectPromotionInput(isStreaming, isRequest, flags, nil)
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
	req, err := http.NewRequest("POST", registryURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Blocks-Protocol-Version", registry.ProtocolVersion)

	printPublishSummary(agentName, promInput)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication failed - try running 'blocks publish' again")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp map[string]interface{}
		if json.Unmarshal(respBody, &errResp) == nil {
			return publishFailedError(agentName, promInput, errResp)
		}
		return fmt.Errorf("publish failed: HTTP %d", resp.StatusCode)
	}

	printPublishSuccess(agentName, promInput, publishedAgentURL(respBody, agentName))
	return nil
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
	if publishAcceptTerms {
		return false
	}
	if !cmd.Flags().Changed("listing") || !cmd.Flags().Changed("billing-mode") {
		return true
	}
	if publishBillingMode != "paid" {
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

func publishedAgentURL(respBody []byte, agentName string) string {
	var payload map[string]interface{}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return agentAppURL(agentName)
	}
	if url := urlField(payload, "agentUrl", "agentURL", "url"); url != "" {
		return url
	}
	if agentPayload, ok := payload["agent"].(map[string]interface{}); ok {
		if url := urlField(agentPayload, "agentUrl", "agentURL", "url"); url != "" {
			return url
		}
	}
	return agentAppURL(agentName)
}

func agentAppURL(agentName string) string {
	baseURL := resolveAppBaseURL()
	if baseURL == "" || strings.TrimSpace(agentName) == "" {
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
