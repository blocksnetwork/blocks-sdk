package registry

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/shopspring/decimal"
)

const (
	MinPricePerTask   = "0.0001"
	MinPricePerMinute = "0.01"
	MaxPricePerTask   = "25.00"
	MaxPricePerMinute = "1.00"

	MaxFreeTasksPerConsumer   = 100
	MaxFreeMinutesPerConsumer = 30
)

const (
	promptAnsiBold  = "\x1b[1m"
	promptAnsiReset = "\x1b[0m"
)

// helpListingText is the visibility-prompt help. It reads the active product
// name at call time so enterprise deployments brand it correctly.
func helpListingText() string {
	return "  Public: Anyone on " + branding.ProductName() + " can discover and send tasks to your agent.\n" +
		"  Private: Only organizations you explicitly invite can see and use your agent.\n" +
		"  You can change this later by re-publishing."
}

// Help text constants for publish prompts.
const (
	helpBilling = "  Free: No charge for consumers who use your agent.\n" +
		"  Paid: You set a price per task or per minute and earn money when consumers use\n" +
		"  your agent. Blocks Network takes a platform fee; you keep the rest. Paid agents\n" +
		"  require accepting the platform terms."

	helpFreeTrialTasksTpl = "  Number of request tasks each consumer organization can run for free before\n" +
		"  charges begin. Between 1 and %d. Enter 0 or leave blank for no free trial.\n" +
		"  Tracked per consumer organization, not per user."

	helpFreeTrialMinutesTpl = "  Number of pipe-task minutes each consumer organization gets for free before\n" +
		"  per-minute charges begin. Between 1 and %d. Enter 0 or leave blank for no\n" +
		"  free trial. Tracked per consumer organization, not per user."

	helpAttestLaws = "  Required for paid agents. You're confirming that your agent doesn't violate\n" +
		"  laws in the jurisdictions where it operates (e.g. data privacy, export controls,\n" +
		"  content regulations)."

	helpAcceptTerms = "  Required for paid agents. You're accepting the Blocks Network terms for paid\n" +
		"  agent providers, including the platform fee structure and payout terms."
)

const (
	DefaultPrice = "0.10"

	pricePerTaskLabel   = "Price per task"
	pricePerMinuteLabel = "Price per minute"

	freeTrialTaskPrompt = promptAnsiBold + "How many task runs" + promptAnsiReset + " should each consumer organization get for free before per-task charges begin?\n" +
		"Example: 5 means the first 5 completed request tasks are free for that consumer organization.\n" +
		promptAnsiBold + "Free trial task runs per consumer organization [0]" + promptAnsiReset
	freeTrialMinutePrompt = promptAnsiBold + "How many pipe-task minutes" + promptAnsiReset + " should each consumer organization get for free before per-minute charges begin?\n" +
		"Example: 30 means the first 30 completed pipe-task minutes are free for that consumer organization.\n" +
		promptAnsiBold + "Free trial minutes per consumer organization [0]" + promptAnsiReset
)

