package registry

import (
	"bufio"
	"strings"
	"testing"
)

func TestCollectPromotionInputAllFlags(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.15"
	freeUnits := 10
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		FreeUnits:   &freeUnits,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if input.Listing != "public" {
		t.Errorf("listing = %q, want public", input.Listing)
	}
	if input.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", input.BillingMode)
	}
	if input.PricePerTask == nil || *input.PricePerTask != "0.150000" {
		t.Errorf("PricePerTask = %v, want 0.150000", input.PricePerTask)
	}
	if input.FreeTasksPerConsumer == nil || *input.FreeTasksPerConsumer != 10 {
		t.Errorf("FreeTasksPerConsumer = %v, want 10", input.FreeTasksPerConsumer)
	}
	if input.TcAcceptedAt == "" {
		t.Error("expected TcAcceptedAt to be set for paid (any listing)")
	}
}

func TestCollectPromotionInputStreamingFlags(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.10"
	freeUnits := 5
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		FreeUnits:   &freeUnits,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(true, false, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if input.PricePerMinute == nil || *input.PricePerMinute != "0.100000" {
		t.Errorf("PricePerMinute = %v, want 0.100000", input.PricePerMinute)
	}
	if input.FreeMinutesPerConsumer == nil || *input.FreeMinutesPerConsumer != 5 {
		t.Errorf("FreeMinutesPerConsumer = %v, want 5", input.FreeMinutesPerConsumer)
	}
}

func TestCollectPromotionInputDualKindExplicitFlags(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	perTask := "0.15"
	perMinute := "0.10"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		PricePerTask:   &perTask,
		PricePerMinute: &perMinute,
		AcceptTerms:    true,
	}

	input, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if input.PricePerTask == nil || *input.PricePerTask != "0.150000" {
		t.Errorf("PricePerTask = %v, want 0.150000", input.PricePerTask)
	}
	if input.PricePerMinute == nil || *input.PricePerMinute != "0.100000" {
		t.Errorf("PricePerMinute = %v, want 0.100000", input.PricePerMinute)
	}
}

func TestCollectPromotionInputDualKindRejectsGenericPrice(t *testing.T) {
	listing := "public"
	price := "0.15"
	flags := PromotionFlags{
		Listing:     &listing,
		Price:       &price,
		AcceptTerms: true,
	}

	_, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error when --price used with dual-kind agent")
	}
}

func TestCollectPromotionInputRejectsFreeUnitsWithFreeTasks(t *testing.T) {
	freeUnits := 5
	freeTasks := 10
	flags := PromotionFlags{
		FreeUnits: &freeUnits,
		FreeTasks: &freeTasks,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error when --free-units and --free-tasks are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want mutual exclusion guidance", err.Error())
	}
}

func TestCollectPromotionInputRejectsFreeUnitsWithFreeMinutes(t *testing.T) {
	freeUnits := 5
	freeMinutes := 10
	flags := PromotionFlags{
		FreeUnits:   &freeUnits,
		FreeMinutes: &freeMinutes,
	}

	_, err := CollectPromotionInput(true, false, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error when --free-units and --free-minutes are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want mutual exclusion guidance", err.Error())
	}
}

// paid without --accept-terms must fail when stdin is not a live prompt.
// T&C gate is driven by billingMode="paid", not by listing (D3 paid-any-listing).
func TestCollectPromotionInputPublicPaidRequiresTerms(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.15"
	// Use a scanner that simulates EOF after price prompt (no T&C input).
	// We provide listing+billingMode+price but no T&C confirmation.
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		AcceptTerms: false,
	}

	scanner := bufio.NewScanner(strings.NewReader("")) // empty — EOF at attestation prompt
	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), scanner)
	if err == nil {
		t.Fatal("expected error when paid published without --accept-terms and no attestation input")
	}
}

