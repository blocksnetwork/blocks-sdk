package partners

import (
	"context"
	"fmt"
	"net/http"
)

// checkTokenHTTP verifies that token is accepted by the provider at verifyURL.
// A 2xx response is valid; anything else returns an error with the HTTP status.
func checkTokenHTTP(ctx context.Context, verifyURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("token verification failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token rejected by provider (HTTP %d) — check scopes and try again", resp.StatusCode)
	}
	return nil
}
