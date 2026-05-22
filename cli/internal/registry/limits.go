package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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

	limits := DefaultPricingLimits()
	if wire.MinPricePerTask != "" {
		limits.MinPricePerTask = wire.MinPricePerTask
	}
	if wire.MinPricePerMinute != "" {
		limits.MinPricePerMinute = wire.MinPricePerMinute
	}
	if wire.MaxPricePerTask != "" {
		limits.MaxPricePerTask = wire.MaxPricePerTask
	}
	if wire.MaxPricePerMinute != "" {
		limits.MaxPricePerMinute = wire.MaxPricePerMinute
	}
	if wire.MaxFreeTasksAllowed != nil {
		limits.MaxFreeTasksAllowed = *wire.MaxFreeTasksAllowed
	}
	if wire.MaxFreeMinutesAllowed != nil {
		limits.MaxFreeMinutesAllowed = *wire.MaxFreeMinutesAllowed
	}

	return limits
}