// CollectPromotionInput collects publish/listing parameters from flags and interactive prompts.
// isStreaming/isRequest are derived from taskKinds; both can be true for dual-kind agents.
//
// Backend invariants mirrored here (fail-fast client-side):
//   - billingMode is explicit: "free" or "paid". Pricing does not imply billing mode.
//   - listing=private may be free or paid; private+free is allowed (D1).
//   - Pricing prompts run only when billingMode="paid".
//   - T&C is required for billingMode="paid" regardless of listing (D3 paid-any-listing).
//   - billingMode="free" with no pricing is allowed for any listing.
//
// The caller is treated as non-interactive when NonInteractive is set (stdin
// is not a TTY). In non-interactive mode, required values must come from flags
// and optional fields default to nil instead of prompting. --billing-mode is
// required; omitting it is a fast-fail error. --accept-terms is only required
// for paid agents (free agents skip T&C entirely, matching the dashboard).
//
// limitsFor supplies the platform's pricing bounds, and is called only if this publish
// turns out paid — every use of the bounds is inside the paid block below. Taking a
// function rather than a value is what keeps the request out of a publish that cannot
// use the answer: a free publish, or one rejected by the validation above, never calls
// it, while a paid one still has real bounds in hand at the moment the price prompt
// renders them. Callers with nothing to fetch pass DefaultPricingLimits directly.
func CollectPromotionInput(isStreaming, isRequest bool, flags PromotionFlags, limitsFor func() PricingLimits, scanner *bufio.Scanner) (PromotionInput, error) {
	if scanner == nil {
		scanner = bufio.NewScanner(os.Stdin)
	}
	isDualKind := isStreaming && isRequest
	nonInteractive := flags.NonInteractive

	if err := validateExclusivePricingFlags(isDualKind, flags); err != nil {
		return PromotionInput{}, err
	}

	listing, err := resolveListing(flags, nonInteractive, scanner)
	if err != nil {
		return PromotionInput{}, err
	}

	billingMode, err := resolveBillingMode(flags, nonInteractive, scanner)
	if err != nil {
		return PromotionInput{}, err
	}

	if billingMode == "free" {
		if err := rejectPricingOnFree(flags); err != nil {
			return PromotionInput{}, err
		}
	}

	input := PromotionInput{
		Listing:     listing,
		BillingMode: billingMode,
	}

	if billingMode == "paid" {
		// Resolved here, not at the call site: this is the first point at which the
		// outcome is known to be paid, and so the first point at which the bounds can
		// affect anything.
		limits := limitsFor()

		if err := collectPaidPrices(&input, isStreaming, isRequest, flags, limits, nonInteractive, scanner); err != nil {
			return PromotionInput{}, err
		}
		if err := collectFreeTrialAllowances(&input, isStreaming, isRequest, flags, limits, nonInteractive, scanner); err != nil {
			return PromotionInput{}, err
		}
		if err := acceptPaidTerms(&input, flags, nonInteractive, scanner); err != nil {
			return PromotionInput{}, err
		}
	}

	return input, nil
}

// validateExclusivePricingFlags rejects flag combinations that cannot be reconciled,
// before anything is prompted for or fetched. A dual-kind agent has to name which kind
// each value applies to, and the singular forms cannot be combined with the specific ones.
func validateExclusivePricingFlags(isDualKind bool, flags PromotionFlags) error {
	if isDualKind && flags.Price != nil {
		return fmt.Errorf("agent has both request and pipe taskKinds - use --price-per-task and --price-per-minute instead of --price")
	}
	if isDualKind && flags.FreeUnits != nil {
		return fmt.Errorf("agent has both request and pipe taskKinds - use --free-tasks and --free-minutes instead of --free-units")
	}
	if flags.Price != nil && flags.PricePerTask != nil {
		return fmt.Errorf("--price and --price-per-task are mutually exclusive")
	}
	if flags.Price != nil && flags.PricePerMinute != nil {
		return fmt.Errorf("--price and --price-per-minute are mutually exclusive")
	}
	if flags.FreeUnits != nil && flags.FreeTasks != nil {
		return fmt.Errorf("--free-units and --free-tasks are mutually exclusive")
	}
	if flags.FreeUnits != nil && flags.FreeMinutes != nil {
		return fmt.Errorf("--free-units and --free-minutes are mutually exclusive")
	}
	return nil
}

// resolveListing settles agent visibility from the flag, else by prompt. Non-interactive
// callers must supply it: there is no safe default between public and private.
func resolveListing(flags PromotionFlags, nonInteractive bool, scanner *bufio.Scanner) (string, error) {
	if flags.Listing != nil {
		listing := *flags.Listing
		if listing != "public" && listing != "private" {
			return "", fmt.Errorf("--listing must be \"public\" or \"private\", got %q", listing)
		}
		return listing, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("Missing --listing. Pass --listing public or --listing private.")
	}
	return promptListingSelection(scanner)
}

// resolveBillingMode settles the billing mode from the flag, else by prompt. Like the
// listing, it has no default — pricing does not imply a mode, and a mode guessed here
// would decide whether the agent charges.
func resolveBillingMode(flags PromotionFlags, nonInteractive bool, scanner *bufio.Scanner) (string, error) {
	if flags.BillingMode != nil {
		billingMode := *flags.BillingMode
		if billingMode != "free" && billingMode != "paid" {
			return "", fmt.Errorf("--billing-mode must be \"free\" or \"paid\", got %q", billingMode)
		}
		return billingMode, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("Missing --billing-mode. Pass --billing-mode free or --billing-mode paid.")
	}
	return promptBillingMode(scanner)
}

