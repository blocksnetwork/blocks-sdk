package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
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
	Short: "Publish an agent to the registry",
	Long:  "Publish the agent card to the registry. Requires prior authentication via 'blocks login' or --api-key.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runPublish(ctx, cmd, args)
	},
}

func runPublish(ctx context.Context, cmd *cobra.Command, args []string) error {
	prep, err := preparePublish(args, publishApiKey, publishApiKeyStdin)
	if err != nil {
		return err
	}

	interactive := isInteractive()
	if interactive && !prep.enterprise && publishNeedsInteractivePrompt(cmd) {
		printPublishIntro(prep.agentName)
	}

	if err := applyEnterpriseOrgPicker(prep, interactive, publishApiKey, publishApiKeyStdin); err != nil {
		return err
	}

	org, err := resolveOrgNameInput(cmd, prep, interactive, "org-name", publishOrgName)
	if err != nil {
		return err
	}

	flags, err := collectPromotionFlags(cmd, interactive, prep.enterprise)
	if err != nil {
		return err
	}

	limits := registry.FetchPricingLimits(prep.backendURL)

	promInput, err := registry.CollectPromotionInput(prep.isStreaming, prep.isRequest, flags, limits, nil)
	if err != nil {
		return err
	}

	return finalizePublish(ctx, prep, promInput, org, interactive, submitOptions{
		apiKeyFlag:  publishApiKey,
		apiKeyStdin: publishApiKeyStdin,
		commandName: "blocks publish",
	})
}

// applyEnterpriseOrgPicker prompts the user to choose the owning org when an
// enterprise account belongs to more than one, then swaps prep.apiKey to the
// chosen org's publish key so the rest of the flow targets it. No-op on
// Blocks Network, non-interactive sessions, when an API key was supplied
// explicitly, or for single-org users.
func applyEnterpriseOrgPicker(prep *publishPrep, interactive bool, apiKeyFlag string, apiKeyStdin bool) error {
	if !prep.enterprise || !interactive || apiKeyFlag != "" || apiKeyStdin {
		return nil
	}
	name, _, perr := profiles.Active()
	if perr != nil {
		return perr
	}
	orgs, oerr := fetchUserOrgs(prep.backendURL, prep.apiKey)
	if oerr != nil {
		return oerr
	}
	if len(orgs) <= 1 {
		return nil
	}
	chosen, cerr := promptOrgChoice(orgs)
	if cerr != nil {
		return cerr
	}
	key, kerr := resolveOrgPublishKey(prep.backendURL, prep.apiKey, name, chosen.Id, chosen.Name)
	if kerr != nil {
		return kerr
	}
	prep.apiKey = key
	return nil
}