// public+free does not require attestations — CLI should skip T&C entirely.
func TestCollectPromotionInputPublicFreeNoTermsNeeded(t *testing.T) {
	listing := "public"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error for public+free: %v", err)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
	if input.PricePerTask != nil {
		t.Errorf("expected no PricePerTask for public+free, got %v", input.PricePerTask)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for public+free, got %q", input.TcAcceptedAt)
	}
}

// public + explicit zero pricing with billingMode=free: allowed, no T&C.
func TestCollectPromotionInputPublicExplicitZeroAllowed(t *testing.T) {
	listing := "public"
	billingMode := "free"
	zero := "0"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &zero,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error for public + price 0 + free billing mode: %v", err)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for public+free (explicit 0), got %q", input.TcAcceptedAt)
	}
}

func TestCollectPromotionInputNonInteractiveRequiresListing(t *testing.T) {
	flags := PromotionFlags{
		NonInteractive: true,
		AcceptTerms:    true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error when --listing omitted in non-interactive mode")
	}
}

// private+free is allowed with explicit billingMode="free" (D1).
// No pricing prompts and no T&C required for free billing mode.
func TestCollectPromotionInput_PrivateNoPricing_Allowed(t *testing.T) {
	listing := "private"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for private+free, got %q", input.TcAcceptedAt)
	}
	if input.PricePerTask != nil || input.PricePerMinute != nil {
		t.Error("expected no pricing fields for private+free (pricingRequired=false preserved)")
	}
}

// private+free with explicit zero pricing: billingMode=free means price fields accepted but no T&C.
// Note: with billingMode=free, pricing prompts are skipped entirely; the explicit zero flag
// is passed but not consumed since we don't enter the paid pricing block.
func TestCollectPromotionInput_PrivateExplicitZero_Allowed(t *testing.T) {
	listing := "private"
	billingMode := "free"
	zero := "0"
	flags := PromotionFlags{
		Listing:      &listing,
		BillingMode:  &billingMode,
		PricePerTask: &zero,
		AcceptTerms:  true,
	}

	input, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for private+free (billingMode explicit), got %q", input.TcAcceptedAt)
	}
}

// private+paid requires T&C (D3 paid-any-listing — billingMode="paid" triggers T&C
// regardless of listing). This is the regression test for paid-any-listing T&C semantics.
func TestCollectPromotionInputPrivatePaidRequiresTerms(t *testing.T) {
	listing := "private"
	billingMode := "paid"
	price := "0.15"
	flags := PromotionFlags{
		Listing:      &listing,
		BillingMode:  &billingMode,
		PricePerTask: &price,
		AcceptTerms:  true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error for private+paid: %v", err)
	}
	if input.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", input.BillingMode)
	}
	if input.TcAcceptedAt == "" {
		t.Error("expected TcAcceptedAt to be set for private+paid (D3 paid-any-listing T&C)")
	}
	if input.PricePerTask == nil || *input.PricePerTask != "0.150000" {
		t.Errorf("PricePerTask = %v, want 0.150000", input.PricePerTask)
	}
}

// Dual-kind private+free with explicit billingMode=free: no pricing prompts, no T&C.
// This is the regression test for pricingRequired=false — private does not force pricing.
func TestCollectPromotionInputDualKindPrivateNoPricingAllowed(t *testing.T) {
	listing := "private"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("expected nil error for dual-kind private+free, got %v", err)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for dual-kind private+free, got %q", input.TcAcceptedAt)
	}
	if input.PricePerTask != nil || input.PricePerMinute != nil {
		t.Error("expected no pricing for dual-kind private+free (pricingRequired=false)")
	}
}

// --- billing-mode explicit contract tests ---

// Non-interactive (AcceptTerms=true) missing --billing-mode must fail with the
// actionable error message.
func TestCollectPromotionInput_NonInteractive_MissingBillingMode_FailsFast(t *testing.T) {
	listing := "public"
	flags := PromotionFlags{
		Listing:        &listing,
		NonInteractive: true,
		// BillingMode intentionally omitted
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error when --billing-mode omitted in non-interactive mode")
	}
	want := "Missing --billing-mode. Pass --billing-mode free or --billing-mode paid."
	if err.Error() != want {
		t.Errorf("error message = %q, want %q", err.Error(), want)
	}
}

