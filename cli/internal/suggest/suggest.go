// Package suggest queries the Blocks registry autocomplete endpoint for
// agent-name suggestions. It is intentionally narrow: a single Agents entry
// point and a typed AgentSuggestion struct naming only the fields the wizard
// renders.
//
// The endpoint (GET /api/v1/registry/suggest) uses optionalAuth: with the
// CLI's API key attached (by the shared blocksapi client) it surfaces the
// caller's private and granted agents alongside public ones; anonymously it
// returns public agents only.
package suggest

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strconv"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

// minQueryLen mirrors the backend's suggestQuerySchema minimum (q.min(2)).
// Shorter queries are a no-op so the wizard can call Agents on every
// keystroke without round-tripping a guaranteed-400.
const minQueryLen = 2

// defaultLimit matches the endpoint's max for the agent field.
const defaultLimit = 10

// AgentSuggestion is one agent-name match from the registry.
type AgentSuggestion struct {
	AgentName   string
	DisplayName string
}

// suggestResponse is the subset of the /registry/suggest payload we read.
type suggestResponse struct {
	Agents []struct {
		AgentName   string `json:"agentName"`
		DisplayName string `json:"displayName"`
	} `json:"agents"`
}

// Agents returns agent-name suggestions for the partial query q. Queries
// shorter than minQueryLen return (nil, nil) without a network call. A nil
// client returns (nil, nil) so callers in environments without a backend
// (offline / unconfigured) degrade to free-text entry rather than erroring.
func Agents(ctx context.Context, client *blocksapi.Client, q string) ([]AgentSuggestion, error) {
	if client == nil || len(q) < minQueryLen {
		return nil, nil
	}

	query := url.Values{
		"q":     []string{q},
		"field": []string{"agentname"},
		"limit": []string{strconv.Itoa(defaultLimit)},
	}
	resp, err := client.Get(ctx, "/api/v1/registry/suggest", query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed suggestResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}

	out := make([]AgentSuggestion, 0, len(parsed.Agents))
	for _, a := range parsed.Agents {
		if a.AgentName == "" {
			continue
		}
		out = append(out, AgentSuggestion{
			AgentName:   a.AgentName,
			DisplayName: a.DisplayName,
		})
	}
	return out, nil
}
