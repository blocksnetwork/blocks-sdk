package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type listAgentsResponse struct {
	Agents []Agent `json:"agents"`
	Next   *string `json:"next"`
}

type singleAgentResponse struct {
	Agent Agent `json:"agent"`
}

// FetchPlaygroundAgents fetches the user's owned playground agents with full card data.
// Handles cursor-based pagination internally.
func FetchPlaygroundAgents(ctx context.Context, backendURL, apiKey string) ([]Agent, error) {
	var all []Agent
	cursor := ""

	for {
		params := url.Values{
			"scope":   {"owned"},
			"listing": {"playground"},
			"include": {"full"},
			"limit":   {"100"},
		}
		if cursor != "" {
			params.Set("cursor", cursor)
		}

		u := fmt.Sprintf("%s/api/v1/registry/agents?%s", backendURL, params.Encode())
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Blocks-Protocol-Version", ProtocolVersion)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("failed to fetch agents: HTTP %d", resp.StatusCode)
		}

		var result listAgentsResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}

		all = append(all, result.Agents...)

		if result.Next == nil || *result.Next == "" {
			break
		}
		cursor = *result.Next
	}

	return all, nil
}

// FetchAgent fetches a single agent by name with full card data.
func FetchAgent(ctx context.Context, backendURL, apiKey, agentName string) (*Agent, error) {
	params := url.Values{
		"agentName": {agentName},
		"include":   {"full"},
	}
	u := fmt.Sprintf("%s/api/v1/registry/agents?%s", backendURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Blocks-Protocol-Version", ProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to fetch agent: HTTP %d", resp.StatusCode)
	}

	var result singleAgentResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result.Agent, nil
}