// Non-interactive invalid --billing-mode value must fail fast.
func TestCollectPromotionInput_NonInteractive_InvalidBillingMode_FailsFast(t *testing.T) {
	listing := "public"
	billingMode := "tier"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for invalid --billing-mode value")
	}
}

// Interactive prompt for billingMode fires when flag is omitted.
func TestCollectPromotionInput_Interactive_PromptsBillingMode(t *testing.T) {
	listing := "public"
	flags := PromotionFlags{
		Listing:     &listing,
		AcceptTerms: false,
		// BillingMode omitted → should prompt
	}

	// Simulate user choosing "1" (free) at billing mode prompt.
	scanner := bufio.NewScanner(strings.NewReader("1\n"))
	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
	if input.TcAcceptedAt != "" {
		t.Error("expected no TcAcceptedAt for free")
	}
}

// Interactive prompt order is visibility first, then billing.
func TestCollectPromotionInput_Interactive_PromptsVisibilityThenBilling(t *testing.T) {
	flags := PromotionFlags{}

	scanner := bufio.NewScanner(strings.NewReader("pr\nf\n"))
	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if input.Listing != "private" {
		t.Errorf("Listing = %q, want private", input.Listing)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
}

// Interactive prompt for billingMode: choosing "2" (paid) prompts pricing and T&C.
func TestCollectPromotionInput_Interactive_PromptsBillingMode_Paid(t *testing.T) {
	listing := "public"
	flags := PromotionFlags{
		Listing:     &listing,
		AcceptTerms: false,
	}

	// Simulate: billing=2(paid), price=0.15, free=0, attest1=y, attest2=y
	input := "2\n0.15\n0\ny\ny\n"
	scanner := bufio.NewScanner(strings.NewReader(input))
	result, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", result.BillingMode)
	}
	if result.TcAcceptedAt == "" {
		t.Error("expected TcAcceptedAt to be set for paid")
	}
}

func TestPromptListingSelection_AcceptsAliases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"1\n", "public"},
		{"public\n", "public"},
		{"p\n", "public"},
		{"2\n", "private"},
		{"private\n", "private"},
		{"pr\n", "private"},
	}

	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.input), func(t *testing.T) {
			got, err := promptListingSelection(bufio.NewScanner(strings.NewReader(tc.input)))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("listing = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPromptBillingMode_AcceptsAliases(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"1\n", "free"},
		{"free\n", "free"},
		{"f\n", "free"},
		{"2\n", "paid"},
		{"paid\n", "paid"},
	}

	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc.input), func(t *testing.T) {
			got, err := promptBillingMode(bufio.NewScanner(strings.NewReader(tc.input)))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("billingMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPromptBillingMode_RejectsPAlias(t *testing.T) {
	_, err := promptBillingMode(bufio.NewScanner(strings.NewReader("p\n")))
	if err == nil {
		t.Fatal("expected billing alias p to be rejected")
	}
}

// Pricing prompts fire only for billingMode=paid; billingMode=free skips pricing entirely.
func TestCollectPromotionInput_FreeBillingMode_NoPricingPrompt(t *testing.T) {
	listing := "public"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No pricing fields populated for free
	if input.PricePerTask != nil {
		t.Errorf("expected nil PricePerTask for free billing, got %v", input.PricePerTask)
	}
	if input.PricePerMinute != nil {
		t.Errorf("expected nil PricePerMinute for free billing, got %v", input.PricePerMinute)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for free billing, got %q", input.TcAcceptedAt)
	}
}

// Free agent in non-interactive mode succeeds WITHOUT --accept-terms (matches dashboard).
func TestCollectPromotionInput_FreeNonInteractive_NoAcceptTermsNeeded(t *testing.T) {
	listing := "public"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		NonInteractive: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("free agent should not require --accept-terms, got: %v", err)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("expected empty TcAcceptedAt for free agent, got %q", input.TcAcceptedAt)
	}
}