// rejectPricingOnFree refuses a free agent carrying a positive price. The backend refuses
// it too; saying so here is what makes the price visible as the problem rather than
// dropping it silently, since the paid-only path that would have read it never runs.
func rejectPricingOnFree(flags PromotionFlags) error {
	if priceGtZero(flags.Price) || priceGtZero(flags.PricePerTask) || priceGtZero(flags.PricePerMinute) {
		return fmt.Errorf("Free agents cannot include positive prices. Use --billing-mode paid or remove the price flags.")
	}
	return nil
}

// collectPaidPrices fills in whichever prices the agent's task kinds call for, looping
// until at least one is above zero.
//
// Each prompt is individually optional because a dual-kind agent needs only one positive
// price, so "at least one" can only be checked once both have been answered — hence the
// loop rather than a required flag per prompt. Non-interactive callers get one pass and an
// error, their flags having already been explicit.
func collectPaidPrices(input *PromotionInput, isStreaming, isRequest bool, flags PromotionFlags, limits PricingLimits, nonInteractive bool, scanner *bufio.Scanner) error {
	const pricingRequired = false

	for {
		input.PricePerTask = nil
		input.PricePerMinute = nil

		if isRequest {
			price, err := resolvePrice(pricePrompt(pricePerTaskLabel, limits.MaxPricePerTask, limits.MinPricePerTask), pricePerTaskLabel, flags.Price, flags.PricePerTask, limits.MinPricePerTask, limits.MaxPricePerTask, pricingRequired, nonInteractive, scanner)
			if err != nil {
				return err
			}
			if price != nil {
				input.PricePerTask = price
			}
		}

		if isStreaming {
			price, err := resolvePrice(pricePrompt(pricePerMinuteLabel, limits.MaxPricePerMinute, limits.MinPricePerMinute), pricePerMinuteLabel, flags.Price, flags.PricePerMinute, limits.MinPricePerMinute, limits.MaxPricePerMinute, pricingRequired, nonInteractive, scanner)
			if err != nil {
				return err
			}
			if price != nil {
				input.PricePerMinute = price
			}
		}

		if priceGtZero(input.PricePerTask) || priceGtZero(input.PricePerMinute) {
			return nil
		}

		// Non-interactive: hard error (flags were explicit).
		if nonInteractive {
			return fmt.Errorf("Paid agents need at least one price above 0. Use --price-per-task and/or --price-per-minute.")
		}

		fmt.Println("  Paid agents need at least one price above 0. Please enter a price.")
	}
}

// collectFreeTrialAllowances fills in the per-consumer-organization free allowances the
// platform permits, and refuses a flag asking for one the platform has switched off — a
// ceiling of zero means the value could never take effect, so accepting it silently would
// publish an agent whose trial does not exist.
func collectFreeTrialAllowances(input *PromotionInput, isStreaming, isRequest bool, flags PromotionFlags, limits PricingLimits, nonInteractive bool, scanner *bufio.Scanner) error {
	freeTasksEnabled := isRequest && limits.MaxFreeTasksAllowed > 0
	freeMinutesEnabled := isStreaming && limits.MaxFreeMinutesAllowed > 0

	if err := rejectDisabledAllowances(isStreaming, isRequest, flags, freeTasksEnabled, freeMinutesEnabled); err != nil {
		return err
	}

	if (freeTasksEnabled || freeMinutesEnabled) && shouldPromptFreeTrialAllowance(isRequest, isStreaming, flags, nonInteractive) {
		printFreeTrialAllowanceIntro()
	}

	if freeTasksEnabled {
		free, err := resolveFreeUnits(freeTrialTaskPrompt, flags.FreeUnits, flags.FreeTasks, limits.MaxFreeTasksAllowed, nonInteractive, scanner)
		if err != nil {
			return err
		}
		if free != nil {
			input.FreeTasksPerConsumer = free
		}
	}

	if freeMinutesEnabled {
		free, err := resolveFreeUnits(freeTrialMinutePrompt, flags.FreeUnits, flags.FreeMinutes, limits.MaxFreeMinutesAllowed, nonInteractive, scanner)
		if err != nil {
			return err
		}
		if free != nil {
			input.FreeMinutesPerConsumer = free
		}
	}
	return nil
}

