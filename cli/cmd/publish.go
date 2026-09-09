package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
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
	prep, err := preparePublish(args)
	if err != nil {
		return err
	}

	// Every prompt in this flow hangs off `interactive`. It must account for
	// --no-input as well as TTY state: a caller who asked not to be prompted gets
	// the non-interactive path, where missing values are reported as errors naming
	// the flag that supplies them.
	interactive := interactiveSession()

	// The flags are mapped and validated before anything is printed, prompted for or
	// written. They need only the invocation's own inputs, so nothing is gained by
	// waiting — and a refusal that arrives later arrives after the organization picker
	// has already minted a key at the deployment for a publish that then never happens,
	// leaving a credential behind with nothing to spend it on.
	flags, err := collectPromotionFlags(cmd, interactive, prep.enterprise)
	if err != nil {
		return err
	}

	if interactive && !prep.enterprise && publishNeedsInteractivePrompt(cmd) {
		printPublishIntro(prep.agentName)
	}

	if err := applyEnterpriseOrgPicker(prep, interactive); err != nil {
		return err
	}

	// The banner announces what this publish is about to act as, so it prints once
	// the target is settled and before anything is written to the registry. The
	// organization picker above is the last input that can still change it: printed
	// any earlier, the banner would name the organization the local precedence
	// resolved rather than the one the user just chose, and on a multi-organization
	// enterprise account those are routinely different. The picker states the
	// deployment itself before it asks, so the half of the target that is settled
	// early is still seen before the picker's own first side effect.
	clictx.PrintBanner()

	org, err := resolveOrgNameInput(cmd, prep, interactive, "org-name", publishOrgName)
	if err != nil {
		return err
	}

	promInput, err := registry.CollectPromotionInput(prep.isStreaming, prep.isRequest, flags, pricingLimitsFor(prep), nil)
	if err != nil {
		return err
	}

	return finalizePublish(ctx, prep, promInput, org, interactive, submitOptions{
		commandName: "blocks publish",
	})
}

// pricingLimitsFor supplies the platform's pricing bounds for this publish, deferring the
// request until something actually needs them.
//
// Every use of the limits inside CollectPromotionInput — both price prompts and their help
// text, the free-tasks and free-minutes ceilings, and the refusals that cite them — sits
// inside its single `billingMode == "paid"` block. So the bounds cannot change a publish
// that ends up free, and fetching them for one bought nothing while risking the pricing
// client's full five-second timeout whenever the deployment accepts the connection and
// does not answer.
//
// Laziness is the whole mechanism, and it is strictly better than testing the billing-mode
// flag here, which is what this function used to do. That test could only ask "might this
// still turn out paid", so it had to answer yes for every interactive publish — including
// the ones that go on to choose free, and the ones rejected moments later by
// CollectPromotionInput's own cross-field validation. Handing over a function instead moves
// the decision to the point where the outcome is known rather than guessed.
//
// An earlier note here claimed this was as late as the fetch could happen, reasoning that
// the interactive price prompt renders the permitted range and so needs the bounds before
// the call. That holds only when a value is passed. A function is resolved inside the paid
// block, before the prompt that renders the range, so the bounds are in hand exactly when
// they are needed and at no other time.
//
// sync.OnceValue memoizes, so the retry loop around the price prompts cannot turn one
// publish into several requests. The defaults are returned on any failure rather than a
// zero value, so nothing downstream can mistake "unreachable" for "the platform allows
// nothing".
func pricingLimitsFor(prep *publishPrep) func() registry.PricingLimits {
	return sync.OnceValue(func() registry.PricingLimits {
		return registry.FetchPricingLimits(prep.backendURL)
	})
}