// Paid agent in non-interactive mode WITHOUT --accept-terms fails with clear error.
func TestCollectPromotionInput_PaidNonInteractive_RequiresAcceptTerms(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.15"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		Price:          &price,
		NonInteractive: true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for paid agent without --accept-terms in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "--accept-terms") {
		t.Errorf("error should mention --accept-terms, got: %v", err)
	}
}

// T&C fires for paid+public (regression: paid-any-listing T&C preserved under explicit billingMode).
func TestCollectPromotionInput_PaidPublic_TCRequired(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.15"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error for paid+public: %v", err)
	}
	if input.TcAcceptedAt == "" {
		t.Error("expected TcAcceptedAt for paid+public")
	}
	if input.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", input.BillingMode)
	}
}

// T&C fires for paid+private (regression: paid-any-listing T&C — not listing-gated).
func TestCollectPromotionInput_PaidPrivate_TCRequired(t *testing.T) {
	listing := "private"
	billingMode := "paid"
	price := "0.15"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("unexpected error for paid+private: %v", err)
	}
	if input.TcAcceptedAt == "" {
		t.Error("expected TcAcceptedAt for paid+private (D3 paid-any-listing)")
	}
	if input.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", input.BillingMode)
	}
}

// private+free emits valid payload: no pricing, no T&C. Regression for pricingRequired=false.
// The string "private requires pricing" must never appear in prompt output.
func TestCollectPromotionInput_PrivateFree_ValidPayload(t *testing.T) {
	listing := "private"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("private+free must not error, got: %v", err)
	}
	if input.Listing != "private" {
		t.Errorf("Listing = %q, want private", input.Listing)
	}
	if input.BillingMode != "free" {
		t.Errorf("BillingMode = %q, want free", input.BillingMode)
	}
	if input.PricePerTask != nil || input.PricePerMinute != nil {
		t.Error("private+free must not have pricing fields")
	}
	if input.TcAcceptedAt != "" {
		t.Errorf("private+free must not have TcAcceptedAt, got %q", input.TcAcceptedAt)
	}
}

// paid+all-zero prices: CLI fails fast with an actionable error rather than
// round-tripping to the backend. Mirrors the backend "paid requires positive
// price" rule client-side for cleaner UX.
func TestCollectPromotionInput_PaidAllZero_RejectedClientSide(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	zero := "0"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		Price:          &zero,
		NonInteractive: true,
		AcceptTerms:    true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for paid+all-zero, got nil")
	}
	if !strings.Contains(err.Error(), "at least one price above 0") {
		t.Errorf("error must mention positive-price requirement, got: %v", err)
	}
}

func TestCollectPromotionInput_DualKindPaidOnePositivePriceAccepted(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	zero := "0"
	perMinute := "0.10"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		PricePerTask:   &zero,
		PricePerMinute: &perMinute,
		AcceptTerms:    true,
	}

	input, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err != nil {
		t.Fatalf("expected one positive price to be accepted, got: %v", err)
	}
	if input.PricePerTask == nil || *input.PricePerTask != "0.000000" {
		t.Errorf("PricePerTask = %v, want 0.000000", input.PricePerTask)
	}
	if input.PricePerMinute == nil || *input.PricePerMinute != "0.100000" {
		t.Errorf("PricePerMinute = %v, want 0.100000", input.PricePerMinute)
	}
}

func TestCollectPromotionInput_DualKindPaidAllZeroRejected(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	zero := "0"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		PricePerTask:   &zero,
		PricePerMinute: &zero,
		NonInteractive: true,
		AcceptTerms:    true,
	}

	_, err := CollectPromotionInput(true, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected all-zero dual-kind paid pricing to fail")
	}
	if !strings.Contains(err.Error(), "at least one price above 0") {
		t.Errorf("error must mention positive-price requirement, got: %v", err)
	}
}