// rejectDisabledAllowances refuses a flag asking for an allowance whose platform ceiling
// is zero. Accepting it would publish an agent advertising a trial that can never apply,
// so the flag is named as the problem rather than dropped. --free-units counts for
// whichever kind is disabled, being the singular form of both.
func rejectDisabledAllowances(isStreaming, isRequest bool, flags PromotionFlags, freeTasksEnabled, freeMinutesEnabled bool) error {
	freeUnitsRequested := flags.FreeUnits != nil && *flags.FreeUnits > 0

	if isRequest && !freeTasksEnabled {
		if (flags.FreeTasks != nil && *flags.FreeTasks > 0) || freeUnitsRequested {
			return fmt.Errorf("Free tasks are disabled by the platform (maximum is 0).")
		}
	}
	if isStreaming && !freeMinutesEnabled {
		if (flags.FreeMinutes != nil && *flags.FreeMinutes > 0) || freeUnitsRequested {
			return fmt.Errorf("Free minutes are disabled by the platform (maximum is 0).")
		}
	}
	return nil
}

// acceptPaidTerms records acceptance of the paid-agent terms, which are required for a
// paid agent whatever its listing — both public+paid and private+paid. Free agents of any
// listing never reach here.
func acceptPaidTerms(input *PromotionInput, flags PromotionFlags, nonInteractive bool, scanner *bufio.Scanner) error {
	if !flags.AcceptTerms {
		if nonInteractive {
			return fmt.Errorf("Paid agents require --accept-terms to accept platform terms non-interactively.")
		}
		if err := promptAttestations(scanner); err != nil {
			return err
		}
	}
	input.TcAcceptedAt = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func promptListingSelection(scanner *bufio.Scanner) (string, error) {
	for {
		fmt.Println("\nWho should be able to discover and use this agent?")
		fmt.Println()
		fmt.Println("  1. " + boldPrompt("Public Agent") + "   Visible and usable by everyone on " + branding.ProductName() + ".")
		fmt.Println("  2. " + boldPrompt("Private Agent") + "  Visible and usable only by organizations you invite.")
		fmt.Println()
		fmt.Print("Select visibility [1/2] (? for help): ")

		if !scanner.Scan() {
			return "", fmt.Errorf("no input received")
		}
		choice := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if choice == "?" {
			fmt.Println(helpListingText())
			fmt.Println()
			continue
		}
		switch choice {
		case "1", "public", "p":
			return "public", nil
		case "2", "private", "pr":
			return "private", nil
		default:
			fmt.Println("  Choose 1 for Public Agent or 2 for Private Agent. Type ? for help.")
			continue
		}
	}
}

func promptBillingMode(scanner *bufio.Scanner) (string, error) {
	for {
		fmt.Println("\nHow should usage be priced?")
		fmt.Println()
		fmt.Println("  1. " + boldPrompt("Free Agent") + "  No charge for any allowed consumer.")
		fmt.Println("  2. " + boldPrompt("Paid Agent") + "  Set usage prices and accept the paid-agent terms.")
		fmt.Println()
		fmt.Print("Select billing [1/2] (? for help): ")

		if !scanner.Scan() {
			return "", fmt.Errorf("no input received")
		}
		choice := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if choice == "?" {
			fmt.Println(helpBilling)
			fmt.Println()
			continue
		}
		switch choice {
		case "1", "free", "f":
			return "free", nil
		case "2", "paid":
			return "paid", nil
		default:
			fmt.Println("  Choose 1 for Free Agent or 2 for Paid Agent. Type ? for help.")
			continue
		}
	}
}

// validatePrice validates a decimal price string.
// max 6 fractional digits; must be 0 (free) or >= min when non-zero.
func validatePrice(raw string, min string, label string) error {
	return validatePriceRange(raw, min, maxPriceForMin(min), label)
}

func validatePriceRange(raw string, min string, max string, label string) error {
	raw = normalizePriceInput(raw)
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return fmt.Errorf("Enter a valid USD amount, for example 0.25 or 12.00.")
	}
	if v.Sign() < 0 {
		return fmt.Errorf("%s must be 0 or greater.", label)
	}
	if v.Exponent() < -6 {
		return fmt.Errorf("%s: max 6 fractional digits", label)
	}
	maxDecimal, _ := decimal.NewFromString(max)
	if v.GreaterThan(maxDecimal) {
		return fmt.Errorf("%s must be %s or less.", label, max)
	}
	if v.IsZero() {
		return nil
	}
	m, _ := decimal.NewFromString(min)
	if v.LessThan(m) {
		return fmt.Errorf("%s must be 0 or at least %s.", label, min)
	}
	return nil
}