// applyEnterpriseOrgPicker prompts the user to choose the owning org when an
// enterprise account belongs to more than one, then records the choice as this
// invocation's organization so the banner, the rest of the flow and the registry
// request all act as it. No-op on Blocks Network, in non-interactive sessions, or
// for single-org users.
//
// It is also a no-op whenever the invocation was handed a credential — --api-key,
// --api-key-stdin or BLOCKS_API_KEY. That key already decides which organization
// the request is authorized as, and replacing it with another organization's would
// silently act as a tenant the caller never named. It is asked of clictx rather
// than of the flags so the answer matches the credential actually being sent.
//
// The picker's own output is ordered around its side effects: it states the
// deployment before it asks anything, and names a minted key as soon as one
// exists, so nothing it does to the user's local state happens without having
// been announced first.
func applyEnterpriseOrgPicker(prep *publishPrep, interactive bool) error {
	if !prep.enterprise || !interactive || clictx.EffectiveCredential().Source.Supplied() {
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
	printOrgPickerTarget(name)
	chosen, cerr := promptOrgChoice(orgs)
	if cerr != nil {
		return cerr
	}
	key, minted, kerr := resolveOrgPublishKey(prep.backendURL, prep.apiKey, name, chosen.Id, chosen.Name)
	// Announced before the error is acted on, and on both paths. The mint is a remote
	// write that has already happened by the time anything can fail after it, so
	// reporting it only when everything downstream succeeded is what would let a key
	// exist on the deployment with nothing anywhere naming it.
	if minted {
		reportMintedOrgKey(chosen.Name, name, kerr == nil)
	}
	if kerr != nil {
		return kerr
	}
	// Hand the choice to the resolver and read the credential back from it, so the
	// key this flow sends is the same one every other consumer of the context sees.
	clictx.SelectOrg(chosen.Name, key)
	prep.apiKey = clictx.EffectiveCredential().Key
	// Remembered, not yet persisted: the profile's default organization follows the
	// choice only once the registry has accepted the agent.
	prep.selectedOrg = &orgSelection{profileName: name, orgId: chosen.Id, orgName: chosen.Name}
	return nil
}

// orgSelection is the organization the enterprise picker chose, carried to the
// end of the flow so the profile's default can be re-pointed at it after the
// publish lands rather than while the user is still being prompted.
type orgSelection struct {
	profileName string
	orgId       string
	orgName     string
}

// collectPromotionFlags maps the `blocks publish` flag variables into
// registry.PromotionFlags. A nil pointer means the flag was not set, so the
// interactive prompt path handles it. Price flags are rejected when present
// but empty, mirroring the original inline validation. On a deployment with no
// marketplace, billing-mode is forced to "free" (billing is globally off; pricing/T&C
// prompts are suppressed) when --billing-mode is absent, and an explicit paid mode is
// refused outright — see refusePaidWithoutMarketplace.
func collectPromotionFlags(cmd *cobra.Command, interactive, enterprise bool) (registry.PromotionFlags, error) {
	flags := registry.PromotionFlags{
		AcceptTerms:    publishAcceptTerms,
		NonInteractive: !interactive,
	}
	if cmd.Flags().Changed("listing") {
		flags.Listing = &publishListing
	}
	if cmd.Flags().Changed("billing-mode") {
		if err := refusePaidWithoutMarketplace(enterprise, publishBillingMode); err != nil {
			return registry.PromotionFlags{}, err
		}
		flags.BillingMode = &publishBillingMode
	} else if ov := enterpriseBillingOverride(enterprise); ov != nil {
		flags.BillingMode = ov
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
	// selectedOrg records the organization the enterprise picker chose, so the
	// profile's default can follow that choice once the registry has accepted the
	// agent. Nil whenever no picker ran, which is every non-enterprise and every
	// single-organization invocation.
	selectedOrg *orgSelection
}

// preparePublish resolves the backend URL and API key, validates the agent
// card, and assembles the base registry envelope. It is shared by `publish`
// and `register` so the two commands cannot drift on validation or payload
// shape; only the promotion params differ between them. The backend origin, the
// credential and the enterprise verdict all come from the invocation's resolved
// context, so neither command re-derives any of them.
func preparePublish(args []string) (*publishPrep, error) {
	backendURL := resolveBackendURL()

	apiKey, err := resolvePublishApiKey()
	if err != nil {
		return nil, err
	}

	enterprise := clictx.Enterprise()

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

// submitOptions carries the per-command bits the shared finalize step needs: the
// command name (for actionable error text) and whether to print the
// promote-to-public hint. How the API key was supplied is not among them — that
// is a property of the invocation, read from the resolved context.
type submitOptions struct {
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

	// The registry has accepted the agent, so the organization it was accepted for
	// is now the one later commands should act as.
	promoteSelectedOrgToDefault(prep)

	respBody, _ := json.Marshal(respPayload)
	agentURL := publishedAgentURL(respBody, prep.agentName, interactive)
	// A link the deployment named that this CLI will not act on replaces the one
	// publishedAgentURL built from local configuration. Falling back to the built link
	// would be the worse answer twice over: it hides that the deployment answered with
	// a different address, and it opens a browser on an address the deployment did not
	// name, at the exact moment there is reason to distrust what it did name.
	if declined := declinedAgentURL(respBody); declined != "" {
		agentURL = declined
	}
	printPublishSuccess(prep.agentName, promInput, agentURL, opts.commandName)
	if opts.promoteHint {
		if clictx.Enterprise() {
			fmt.Println("To change visibility later, run `blocks publish --listing public`.")
		} else {
			fmt.Println("To make this agent public or set pricing later, run `blocks publish`.")
		}
	}

	// Asked of safeHTTPURL rather than of a flag carried down from the block above, so
	// the question the opener is gated on is the same question, answered by the same
	// function, that the printed link was held to. A second spelling of "is this
	// openable" is how the two would come to disagree.
	if interactive && safeHTTPURL(agentURL) != "" {
		_ = openBrowser(agentURL)
	}
	return nil
}

// promoteSelectedOrgToDefault makes the organization the enterprise picker chose
// the active profile's default, once the registry has accepted the agent. Before
// that point the choice is only this invocation's, held in clictx, so a publish
// abandoned at a later prompt or rejected by the registry leaves `run` and
// `whoami` resolving the organization they resolved before.
//
// A failure to record it is reported and not fatal: the agent is published, and
// failing the command over a local bookkeeping write would tell the user the
// opposite of what happened.
func promoteSelectedOrgToDefault(prep *publishPrep) {
	sel := prep.selectedOrg
	if sel == nil {
		return
	}
	if err := setProfileDefaultOrg(sel.profileName, sel.orgId); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not make %s the default organization for later commands: %v\n",
			termsafe.Text(sel.orgName), err)
	}
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
		switch clictx.EffectiveCredential().Source {
		case clictx.SourceFlag:
			return fmt.Errorf("authentication failed — the provided --api-key was rejected; replace it with a valid key and retry")
		case clictx.SourceStdin:
			return fmt.Errorf("authentication failed — the API key provided via --api-key-stdin was rejected; replace it with a valid key and retry")
		}
		return fmt.Errorf("authentication failed — run 'blocks login' to re-authenticate, then retry '%s'", opts.commandName)
	case 403:
		return fmt.Errorf("permission denied (HTTP 403) — check that your API key owns this agent")
	default:
		return publishFailedError(agentName, promInput, apiErrorToMap(apiErr), opts.commandName)
	}
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

// refusePaidWithoutMarketplace rejects an explicit paid billing mode on a deployment
// that has no marketplace to sell into. It answers nothing on Blocks Network, and
// nothing for any other billing mode: an explicit --billing-mode free asks for what
// such a deployment already does.
//
// Refused rather than quietly reinterpreted as free, which is the other half of the
// same suppression. Forcing free where the flag is absent turns an unstated default
// into the only thing the deployment can do; forcing it where the operator stated the
// opposite would silently rewrite an instruction, and a publish that reports success
// is the last place anyone looks for a flag that did not take effect. A caller asking
// a marketplace-less deployment for paid billing is pointed at the wrong deployment,
// or was written against a marketplace this one does not have, and both are worth
// stopping on: the agent would otherwise be free forever with no pricing anywhere to
// change, and the next-steps text would promise paid tasks a deployment with billing
// off can never bill for. The error names the flag value that does work, so the fix
// is one edit.
func refusePaidWithoutMarketplace(enterprise bool, mode string) error {
	if !enterprise || mode != "paid" {
		return nil
	}
	return fmt.Errorf("--billing-mode paid is not available on this deployment: billing is off and there is no marketplace to sell into — publish free instead (omit --billing-mode, or pass --billing-mode free)")
}

// resolvePublishApiKey returns the API key this invocation will send. The
// precedence — --api-key, --api-key-stdin, BLOCKS_API_KEY, the active profile's
// cached key, then the legacy credential store — is resolved once per invocation
// by clictx, so `publish`, `register` and `unregister` all send the key the
// context banner described, and a piped key is read exactly once. Only the error
// wording lives here. Does NOT trigger any browser/login flow — these commands
// require a prior `blocks login`.
func resolvePublishApiKey() (string, error) {
	c := clictx.EffectiveCredential()
	switch {
	case c.Err != nil:
		return "", c.Err
	case c.Key != "":
		return c.Key, nil
	case c.StoreErr != nil:
		return "", fmt.Errorf("failed to load credentials: %w", c.StoreErr)
	case c.Expired:
		return "", fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate, or provide --api-key")
	default:
		return "", fmt.Errorf("not authenticated — run 'blocks login' first, or provide --api-key")
	}
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
		fmt.Printf("Agent: %s\n", termsafe.Text(agentName))
	}
	fmt.Println()
	fmt.Println("We'll collect the details we need, then publish your agent.")
}