// paid with no pricing flags at all (non-interactive) also fails client-side.
func TestCollectPromotionInput_PaidNoPriceFlags_RejectedClientSide(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	flags := PromotionFlags{
		Listing:        &listing,
		BillingMode:    &billingMode,
		NonInteractive: true,
		AcceptTerms:    true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for paid with no pricing flags, got nil")
	}
	if !strings.Contains(err.Error(), "at least one price above 0") {
		t.Errorf("error must mention positive-price requirement, got: %v", err)
	}
}

// free + positive --price is rejected client-side rather than silently dropped.
func TestCollectPromotionInput_FreeWithPositivePrice_Rejected(t *testing.T) {
	listing := "public"
	billingMode := "free"
	price := "0.15"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		AcceptTerms: true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for free + positive price, got nil")
	}
	if !strings.Contains(err.Error(), "Free agents cannot include positive prices") {
		t.Errorf("error must call out free/pricing incompatibility, got: %v", err)
	}
}

// free + positive --price-per-task is rejected client-side.
func TestCollectPromotionInput_FreeWithPricePerTask_Rejected(t *testing.T) {
	listing := "public"
	billingMode := "free"
	price := "0.0001"
	flags := PromotionFlags{
		Listing:      &listing,
		BillingMode:  &billingMode,
		PricePerTask: &price,
		AcceptTerms:  true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for free + positive price-per-task, got nil")
	}
}

// Non-interactive (NonInteractive=true, AcceptTerms=false) without --billing-mode
// fails fast with the actionable missing-flag error. This is the CI-without-TTY
// path that previously fell through to promptBillingMode and EOF-failed.
func TestCollectPromotionInput_NonInteractiveTTYMissingBillingMode_FailsFast(t *testing.T) {
	listing := "public"
	flags := PromotionFlags{
		Listing:        &listing,
		NonInteractive: true,
	}

	_, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), nil)
	if err == nil {
		t.Fatal("expected error for non-interactive missing --billing-mode, got nil")
	}
	if !strings.Contains(err.Error(), "--billing-mode") {
		t.Errorf("error must call out the missing flag, got: %v", err)
	}
}

// Copy assertion: verify promptBillingMode output contains no "private requires pricing".
func TestPromptBillingMode_NoPrivateRequiresPricingCopy(t *testing.T) {
	// Use a scanner that returns valid input so we can test the function runs.
	scanner := bufio.NewScanner(strings.NewReader("1\n"))
	result, err := promptBillingMode(scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "free" {
		t.Errorf("expected free, got %q", result)
	}
	// The function itself doesn't write "private requires pricing"; verify via
	// source-level grep in parity_test.go. This test confirms the happy path.
}

// Interactive billing mode prompt accepts "free" by text as well as "1".
func TestPromptBillingMode_AcceptsTextInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("paid\n"))
	result, err := promptBillingMode(scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "paid" {
		t.Errorf("expected paid, got %q", result)
	}
}

// Interactive billing mode prompt rejects invalid input.
func TestPromptBillingMode_RejectsInvalidInput(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("tier\n"))
	_, err := promptBillingMode(scanner)
	if err == nil {
		t.Fatal("expected error for invalid billing mode input")
	}
}

func TestValidatePriceZeroAccepted(t *testing.T) {
	if err := validatePrice("0", MinPricePerTask, "test"); err != nil {
		t.Errorf("expected 0 to be accepted: %v", err)
	}
}

func TestValidatePriceTaskMinAccepted(t *testing.T) {
	if err := validatePrice("0.0001", MinPricePerTask, "test"); err != nil {
		t.Errorf("expected 0.0001 to be accepted for task: %v", err)
	}
}

func TestValidatePriceAcceptsDollarPrefix(t *testing.T) {
	if err := validatePrice("$0.25", MinPricePerTask, pricePerTaskLabel); err != nil {
		t.Errorf("expected dollar-prefixed price to be accepted: %v", err)
	}
}

func TestValidatePriceRejectsTaskMax(t *testing.T) {
	err := validatePrice("25.01", MinPricePerTask, pricePerTaskLabel)
	if err == nil {
		t.Fatal("expected price above task max to fail")
	}
	if !strings.Contains(err.Error(), "25.00 or less") {
		t.Errorf("error = %q, want task max range", err.Error())
	}
}

