package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Promote sends a PATCH request to promote an existing playground agent.
func Promote(ctx context.Context, backendURL, apiKey, agentName string, input PromotionInput) error {
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	u := fmt.Sprintf("%s/api/v1/registry/agents?agentName=%s", backendURL, url.QueryEscape(agentName))
	req, err := http.NewRequestWithContext(ctx, "PATCH", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Blocks-Protocol-Version", ProtocolVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 {
		return fmt.Errorf("authentication failed — try running 'blocks login' again")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp map[string]interface{}
		if json.Unmarshal(respBody, &errResp) == nil {
			if msg, ok := errResp["error"].(string); ok {
				return fmt.Errorf("promote failed: %s", msg)
			}
		}
		return fmt.Errorf("promote failed: HTTP %d", resp.StatusCode)
	}

	return nil
}