// collectPromotionFlags maps the `blocks publish` flag variables into
// registry.PromotionFlags. A nil pointer means the flag was not set, so the
// interactive prompt path handles it. Price flags are rejected when present
// but empty, mirroring the original inline validation. In enterprise mode,
// billing-mode is forced to "free" (billing is globally off; pricing/T&C
// prompts are suppressed) unless --billing-mode was explicitly passed.
func collectPromotionFlags(cmd *cobra.Command, interactive, enterprise bool) (registry.PromotionFlags, error) {
	flags := registry.PromotionFlags{
		AcceptTerms:    publishAcceptTerms,
		NonInteractive: !interactive,
	}
	if ov := enterpriseBillingOverride(enterprise); ov != nil && !cmd.Flags().Changed("billing-mode") {
		flags.BillingMode = ov
	}
	if cmd.Flags().Changed("listing") {
		flags.Listing = &publishListing
	}
	if cmd.Flags().Changed("billing-mode") {
		flags.BillingMode = &publishBillingMode
	}
	for _, pf := range []struct {
		name  string
		value string
		dest  **string
	}{
		{"price", publishPrice, &flags.Price},
		{"price-per-task", publishPricePerTask, &flags.PricePerTask},
		{"price-per-minute", publishPricePerMinute, &flags.PricePerMinute},
	} {
		if !cmd.Flags().Changed(pf.name) {
			continue
		}
		if pf.value == "" {
			return registry.PromotionFlags{}, fmt.Errorf("--%s requires a non-empty decimal value", pf.name)
		}
		v := pf.value
		*pf.dest = &v
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
	return flags, nil
}

// publishPrep holds the command-agnostic inputs shared by `blocks publish` and
// `blocks register` before promotion params (listing/billing) are decided.
// enterprise is sticky for the whole flow: it gates the org picker, the
// org-name prompt, and the billing-mode override.
type publishPrep struct {
	backendURL  string
	apiKey      string
	agentName   string
	envelope    map[string]interface{}
	isStreaming bool
	isRequest   bool
	enterprise  bool
}

// preparePublish resolves the backend URL and API key, validates the agent
// card, and assembles the base registry envelope. It is shared by `publish`
// and `register` so the two commands cannot drift on validation or payload
// shape; only the promotion params differ between them.
func preparePublish(args []string, apiKeyFlag string, apiKeyStdin bool) (*publishPrep, error) {
	backendURL := resolveBackendURL()

	apiKey, err := resolvePublishApiKey(apiKeyFlag, apiKeyStdin)
	if err != nil {
		return nil, err
	}

	enterprise := resolvePublishEnterprise(backendURL)

	cardPath := "agent-card.json"
	if len(args) > 0 {
		cardPath = args[0]
	}
	if !filepath.IsAbs(cardPath) {
		cardPath = filepath.Join(mustCwd(), cardPath)
	}

	// Rewrite the deprecated `skills` field to `tags` in-memory before
	// validation so customers still on the old card layout get a warning
	// + working publish, not a confusing schema rejection. The source
	// file is left untouched. Same shim is used by `blocks run` and
	// `blocks check`.
	result := validateCardWithLegacyShim(cardPath)
	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			fmt.Fprintf(os.Stderr, "  [FAIL] %s\n", e)
		}
		return nil, fmt.Errorf("fix validation errors in %s before publishing", cardPath)
	}
	for _, s := range result.Successes {
		fmt.Printf("  [OK] %s\n", s)
	}

	card := result.Card

	identity, _ := card["identity"].(map[string]interface{})
	agentName, _ := identity["agentName"].(string)
	if agentName == "" {
		return nil, fmt.Errorf("agent-card.json must contain identity.agentName")
	}

	envelope := map[string]interface{}{
		"agentName":                agentName,
		"card":                     card,
		"cliVersion":               Version,
		"protocolVersions":         []string{registry.ProtocolVersion},
		"preferredProtocolVersion": registry.ProtocolVersion,
	}

	isStreaming, isRequest := deriveTaskKinds(card)

	return &publishPrep{
		backendURL:  backendURL,
		apiKey:      apiKey,
		agentName:   agentName,
		envelope:    envelope,
		isStreaming: isStreaming,
		isRequest:   isRequest,
		enterprise:  enterprise,
	}, nil
}

// deriveTaskKinds reads capabilities.taskKinds off the card and reports whether
// the agent handles pipe (streaming) and/or request tasks. An agent with no
// recognized taskKinds defaults to request.
func deriveTaskKinds(card map[string]interface{}) (isStreaming, isRequest bool) {
	capabilities, _ := card["capabilities"].(map[string]interface{})
	taskKindsRaw, _ := capabilities["taskKinds"].([]interface{})
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
	return isStreaming, isRequest
}

// orgNameInput carries the resolved org-name prompt result through to the
// apply step in finalizePublish. A zero value (pubCtx nil) means the prompt
// was skipped (e.g. enterprise) and finalizePublish must not apply any update.
type orgNameInput struct {
	pubCtx      *registry.PublishContext
	chosen      string
	interactive bool // re-prompt on a name-taken conflict
}

// resolveOrgNameInput fetches publish context and runs the first-publish
// org-name prompt. flagName/flagValue describe the command's --org-name flag
// (its name is the same on both commands, but the bound variable differs).
// In enterprise mode the prompt is skipped — orgs are pre-seeded by the
// enterprise admin and publish must not rename them — and a zero-value
// orgNameInput is returned.
func resolveOrgNameInput(cmd *cobra.Command, prep *publishPrep, interactive bool, flagName, flagValue string) (orgNameInput, error) {
	if prep.enterprise {
		return orgNameInput{}, nil
	}
	pubCtx := registry.FetchPublishContext(prep.backendURL, prep.apiKey)
	orgNameFlags := registry.OrgNameFlags{NonInteractive: !interactive}
	if cmd.Flags().Changed(flagName) {
		v := flagValue
		orgNameFlags.OrgName = &v
	}
	chosen, err := registry.PromptOrgName(pubCtx, orgNameFlags, nil)
	if err != nil {
		return orgNameInput{}, err
	}
	// If --org-name was explicitly provided, treat conflicts as hard errors (no re-prompt).
	return orgNameInput{
		pubCtx:      pubCtx,
		chosen:      chosen,
		interactive: interactive && !cmd.Flags().Changed(flagName),
	}, nil
}

