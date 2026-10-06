package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/carddrift"
	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/shopspring/decimal"
)

const cardDriftTimeout = 5 * time.Second

func checkRegistryDrift(ctx context.Context, card map[string]any, cardArg string) (drifted bool) {
	lookupCtx, cancel := context.WithTimeout(ctx, cardDriftTimeout)
	defer cancel()

	client, reason := driftClient(lookupCtx)
	if reason != "" {
		fmt.Printf("[SKIP] Registry comparison skipped: %s\n", reason)
		return false
	}
	target := clictx.TargetName()
	agentName := cardAgentName(card)

	registered, err := cardfetch.Fetch(lookupCtx, client, agentName)
	if errors.Is(err, cardfetch.ErrAgentNotFound) {
		fmt.Printf("[INFO] %s is not registered on %s yet.\n", termsafe.Text(agentName), target)
		fmt.Printf("       To register it privately, run `%s`.\n", withCardArg("blocks register", cardArg))
		return false
	}
	// Decoded as float64 on purpose: publish sends the float64-decoded card and the
	// backend parses it as JS numbers, so the registry never holds more precision.
	var remote map[string]any
	if err == nil {
		err = json.Unmarshal(registered.Card, &remote)
	}
	if err != nil {
		fmt.Printf("[SKIP] Registry comparison skipped: could not fetch %s from %s\n", termsafe.Text(agentName), target)
		return false
	}

	paths := carddrift.Diff(card, remote)
	if len(paths) == 0 {
		fmt.Printf("[OK] Card matches the version registered on %s\n", target)
		return false
	}
	printDrift(paths, card, registered, target, cardArg)
	return true
}

func printDrift(paths []string, card map[string]any, registered *cardfetch.AgentCard, target, cardArg string) {
	fmt.Fprintf(os.Stderr, "[WARN] agent-card.json differs from the version registered on %s:\n", target)
	for _, p := range paths {
		fmt.Fprintf(os.Stderr, "         %s\n", termsafe.Text(p))
	}
	advice := syncCommand(card, registered)
	fmt.Fprintf(os.Stderr, "       Callers see the registered version until you run `%s`.\n", withCardArg(advice.command, cardArg))
	if advice.needsNewPricing {
		fmt.Fprintln(os.Stderr, "       Its registered prices do not cover the card's task kinds, so publish asks for new ones.")
	}
	if len(advice.pricing) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "       To keep its pricing, pass the values registered on %s:\n", target)
	for _, v := range advice.pricing {
		fmt.Fprintf(os.Stderr, "         <%s>  %s\n", v.flag, termsafe.Text(v.value))
	}
}

// No credential skips rather than looking up anonymously: an anonymous lookup of a
// private agent is a 404, which would be misreported as "not registered".
func driftClient(ctx context.Context) (*blocksapi.Client, string) {
	c := clictx.EffectiveCredential()
	switch {
	case c.Err != nil:
		return nil, "no usable credential"
	case c.Expired:
		return nil, "your login has expired (run `blocks login`)"
	case c.Key == "":
		return nil, "not logged in (run `blocks login`)"
	}
	backend, err := backendURLWithin(ctx)
	switch {
	case err != nil:
		return nil, "could not resolve the deployment"
	case backend == "":
		return nil, "no deployment configured"
	}
	return blocksapi.NewClient(backend, c.Key), ""
}

// The remote config fetch has its own longer timeout and retry; on our deadline it is
// abandoned, not cancelled.
func backendURLWithin(ctx context.Context) (string, error) {
	type resolved struct {
		url string
		err error
	}
	done := make(chan resolved, 1)
	go func() {
		url, err := clictx.EffectiveBackendURL()
		done <- resolved{trimURL(url), err}
	}()
	select {
	case r := <-done:
		return r.url, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type registeredValue struct {
	flag  string
	value string
}

type syncAdvice struct {
	command         string
	pricing         []registeredValue
	needsNewPricing bool
}

// Registry values stay off the command line (command_shaped_output_test.go): pricing
// flags carry placeholders, and the registered values are returned to print below it.
func syncCommand(card map[string]any, registered *cardfetch.AgentCard) syncAdvice {
	listing, billingMode := registered.Listing, registered.BillingMode
	switch {
	case listing != "private" && listing != "public", billingMode != "free" && billingMode != "paid":
		return syncAdvice{command: "blocks publish"}
	case listing == "private" && billingMode == "free":
		return syncAdvice{command: "blocks register"}
	case billingMode == "free":
		return syncAdvice{command: "blocks publish --listing " + listing + " --billing-mode free"}
	}
	advice := syncAdvice{command: "blocks publish --listing " + listing + " --billing-mode paid"}
	advice.pricing = registeredPricing(card, registered.Pricing)
	advice.needsNewPricing = len(advice.pricing) == 0
	for _, v := range advice.pricing {
		advice.command += " --" + v.flag + " <" + v.flag + ">"
	}
	return advice
}

// Carried over only while it prices one of the card's task kinds above zero: publish
// refuses a paid agent without such a price, so it must ask for new ones instead.
func registeredPricing(card map[string]any, p cardfetch.Pricing) []registeredValue {
	isStreaming, isRequest := deriveTaskKinds(card)
	if !(isRequest && pricePositive(p.PricePerTask)) && !(isStreaming && pricePositive(p.PricePerMinute)) {
		return nil
	}
	var values []registeredValue
	add := func(applies bool, flag, value string) {
		if applies && value != "" {
			values = append(values, registeredValue{flag, value})
		}
	}
	add(isRequest, "price-per-task", p.PricePerTask)
	add(isStreaming, "price-per-minute", p.PricePerMinute)
	add(isRequest, "free-tasks", formatOptionalInt(p.FreeTasksPerConsumer))
	add(isStreaming, "free-minutes", formatOptionalInt(p.FreeMinutesPerConsumer))
	return values
}

func pricePositive(price string) bool {
	v, err := decimal.NewFromString(price)
	return err == nil && v.Sign() > 0
}

func formatOptionalInt(n *int) string {
	if n == nil {
		return ""
	}
	return strconv.Itoa(*n)
}

func withCardArg(command, cardArg string) string {
	if cardArg == "" {
		return command
	}
	return command + " <path>"
}

func cardAgentName(card map[string]any) string {
	identity, _ := card["identity"].(map[string]any)
	name, _ := identity["agentName"].(string)
	return name
}