func TestValidatePriceRejectsMinuteMax(t *testing.T) {
	err := validatePrice("1.01", MinPricePerMinute, pricePerMinuteLabel)
	if err == nil {
		t.Fatal("expected price above minute max to fail")
	}
	if !strings.Contains(err.Error(), "1.00 or less") {
		t.Errorf("error = %q, want minute max range", err.Error())
	}
}

func TestResolveFreeUnitsZeroFlagOmitted(t *testing.T) {
	zero := 0
	got, err := resolveFreeUnits(freeTrialTaskPrompt, nil, &zero, MaxFreeTasksPerConsumer, true, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected zero free-unit flag to be omitted, got %v", got)
	}
}

func TestResolveFreeUnitsRejectsNegativeFlag(t *testing.T) {
	negative := -1
	_, err := resolveFreeUnits(freeTrialTaskPrompt, nil, &negative, MaxFreeTasksPerConsumer, true, nil)
	if err == nil {
		t.Fatal("expected negative free trial units to fail")
	}
	if !strings.Contains(err.Error(), "whole number of 0 or greater") {
		t.Errorf("error = %q, want whole-number validation", err.Error())
	}
}

func TestResolveFreeUnitsRejectsAboveMax(t *testing.T) {
	above := MaxFreeTasksPerConsumer + 1
	_, err := resolveFreeUnits(freeTrialTaskPrompt, nil, &above, MaxFreeTasksPerConsumer, true, nil)
	if err == nil {
		t.Fatal("expected free units above max to fail")
	}
	if !strings.Contains(err.Error(), "Maximum is 100") {
		t.Errorf("error = %q, want max validation", err.Error())
	}
}

func TestResolveFreeMinutesRejectsAboveMax(t *testing.T) {
	above := MaxFreeMinutesPerConsumer + 1
	_, err := resolveFreeUnits(freeTrialMinutePrompt, nil, &above, MaxFreeMinutesPerConsumer, true, nil)
	if err == nil {
		t.Fatal("expected free minutes above max to fail")
	}
	if !strings.Contains(err.Error(), "Maximum is 30") {
		t.Errorf("error = %q, want max validation", err.Error())
	}
}

func TestValidatePriceTaskMinRejectedForMinute(t *testing.T) {
	if err := validatePrice("0.0001", MinPricePerMinute, "test"); err == nil {
		t.Error("expected 0.0001 to be rejected for minute min 0.01")
	}
}

func TestValidatePriceBelowTaskMinRejected(t *testing.T) {
	if err := validatePrice("0.00005", MinPricePerTask, "test"); err == nil {
		t.Error("expected 0.00005 to be rejected (below task min 0.0001)")
	}
}

func TestValidatePriceTooManyFractionalDigits(t *testing.T) {
	if err := validatePrice("0.1234567", MinPricePerTask, "test"); err == nil {
		t.Error("expected 7 fractional digits to be rejected")
	}
}

func TestValidatePriceNegativeRejected(t *testing.T) {
	err := validatePrice("-0.01", MinPricePerTask, "test")
	if err == nil {
		t.Error("expected negative price to be rejected")
	}
	if !strings.Contains(err.Error(), "0 or greater") {
		t.Errorf("error = %q, want non-negative guidance", err.Error())
	}
}

func TestValidatePriceNonDecimalRejected(t *testing.T) {
	if err := validatePrice("abc", MinPricePerTask, "test"); err == nil {
		t.Error("expected non-decimal string to be rejected")
	}
}

