package blocksapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/registry"
)

// helper creates a test server and a client pointed at it.
func newTestClient(handler http.Handler) (*Client, *httptest.Server) {
	ts := httptest.NewServer(handler)
	c := NewClient(ts.URL, "test-api-key")
	return c, ts
}

func TestGet_HappyPath(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer ts.Close()

	resp, err := c.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPost_HappyPath(t *testing.T) {
	var received map[string]string
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"created"}`)
	}))
	defer ts.Close()

	resp, err := c.Post(context.Background(), "/test", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if received["hello"] != "world" {
		t.Errorf("body not encoded correctly: %v", received)
	}
}

func TestPatch_HappyPath(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	resp, err := c.Patch(context.Background(), "/test", map[string]bool{"enabled": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestDelete_HappyPath(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	resp, err := c.Delete(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestDoJSON_DecodesResponseIntoOut(t *testing.T) {
	type payload struct {
		Value string `json:"value"`
	}
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"value":"hello"}`)
	}))
	defer ts.Close()

	var out payload
	if err := c.DoJSON(context.Background(), http.MethodGet, "/test", nil, &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Value != "hello" {
		t.Errorf("expected 'hello', got %q", out.Value)
	}
}

func TestAuthHeader_PresentWhenKeySet(t *testing.T) {
	var gotAuth string
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	resp, err := c.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer test-api-key" {
		t.Errorf("expected 'Bearer test-api-key', got %q", gotAuth)
	}
}

func TestAuthHeader_AbsentWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{}`)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "") // no API key
	resp, err := c.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "" {
		t.Errorf("expected no Authorization header, got %q", gotAuth)
	}
}

func TestProtocolVersionHeader_SetOnEveryRequest(t *testing.T) {
	methods := []struct {
		name string
		fn   func(c *Client) error
	}{
		{"GET", func(c *Client) error {
			r, err := c.Get(context.Background(), "/x", nil)
			if r != nil {
				r.Body.Close()
			}
			return err
		}},
		{"POST", func(c *Client) error {
			r, err := c.Post(context.Background(), "/x", nil)
			if r != nil {
				r.Body.Close()
			}
			return err
		}},
		{"PATCH", func(c *Client) error {
			r, err := c.Patch(context.Background(), "/x", nil)
			if r != nil {
				r.Body.Close()
			}
			return err
		}},
		{"DELETE", func(c *Client) error {
			r, err := c.Delete(context.Background(), "/x", nil)
			if r != nil {
				r.Body.Close()
			}
			return err
		}},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			var got string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("Blocks-Protocol-Version")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, `{}`)
			}))
			defer ts.Close()

			c := NewClient(ts.URL, "key")
			if err := m.fn(c); err != nil {
				t.Fatalf("%s: unexpected error: %v", m.name, err)
			}
			if got != registry.ProtocolVersion {
				t.Errorf("%s: expected protocol version %q, got %q", m.name, registry.ProtocolVersion, got)
			}
		})
	}
}

func TestNon2xx_ReturnsAPIError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		body       string
		wantMsg    string
		wantCode   string
	}{
		{
			name:       "400 error field",
			statusCode: 400,
			body:       `{"error":"bad request","code":"INVALID"}`,
			wantMsg:    "bad request",
			wantCode:   "INVALID",
		},
		{
			name:       "401 message fallback",
			statusCode: 401,
			body:       `{"message":"unauthorized"}`,
			wantMsg:    "unauthorized",
		},
		{
			name:       "403 error field",
			statusCode: 403,
			body:       `{"error":"forbidden"}`,
			wantMsg:    "forbidden",
		},
		{
			name:       "500 raw body fallback",
			statusCode: 500,
			body:       "internal server error",
			wantMsg:    "internal server error",
		},
		{
			name:       "404 empty body",
			statusCode: 404,
			body:       "",
			wantMsg:    "404 Not Found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				io.WriteString(w, tc.body)
			}))
			defer ts.Close()

			_, err := c.Get(context.Background(), "/test", nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T", err)
			}
			if apiErr.StatusCode != tc.statusCode {
				t.Errorf("expected status %d, got %d", tc.statusCode, apiErr.StatusCode)
			}
			if !strings.Contains(apiErr.Message, tc.wantMsg) {
				t.Errorf("expected message to contain %q, got %q", tc.wantMsg, apiErr.Message)
			}
			if tc.wantCode != "" && apiErr.Code != tc.wantCode {
				t.Errorf("expected code %q, got %q", tc.wantCode, apiErr.Code)
			}
		})
	}
}

func TestRetryAfter_ParsedFrom429(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer ts.Close()

	_, err := c.Post(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", apiErr.StatusCode)
	}
	if apiErr.RetryAfter != 30*time.Second {
		t.Errorf("expected 30s, got %v", apiErr.RetryAfter)
	}
}

func TestRetryAfter_NonIntegerIgnored(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "Thu, 01 Jan 2026 00:00:00 GMT")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer ts.Close()

	_, err := c.Post(context.Background(), "/test", nil)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.RetryAfter != 0 {
		t.Errorf("expected 0 for non-integer Retry-After, got %v", apiErr.RetryAfter)
	}
}

func TestProtocolVersionReject_412_ClearError(t *testing.T) {
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		io.WriteString(w, `{"error":"unsupported protocol version"}`)
	}))
	defer ts.Close()

	_, err := c.Post(context.Background(), "/test", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError (not panic/nil), got %T", err)
	}
	if apiErr.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Message, "unsupported protocol version") {
		t.Errorf("expected clear error message, got %q", apiErr.Message)
	}
}

func TestErrorDetails_ParsedFromValidationError(t *testing.T) {
	// Zod validation errors use { "error": "Validation error", "details": [...] }
	c, ts := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"Validation error","details":[{"path":["agents"],"message":"Required"}]}`)
	}))
	defer ts.Close()

	_, err := c.Post(context.Background(), "/test", nil)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Details == nil {
		t.Error("expected Details to be populated for validation error")
	}
}

func TestNewClient_DefaultBaseURL(t *testing.T) {
	c := NewClient("", "key")
	if c.BaseURL != defaultBaseURL {
		t.Errorf("expected default base URL %q, got %q", defaultBaseURL, c.BaseURL)
	}
}

func TestNewClient_TrailingSlashTrimmed(t *testing.T) {
	c := NewClient("https://example.com/", "key")
	if c.BaseURL != "https://example.com" {
		t.Errorf("expected trailing slash trimmed, got %q", c.BaseURL)
	}
}