func printPublishSummary(agentName string, input registry.PromotionInput) {
	fmt.Println()
	fmt.Printf("Publishing %s...\n", termsafe.Text(agentName))
	fmt.Printf("Visibility: %s\n", displayMode(input.Listing))
	// Enterprise has no marketplace: billing is globally off, so the line is
	// omitted rather than printed as "Free".
	if !clictx.Enterprise() {
		fmt.Printf("Billing: %s\n", displayMode(input.BillingMode))
	}
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

func printPublishSuccess(agentName string, input registry.PromotionInput, agentURL, commandName string) {
	// Escaped once, for both places this block prints the name, so the logo and the
	// sentence below it cannot disagree about how the same name renders. boldText wraps
	// its argument in ANSI codes and is no protection at all: a name carrying a
	// cursor-up and erase-line pair is interpreted through it, and these are the lines
	// stating which deployment the agent just landed on and with what visibility.
	shown := termsafe.Text(agentName)
	fmt.Println()
	fmt.Println(blocksSuccessLogo(shown))
	fmt.Println()
	// `register` does not publish — using the publish verb for it was the bug;
	// the product name was already correct.
	verb := "is published to"
	if commandName == "blocks register" {
		verb = "is registered on"
	}
	fmt.Printf("Congratulations! %s %s %s.\n", boldText(shown), verb, clictx.TargetName())
	fmt.Printf("Visibility: %s\n", boldText(displayMode(input.Listing)))
	if !clictx.Enterprise() {
		fmt.Printf("Billing: %s\n", boldText(displayMode(input.BillingMode)))
	}
	// Escaped like every other value this block prints: the deployment chose it, and a
	// declined link reaches here without having passed stripControlChars at all.
	if agentURL != "" {
		fmt.Printf("View: %s\n", termsafe.Text(agentURL))
		if safeHTTPURL(agentURL) == "" {
			fmt.Println(declinedAgentURLNote)
		}
	}
	for _, line := range publishNextSteps(input) {
		fmt.Println(line)
	}
}

// blocksSuccessLogo centres the agent name inside the wordmark. agentName must already
// be inert — centreing counts the runes it is given, so escaping here would change the
// width its caller measured.
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

// declinedAgentURLNote explains a "View" link that was printed but not opened. It says
// what was not done and why, and asks the reader to make the call, because only they
// know which site they expected: the CLI cannot tell a deployment served from an unusual
// authority apart from one naming somebody else's.
const declinedAgentURLNote = "Not opened for you: the address above is not one this CLI will hand to a browser — the site it reaches may not be the one it appears to name. Check it before visiting it."

// agentURLKeys are the properties a registration response could name the published
// agent's own page in. Spelled once, so the decoder that reads one and the check that
// declines one cannot look at different sets of keys and disagree about whether a
// response named a link at all.
var agentURLKeys = []string{"agentUrl", "agentURL", "url"}

// responseAgentURL returns the agent URL a registration response named, exactly as it
// arrived and unvalidated, or "" when it named none.
//
// Defensive, and unreachable against the shipped contract. The response body for
// POST /api/v1/registry/agents carries agentName, status and ts, optionally
// effectiveListing and billingMode, and no further property at all — so a deployment
// honouring it cannot send any of these keys, and none of the branches below can fire.
// That is also why no test pins it as unreachable: proving a branch cannot fire needs a
// response nobody can legitimately produce, and a test that answered one off-contract
// would be asserting the opposite.
//
// It is kept rather than deleted for one reason: adding a property to a response is not
// a breaking change, and where a deployment puts an agent's page is the deployment's
// question — everything else here guesses it from local configuration, in agentAppURL,
// and a deployment served from a path prefix or a split frontend is exactly where that
// guess is worst. A newer deployment could start answering the field while this CLI
// version is still installed, and the whole cost of being ready for that is this
// comment.
//
// Nothing may come to depend on it. A caller that needs a just-published agent's page
// builds it; this only prefers an answer if one arrives.
func responseAgentURL(payload map[string]interface{}) string {
	if raw := stringField(payload, agentURLKeys...); raw != "" {
		return raw
	}
	if agentPayload, ok := payload["agent"].(map[string]interface{}); ok {
		return stringField(agentPayload, agentURLKeys...)
	}
	return ""
}

func publishedAgentURL(respBody []byte, agentName string, allowNetworkFallback bool) string {
	var payload map[string]interface{}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return agentAppURL(agentName, allowNetworkFallback)
	}
	if url := safeHTTPURL(responseAgentURL(payload)); url != "" {
		return url
	}
	return agentAppURL(agentName, allowNetworkFallback)
}