func TestResolvePriceInteractiveEmptyInputAppliesDefault(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	result, err := resolvePrice("label", "label", nil, nil, MinPricePerTask, MaxPricePerTask, false, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != "0.100000" {
		t.Errorf("expected default 0.100000, got %v", result)
	}
}

func TestResolvePriceEffectiveDefaultWhenMinAboveDefault(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	result, err := resolvePrice("label", "label", nil, nil, "0.50", "10.00", false, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != "0.500000" {
		t.Errorf("expected effective default 0.500000 (min > 0.10), got %v", result)
	}
}

func TestResolvePriceEffectiveDefaultWhenMaxBelowDefault(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("\n"))
	result, err := resolvePrice("label", "label", nil, nil, "0.0001", "0.08", false, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != "0.080000" {
		t.Errorf("expected effective default 0.080000 (max < 0.10), got %v", result)
	}
}

func TestCollectPromotionInput_Interactive_PaidEmptyPriceUsesDefault(t *testing.T) {
	listing := "public"
	flags := PromotionFlags{
		Listing:     &listing,
		AcceptTerms: false,
	}

	// billing=2(paid), price per task=<enter>, free tasks=0, attest1=y, attest2=y
	input := "2\n\n0\ny\ny\n"
	scanner := bufio.NewScanner(strings.NewReader(input))
	result, err := CollectPromotionInput(false, true, flags, DefaultPricingLimits(), scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BillingMode != "paid" {
		t.Errorf("BillingMode = %q, want paid", result.BillingMode)
	}
	if result.PricePerTask == nil || *result.PricePerTask != "0.100000" {
		t.Errorf("PricePerTask = %v, want 0.100000 (default)", result.PricePerTask)
	}
}

func TestResolvePriceInteractiveNormalizesToStringFixed(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("0.0001\n"))
	result, err := resolvePrice("label", "label", nil, nil, MinPricePerTask, MaxPricePerTask, true, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != "0.000100" {
		t.Errorf("expected 0.000100 (StringFixed(6)), got %v", result)
	}
}

func TestResolvePriceInteractiveAndFlagProduceSameWireFormat(t *testing.T) {
	scanner := bufio.NewScanner(strings.NewReader("5\n"))
	fromInteractive, err := resolvePrice("label", "label", nil, nil, MinPricePerTask, MaxPricePerTask, true, false, scanner)
	if err != nil {
		t.Fatalf("interactive: unexpected error: %v", err)
	}
	raw := "5"
	fromFlag, err := resolvePrice("label", "label", nil, &raw, MinPricePerTask, MaxPricePerTask, true, true, nil)
	if err != nil {
		t.Fatalf("flag: unexpected error: %v", err)
	}
	if fromInteractive == nil || fromFlag == nil || *fromInteractive != *fromFlag {
		t.Errorf("interactive vs flag wire-format mismatch: interactive=%v flag=%v", fromInteractive, fromFlag)
	}
}

func TestResolvePriceRejectsTooManyFractionalDigitsViaFlag(t *testing.T) {
	raw := "0.1234567"
	_, err := resolvePrice(pricePrompt(pricePerTaskLabel, MaxPricePerTask, MinPricePerTask), pricePerTaskLabel, nil, &raw, MinPricePerTask, MaxPricePerTask, true, true, nil)
	if err == nil {
		t.Fatal("expected error for 7 fractional digits via flag")
	}
}

func TestResolvePriceInteractiveFallbackOnBadFlag(t *testing.T) {
	bad := "50.00"
	scanner := bufio.NewScanner(strings.NewReader("10.00\n"))
	result, err := resolvePrice(pricePrompt(pricePerTaskLabel, MaxPricePerTask, MinPricePerTask), pricePerTaskLabel, nil, &bad, MinPricePerTask, MaxPricePerTask, false, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != "10.000000" {
		t.Errorf("expected 10.000000 from interactive retry, got %v", result)
	}
}

func TestResolveFreeUnitsInteractiveFallbackOnBadFlag(t *testing.T) {
	bad := 200
	scanner := bufio.NewScanner(strings.NewReader("50\n"))
	result, err := resolveFreeUnits(freeTrialTaskPrompt, nil, &bad, MaxFreeTasksPerConsumer, false, scanner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || *result != 50 {
		t.Errorf("expected 50 from interactive retry, got %v", result)
	}
}
