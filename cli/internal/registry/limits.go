package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

type PricingLimits struct {
	MinPricePerTask       string `json:"minPricePerTask"`
	MinPricePerMinute     string `json:"minPricePerMinute"`
	MaxPricePerTask       string `json:"maxPricePerTask"`
	MaxPricePerMinute     string `json:"maxPricePerMinute"`
	MaxFreeTasksAllowed   int    `json:"maxFreeTasksAllowed"`
	MaxFreeMinutesAllowed int    `json:"maxFreeMinutesAllowed"`
}

type pricingLimitsWire struct {
	MinPricePerTask       string `json:"minPricePerTask"`
	MinPricePerMinute     string `json:"minPricePerMinute"`
	MaxPricePerTask       string `json:"maxPricePerTask"`
	MaxPricePerMinute     string `json:"maxPricePerMinute"`
	MaxFreeTasksAllowed   *int   `json:"maxFreeTasksAllowed"`
	MaxFreeMinutesAllowed *int   `json:"maxFreeMinutesAllowed"`
}

func DefaultPricingLimits() PricingLimits {
	return PricingLimits{
		MinPricePerTask:       MinPricePerTask,
		MinPricePerMinute:     MinPricePerMinute,
		MaxPricePerTask:       MaxPricePerTask,
		MaxPricePerMinute:     MaxPricePerMinute,
		MaxFreeTasksAllowed:   MaxFreeTasksPerConsumer,
		MaxFreeMinutesAllowed: MaxFreeMinutesPerConsumer,
	}
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func FetchPricingLimits(backendURL string) PricingLimits {
	if backendURL == "" {
		return DefaultPricingLimits()
	}

	url := fmt.Sprintf("%s/api/v1/pricing/limits", backendURL)
	resp, err := httpClient.Get(url)
	if err != nil {
		return DefaultPricingLimits()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DefaultPricingLimits()
	}

	var wire pricingLimitsWire
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return DefaultPricingLimits()
	}

	limits, ok := wire.limits()
	if !ok {
		return DefaultPricingLimits()
	}
	return limits
}

// limits converts a decoded response into bounds, or reports that it cannot.
//
// It is all-or-nothing on purpose. The previous overlay applied each field only when
// present, so a response carrying three of the six left the other three at this build's
// compiled-in defaults — bounds from two different sources, presented to the user as one
// deployment's pricing rules, with no way to tell which came from where. The response
// schema makes all six required, so a body missing any of them is not this endpoint's
// answer and the honest reading is to use none of it.
//
// The four prices are also checked as decimals rather than trusted as strings. They are
// compared against a typed price later, so an unparseable bound would not be rejected
// there — it would silently fail to constrain, which is the direction that matters for a
// value the deployment supplies.
func (w pricingLimitsWire) limits() (PricingLimits, bool) {
	if w.MaxFreeTasksAllowed == nil || w.MaxFreeMinutesAllowed == nil {
		return PricingLimits{}, false
	}
	for _, price := range []string{w.MinPricePerTask, w.MinPricePerMinute, w.MaxPricePerTask, w.MaxPricePerMinute} {
		if price == "" {
			return PricingLimits{}, false
		}
		if _, err := decimal.NewFromString(price); err != nil {
			return PricingLimits{}, false
		}
	}
	return PricingLimits{
		MinPricePerTask:       w.MinPricePerTask,
		MinPricePerMinute:     w.MinPricePerMinute,
		MaxPricePerTask:       w.MaxPricePerTask,
		MaxPricePerMinute:     w.MaxPricePerMinute,
		MaxFreeTasksAllowed:   *w.MaxFreeTasksAllowed,
		MaxFreeMinutesAllowed: *w.MaxFreeMinutesAllowed,
	}, true
}