// declinedAgentURL returns the agent URL a registration response named that this CLI
// will not act on, or "" when the response named none or named one safeHTTPURL accepts.
//
// It exists so a refusal can be shown instead of silently swapped for a link built from
// local configuration, and so it can be shown without failing the command: by the time
// this response is read the agent is registered on the deployment, and a publish that
// reports failure over the shape of a link in its own success output would be reporting
// the opposite of what happened.
func declinedAgentURL(respBody []byte) string {
	var payload map[string]interface{}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return ""
	}
	raw := responseAgentURL(payload)
	if raw == "" || safeHTTPURL(raw) != "" {
		return ""
	}
	return raw
}

// agentAppURL builds the dashboard URL for an agent. Resolution order:
// BLOCKS_APP_BASE_URL / BLOCKS_DASHBOARD_URL → active profile DashboardBaseURL
// (all via resolveAppBaseURL) → the deployment origin's offline tiers
// (BLOCKS_BACKEND_URL → active profile BaseURL → ldflag default) → CDM. Routing
// the fallback through the deployment origin is what keeps the "View" link on
// the deployment the publish actually targeted instead of always stock
// https://app.blocks.ai. Only the CDM tier depends on the network,
// so only it is gated behind allowNetworkFallback — a non-interactive publish
// with BLOCKS_BACKEND_URL (or a profile / ldflag default) set still gets a View
// link, while CI/offline environments avoid a potential 21s CDM timeout stall.
// In production CDM returns the app origin; in local dev where frontend and
// backend are split, set BLOCKS_APP_BASE_URL to the frontend origin.
func agentAppURL(agentName string, allowNetworkFallback bool) string {
	if strings.TrimSpace(agentName) == "" {
		return ""
	}
	baseURL := resolveAppBaseURL()
	if baseURL == "" {
		baseURL = resolveBackendURLOffline()
	}
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

// safeHTTPURL returns raw once it is a web address this CLI may hand to a browser, and
// "" when it is not one. It is the gate in front of the "View" link a publish prints and
// opens, and every value that reaches it is chosen elsewhere: by a project .env
// (BLOCKS_APP_BASE_URL, BLOCKS_DASHBOARD_URL, BLOCKS_BACKEND_URL), by a profile a
// deployment's discovery payload filled in, by a CDM payload, or by the deployment's own
// registration response.
//
// A scheme and a non-empty host were not enough, twice over. `https://` was not
// required, so a deployment could put a credential-free page link on plaintext http to
// any host on the internet. And the authority is not the host it looks like: everything
// before an `@` is userinfo, so `https://app.acme.example@evil.example/x` reads as the
// dashboard and opens evil.example — the browser opener asks the operating system to
// dispatch the URL, so nothing downstream re-examines it.
//
// The rule applied is deploymentURL's, unchanged: https, or http for loopback only, an
// authority that is a host and nothing else, and a dialable port. It is the same
// question `blocks login` asks of an instance argument and `blocks dashboard` asks of a
// dashboard origin, and a fourth spelling of it would be a fourth answer.
//
// It is applied to the ORIGIN only, and the path, query and fragment are kept as given.
// deploymentURL refuses a query and a fragment because its result is an origin the
// caller appends endpoint paths to as a string, so `https://host?x=1` would become
// `https://host?x=1/api/v1/...`. This value is not that: it is a terminal page link, it
// is never concatenated with anything, and a dashboard page legitimately addresses a tab
// or an anchor. Refusing them here would decline links that are entirely ordinary, while
// the hazard deploymentURL guards against cannot arise.
func safeHTTPURL(raw string) string {
	cleaned := stripControlChars(strings.TrimSpace(raw))
	parsed, err := url.Parse(cleaned)
	if err != nil {
		return ""
	}
	// The userinfo is carried into the origin deliberately: it is the component the
	// authority rule exists to reject, and dropping it here would ask the rule about a
	// host this URL does not actually address.
	origin := &url.URL{Scheme: parsed.Scheme, User: parsed.User, Host: parsed.Host}
	if deploymentURL(origin.String()) == "" {
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
	for {
		fmt.Printf("\nOrganization name [%s] (? for help): ", defaultName)
		line, ok, refused := readStdinLine()
		if refused {
			// In --no-input mode, we cannot prompt for retry. Return empty to
			// signal that retry is not possible.
			return ""
		}
		if !ok {
			return ""
		}
		if line == "?" {
			fmt.Println(registry.HelpOrgNameText())
			continue
		}
		return line
	}
}