// submitOptions carries the per-command bits the shared finalize step needs:
// how the API key was supplied (for 401 messaging), the command name (for
// actionable error text), and whether to print the promote-to-public hint.
type submitOptions struct {
	apiKeyFlag  string
	apiKeyStdin bool
	commandName string
	promoteHint bool
}

// finalizePublish assigns promotion fields onto the envelope, applies any
// pending org-name update, POSTs to the registry, and renders the result.
// Shared by `publish` and `register`.
func finalizePublish(ctx context.Context, prep *publishPrep, promInput registry.PromotionInput, org orgNameInput, interactive bool, opts submitOptions) error {
	applyPromotionToEnvelope(prep.envelope, promInput)

	if prep.backendURL == "" {
		return fmt.Errorf("BLOCKS_BACKEND_URL must be set")
	}

	// Apply org name update right before publishing (after all prompts succeed).
	// Skipped in enterprise (the org-name prompt never ran there, so pubCtx is nil).
	if org.chosen != "" && org.pubCtx != nil {
		if err := applyOrgNameUpdate(prep.backendURL, prep.apiKey, org.pubCtx, org.chosen, org.interactive); err != nil {
			return err
		}
	}

	printPublishSummary(prep.agentName, promInput)

	// Use the shared blocksapi.Client so Authorization and Blocks-Protocol-Version
	// headers are attached automatically on every outbound Blocks-backend call.
	client := blocksapi.NewClient(prep.backendURL, prep.apiKey)
	var respPayload map[string]interface{}
	if err := client.DoJSON(ctx, "POST", "/api/v1/registry/agents", prep.envelope, &respPayload); err != nil {
		return submitPublishError(err, prep.agentName, promInput, opts)
	}

	respBody, _ := json.Marshal(respPayload)
	agentURL := publishedAgentURL(respBody, prep.agentName, interactive)
	printPublishSuccess(prep.agentName, promInput, agentURL)
	if opts.promoteHint {
		fmt.Println("To make this agent public or set pricing later, run `blocks publish`.")
	}

	if agentURL != "" && interactive {
		_ = openBrowser(agentURL)
	}
	return nil
}

// applyPromotionToEnvelope copies the validated promotion params onto the
// outgoing registry envelope, omitting optional fields that are unset.
func applyPromotionToEnvelope(envelope map[string]interface{}, promInput registry.PromotionInput) {
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
}

// submitPublishError maps a failed registry POST into an actionable error.
// 401 messaging depends on how the API key was supplied; other statuses fall
// through to the shared publish-failure formatter.
func submitPublishError(err error, agentName string, promInput registry.PromotionInput, opts submitOptions) error {
	apiErr, ok := err.(*blocksapi.APIError)
	if !ok {
		return fmt.Errorf("request failed: %w", err)
	}
	switch apiErr.StatusCode {
	case 401:
		if opts.apiKeyFlag != "" {
			return fmt.Errorf("authentication failed — the provided --api-key was rejected; replace it with a valid key and retry")
		}
		if opts.apiKeyStdin {
			return fmt.Errorf("authentication failed — the API key provided via --api-key-stdin was rejected; replace it with a valid key and retry")
		}
		return fmt.Errorf("authentication failed — run 'blocks login' to re-authenticate, then retry '%s'", opts.commandName)
	case 403:
		return fmt.Errorf("permission denied (HTTP 403) — check that your API key owns this agent")
	default:
		return publishFailedError(agentName, promInput, apiErrorToMap(apiErr), opts.commandName)
	}
}

// resolvePublishEnterprise decides whether this publish targets an enterprise
// deployment. A profile saved by a prior `blocks login <instanceUrl>` is
// authoritative when it already records enterprise. When no enterprise profile
// is present but a custom backend is targeted (BLOCKS_BACKEND_URL or a profile
// BaseURL — e.g. a scripted --api-key publish that never ran login), confirm via
// a lenient cli-config discovery so enterprise still suppresses the Network-only
// billing / org-name flow. A pure stock-Network target can't be enterprise, so
// it skips the round-trip; discovery errors and older backends (404) yield false
// and never block publish.
func resolvePublishEnterprise(backendURL string) bool {
	customBackend := os.Getenv("BLOCKS_BACKEND_URL") != ""
	if _, p, err := profiles.Active(); err == nil {
		if p.Enterprise {
			return true
		}
		if p.BaseURL != "" {
			customBackend = true
		}
	}
	if !customBackend {
		return false
	}
	disco, err := cliconfig.Fetch(backendURL)
	return err == nil && disco != nil && disco.Enterprise
}