func effectiveDefault(minPrice, maxPrice string) string {
	d, _ := decimal.NewFromString(DefaultPrice)
	min, _ := decimal.NewFromString(minPrice)
	max, _ := decimal.NewFromString(maxPrice)
	if d.LessThan(min) {
		return min.String()
	}
	if d.GreaterThan(max) {
		return max.String()
	}
	return DefaultPrice
}

func pricePrompt(label, maxPrice, minPrice string) string {
	return fmt.Sprintf("%s%s%s in USD (up to %s) [%s]", promptAnsiBold, label, promptAnsiReset, maxPrice, effectiveDefault(minPrice, maxPrice))
}

func normalizePriceInput(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "$")
	return strings.TrimSpace(raw)
}

func maxPriceForMin(min string) string {
	if min == MinPricePerMinute {
		return MaxPricePerMinute
	}
	return MaxPricePerTask
}

func priceGtZero(p *string) bool {
	if p == nil {
		return false
	}
	v, err := decimal.NewFromString(normalizePriceInput(*p))
	return err == nil && v.Sign() > 0
}

func shouldPromptFreeTrialAllowance(isRequest, isStreaming bool, flags PromotionFlags, nonInteractive bool) bool {
	if nonInteractive {
		return false
	}
	if isRequest && flags.FreeUnits == nil && flags.FreeTasks == nil {
		return true
	}
	if isStreaming && flags.FreeUnits == nil && flags.FreeMinutes == nil {
		return true
	}
	return false
}

func printFreeTrialAllowanceIntro() {
	fmt.Println("\n" + boldPrompt("Optional free trial allowance"))
	fmt.Println()
	fmt.Println("You can let each consumer organization try this paid agent before charges begin.")
	fmt.Println("Leave blank for 0. Trial usage is tracked per consumer organization, not per user.")
}

