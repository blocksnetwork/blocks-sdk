package blocksapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/registry"
)

const defaultBaseURL = "https://blocks.api.pubnub.com"

// Client is a shared HTTP helper for outbound Blocks-backend calls.
// It auto-attaches Authorization and Blocks-Protocol-Version headers
// on every request and parses non-2xx responses into *APIError.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient returns a Client. If baseURL is empty, defaultBaseURL is used.
func NewClient(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		APIKey:     apiKey,
		HTTPClient: &http.Client{},
	}
}

// APIError is returned for non-2xx HTTP responses.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    interface{}
	RetryAfter time.Duration // populated from Retry-After header on 429
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// Get issues a GET request to path with optional query parameters.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	fullURL := c.BaseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	c.setCommonHeaders(req, false)
	return c.do(req)
}

// Post issues a POST request with a JSON-encoded body.
func (c *Client) Post(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	return c.doWithBody(ctx, http.MethodPost, path, body)
}

// Patch issues a PATCH request with a JSON-encoded body.
func (c *Client) Patch(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	return c.doWithBody(ctx, http.MethodPatch, path, body)
}

// Delete issues a DELETE request. body may be nil.
func (c *Client) Delete(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	return c.doWithBody(ctx, http.MethodDelete, path, body)
}

// Do issues an arbitrary method request. body is JSON-encoded when non-nil.
func (c *Client) Do(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	return c.doWithBody(ctx, method, path, body)
}

// DoJSON calls Do and JSON-decodes a 2xx response body into out.
// Returns *APIError for non-2xx responses.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, out interface{}) error {
	resp, err := c.doWithBody(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// doWithBody encodes body as JSON (when non-nil) and issues the request.
func (c *Client) doWithBody(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	hasBody := body != nil
	if hasBody {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("blocksapi: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	c.setCommonHeaders(req, hasBody)
	return c.do(req)
}

// setCommonHeaders attaches the Authorization and Blocks-Protocol-Version headers.
// Content-Type is set only when the request carries a body.
func (c *Client) setCommonHeaders(req *http.Request, hasBody bool) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("Blocks-Protocol-Version", registry.ProtocolVersion)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
}

// do executes the prepared request. Non-2xx responses are converted to *APIError.
// On success, the *http.Response body is left open for the caller to read/close.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}

	// Non-2xx: read body and build *APIError.
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	apiErr := &APIError{StatusCode: resp.StatusCode}

	// Parse Retry-After header for 429 responses.
	if resp.StatusCode == http.StatusTooManyRequests {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				apiErr.RetryAfter = time.Duration(secs) * time.Second
			}
		}
	}

	// Try to decode the backend's error envelope shapes.
	//
	// Primary shape (the Blocks backend's shared error envelope):
	//   { "error": "<message>", "code": "<code>", "details": [...] }
	// Fallback shape (legacy endpoints):
	//   { "message": "<message>" }
	var envelope struct {
		Error   interface{} `json:"error"`
		Code    string      `json:"code"`
		Message string      `json:"message"`
		Details interface{} `json:"details"`
	}

	if len(raw) > 0 && json.Unmarshal(raw, &envelope) == nil {
		switch v := envelope.Error.(type) {
		case string:
			apiErr.Message = v
		case map[string]interface{}:
			// Nested object — unlikely per the backend but handle gracefully.
			if msg, ok := v["message"].(string); ok {
				apiErr.Message = msg
			}
		}
		if apiErr.Message == "" {
			apiErr.Message = envelope.Message
		}
		apiErr.Code = envelope.Code
		apiErr.Details = envelope.Details
	}

	// Final fallback: raw body as text.
	if apiErr.Message == "" {
		if len(raw) > 0 {
			apiErr.Message = strings.TrimSpace(string(raw))
		} else {
			apiErr.Message = resp.Status
		}
	}

	return nil, apiErr
}