// enterpriseBillingOverride forces free billing in enterprise (billing is globally
// off; there are no pricing/T&C prompts). Returns nil on Blocks Network.
func enterpriseBillingOverride(enterprise bool) *string {
	if !enterprise {
		return nil
	}
	free := "free"
	return &free
}

// resolvePublishApiKey resolves the API key from (in order): --api-key flag,
// --api-key-stdin flag, BLOCKS_API_KEY env var, then stored credentials.
// Does NOT trigger any browser/login flow — publish requires prior `blocks
// login` per BLOCKS-321. The flag values are passed in so the same resolver
// serves both `publish` and `register` (each binds its own flag variables).
func resolvePublishApiKey(apiKeyFlag string, apiKeyStdin bool) (string, error) {
	if apiKeyFlag != "" {
		return apiKeyFlag, nil
	}
	if apiKeyStdin {
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
	// Prefer the active profile's default-org key; fall back to the legacy
	// credentials.json for one migration cycle.
	if key, ok := activeProfileAPIKey(); ok {
		return key, nil
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
		return "", fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate, or provide --api-key")
	}
	return creds.ApiKey, nil
}

// apiErrorToMap converts an *APIError into the map[string]interface{} shape
// that publishFailedError expects.
func apiErrorToMap(e *blocksapi.APIError) map[string]interface{} {
	m := map[string]interface{}{}
	if e.Code != "" {
		m["code"] = e.Code
	}
	if e.Message != "" {
		m["error"] = e.Message
	}
	return m
}

// publishFailedError formats a registry error for the user. commandName is the
// CLI verb the user actually ran ("blocks publish" or "blocks register") so
// the prefix matches their invocation; an empty value falls back to a generic
// "publish failed" prefix.
func publishFailedError(agentName string, input registry.PromotionInput, payload map[string]interface{}, commandName string) error {
	prefix := commandFailedPrefix(commandName)
	if publishErrorCode(payload) == "BillingModeInvalid" && input.BillingMode == "free" {
		return fmt.Errorf("%s: %s", prefix, existingPaidAgentMessage(agentName))
	}
	if msg := publishErrorMessage(payload); msg != "" {
		return fmt.Errorf("%s: %s", prefix, msg)
	}
	if code := publishErrorCode(payload); code != "" {
		return fmt.Errorf("%s: %s", prefix, code)
	}
	return fmt.Errorf("%s", prefix)
}

// commandFailedPrefix derives the leading error label from the invoked command
// ("blocks publish" -> "publish failed", "blocks register" -> "register failed").
// Unknown / empty commandName defaults to "publish failed" so legacy callers
// keep their original wording.
func commandFailedPrefix(commandName string) string {
	switch commandName {
	case "blocks register":
		return "register failed"
	case "blocks publish":
		return "publish failed"
	default:
		return "publish failed"
	}
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

func existingPaidAgentMessage(agentName string) string {
	agentLabel := "This agent"
	if strings.TrimSpace(agentName) != "" {
		agentLabel = fmt.Sprintf("Agent %s", agentName)
	}
	return fmt.Sprintf("%s is already configured as a Paid agent. Please delete via the Blocks portal before publishing it as a Free agent.", agentLabel)
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
	fmt.Printf("Congratulations! %s is published to %s.\n", boldText(agentName), branding.ProductName())
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
// BLOCKS_APP_BASE_URL / BLOCKS_DASHBOARD_URL → active profile DashboardBaseURL
// (all via resolveAppBaseURL) → the deployment origin from resolveBackendURL
// (BLOCKS_BACKEND_URL → active profile BaseURL → ldflag default → CDM), the
// last tier only when allowNetworkFallback is true. Routing the fallback
// through resolveBackendURL is what keeps the "View" link on the deployment
// the publish actually targeted instead of always stock https://app.blocks.ai
// (BLOCKS-563). In production CDM returns the app origin; in local dev where
// frontend and backend are split, set BLOCKS_APP_BASE_URL to the frontend
// origin. The network-dependent fallback is skipped in non-interactive mode to
// avoid a potential 21s timeout stall in CI/offline environments.
func agentAppURL(agentName string, allowNetworkFallback bool) string {
	if strings.TrimSpace(agentName) == "" {
		return ""
	}
	baseURL := resolveAppBaseURL()
	if baseURL == "" && allowNetworkFallback {
		baseURL = resolveBackendURL()
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
			fmt.Println(registry.HelpOrgNameText())
			continue
		}
		return text
	}
}