// resolvePrice resolves a price from flags or interactive prompt, returning a decimal-dollar string.
// genericFlag is --price, specificFlag is --price-per-task or --price-per-minute.
// When nonInteractive is true and no flag is set, returns an error if the field is
// required or nil if optional - never reads stdin.
func resolvePrice(prompt string, label string, genericFlag, specificFlag *string, minPrice string, maxPrice string, required, nonInteractive bool, scanner *bufio.Scanner) (*string, error) {
	var raw *string
	if specificFlag != nil {
		raw = specificFlag
	} else if genericFlag != nil {
		raw = genericFlag
	}

	if raw != nil {
		if err := validatePriceRange(*raw, minPrice, maxPrice, label); err != nil {
			if nonInteractive {
				return nil, err
			}
			fmt.Printf("  %v\n", err)
		} else {
			v, _ := decimal.NewFromString(normalizePriceInput(*raw))
			if required && v.IsZero() {
				if nonInteractive {
					return nil, fmt.Errorf("%s must be > 0", label)
				}
				fmt.Printf("  %s must be > 0\n", label)
			} else {
				s := v.StringFixed(6)
				return &s, nil
			}
		}
	}

	if nonInteractive {
		if required {
			return nil, fmt.Errorf("%s is required (use --price, --price-per-task, or --price-per-minute)", label)
		}
		return nil, nil
	}

	// Generate help text from actual limits.
	effDefault := effectiveDefault(minPrice, maxPrice)
	var helpText string
	if label == pricePerMinuteLabel {
		helpText = fmt.Sprintf("  The USD amount charged to the consumer for each minute of a pipe (long-running)\n"+
			"  task. Between $%s and $%s. Leave blank to use the default ($%s).\n"+
			"  Enter 0 for no per-minute charge (if you're using per-task pricing instead).", minPrice, maxPrice, effDefault)
	} else {
		helpText = fmt.Sprintf("  The USD amount charged to the consumer for each completed request task.\n"+
			"  Between $%s and $%s. Leave blank to use the default ($%s).\n"+
			"  Enter 0 for no per-task charge (if you're using per-minute pricing instead).", minPrice, maxPrice, effDefault)
	}

	for {
		fmt.Printf("%s (? for help): ", prompt)

		if !scanner.Scan() {
			if required {
				return nil, fmt.Errorf("no input received for %s", label)
			}
			return nil, nil
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
		if text == "" {
			text = effectiveDefault(minPrice, maxPrice)
		}

		if err := validatePriceRange(text, minPrice, maxPrice, label); err != nil {
			fmt.Printf("  %v\n", err)
			continue
		}
		v, _ := decimal.NewFromString(normalizePriceInput(text))
		if required && v.IsZero() {
			fmt.Printf("  %s must be > 0\n", label)
			continue
		}
		s := v.StringFixed(6)
		return &s, nil
	}
}

// resolveFreeUnits resolves free units from flags or interactive prompt.
// When nonInteractive is true and no flag is set, returns nil and never reads stdin.
func resolveFreeUnits(label string, genericFlag, specificFlag *int, max int, nonInteractive bool, scanner *bufio.Scanner) (*int, error) {
	flagVal := specificFlag
	if flagVal == nil {
		flagVal = genericFlag
	}

	if flagVal != nil {
		result, err := validateFreeUnits(*flagVal, max)
		if err != nil {
			if nonInteractive {
				return nil, err
			}
			fmt.Printf("  %v\n", err)
		} else {
			return result, nil
		}
	}

	if nonInteractive {
		return nil, nil
	}

	// Generate help text from actual max.
	helpText := fmt.Sprintf(helpFreeTrialTasksTpl, max)
	if strings.Contains(label, "minute") || strings.Contains(label, "Minute") {
		helpText = fmt.Sprintf(helpFreeTrialMinutesTpl, max)
	}

	for {
		fmt.Printf("%s (? for help): ", label)
		if !scanner.Scan() {
			return nil, nil
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(helpText)
			fmt.Println()
			continue
		}
		if text == "" {
			return nil, nil
		}

		v, err := decimal.NewFromString(text)
		if err != nil || v.Sign() < 0 || !v.IsInteger() {
			fmt.Println("  Enter a whole number of 0 or greater.")
			continue
		}
		if v.IsZero() {
			return nil, nil
		}
		i := int(v.IntPart())
		if i > max {
			fmt.Printf("  Maximum is %d.\n", max)
			continue
		}
		return &i, nil
	}
}

func validateFreeUnits(v int, max int) (*int, error) {
	if v < 0 {
		return nil, fmt.Errorf("Enter a whole number of 0 or greater.")
	}
	if v == 0 {
		return nil, nil
	}
	if v > max {
		return nil, fmt.Errorf("Maximum is %d.", max)
	}
	return &v, nil
}

func promptAttestations(scanner *bufio.Scanner) error {
	fmt.Println("\n" + boldPrompt("Before publishing a paid agent:"))
	fmt.Println()

	// First attestation: legal compliance
	for {
		fmt.Print("  " + boldPrompt("I attest this agent complies with applicable laws.") + " (y/N, ? for help): ")
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(helpAttestLaws)
			fmt.Println()
			continue
		}
		if isNo(text) {
			return fmt.Errorf("Paid agents require both attestations. Publish canceled.")
		}
		if !isYes(text) {
			fmt.Println("  Please answer y or n.")
			continue
		}
		break
	}

	// Second attestation: platform terms
	for {
		fmt.Print("  " + boldPrompt("I accept the platform terms.") + " (y/N, ? for help): ")
		if !scanner.Scan() {
			return fmt.Errorf("no input received")
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "?" {
			fmt.Println(helpAcceptTerms)
			fmt.Println()
			continue
		}
		if isNo(text) {
			return fmt.Errorf("Paid agents require both attestations. Publish canceled.")
		}
		if !isYes(text) {
			fmt.Println("  Please answer y or n.")
			continue
		}
		break
	}

	return nil
}

func boldPrompt(text string) string {
	return promptAnsiBold + text + promptAnsiReset
}

func isYes(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "y" || s == "yes"
}

func isNo(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "n" || s == "no"
}
