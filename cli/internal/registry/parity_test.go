package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMinPriceParityWithBackend(t *testing.T) {
	tsPath := filepath.Join("..", "..", "..", "..", "afui_mvp_backend", "src", "modules", "billing", "billing.constants.ts")
	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("cannot read billing.constants.ts: %v", err)
	}
	src := string(data)

	taskRe := regexp.MustCompile(`MIN_PRICE_PER_TASK\s*=\s*'([^']+)'`)
	minuteRe := regexp.MustCompile(`MIN_PRICE_PER_MINUTE\s*=\s*'([^']+)'`)

	taskMatch := taskRe.FindStringSubmatch(src)
	if taskMatch == nil {
		t.Fatal("MIN_PRICE_PER_TASK not found in billing.constants.ts")
	}
	minuteMatch := minuteRe.FindStringSubmatch(src)
	if minuteMatch == nil {
		t.Fatal("MIN_PRICE_PER_MINUTE not found in billing.constants.ts")
	}

	if taskMatch[1] != MinPricePerTask {
		t.Errorf("MinPricePerTask = %q, backend = %q", MinPricePerTask, taskMatch[1])
	}
	if minuteMatch[1] != MinPricePerMinute {
		t.Errorf("MinPricePerMinute = %q, backend = %q", MinPricePerMinute, minuteMatch[1])
	}
}

// TestPromotionInputBillingModeInJSON asserts that BillingMode is present
// in the JSON-serialised PromotionInput so the publish payload always carries it.
func TestPromotionInputBillingModeInJSON(t *testing.T) {
	listing := "public"
	billingMode := "paid"
	price := "0.15"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		Price:       &price,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	bm, ok := m["billingMode"]
	if !ok {
		t.Fatal("billingMode field missing from PromotionInput JSON")
	}
	if bm != "paid" {
		t.Errorf("billingMode = %v, want paid", bm)
	}
}

// TestPromotionInputFreeBillingModeInJSON asserts billingMode=free is serialised.
func TestPromotionInputFreeBillingModeInJSON(t *testing.T) {
	listing := "private"
	billingMode := "free"
	flags := PromotionFlags{
		Listing:     &listing,
		BillingMode: &billingMode,
		AcceptTerms: true,
	}

	input, err := CollectPromotionInput(false, true, flags, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	b, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	bm, ok := m["billingMode"]
	if !ok {
		t.Fatal("billingMode field missing from PromotionInput JSON for free billing")
	}
	if bm != "free" {
		t.Errorf("billingMode = %v, want free", bm)
	}
}

// TestNoCopyPrivateRequiresPricing asserts that the "private requires pricing"
// string does not appear anywhere in the CLI source files owned by this package.
func TestNoCopyPrivateRequiresPricing(t *testing.T) {
	forbidden := "private requires pricing"
	files := []string{"prompt.go", "types.go"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("cannot read %s: %v", f, err)
		}
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Errorf("forbidden copy %q found in %s", forbidden, f)
		}
	}
}
