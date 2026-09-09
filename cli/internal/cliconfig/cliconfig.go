// Package cliconfig fetches the deployment's non-secret CLI discovery payload
// from GET /api/v1/cli-config. The endpoint is unauthenticated and exempt from
// protocol-version enforcement, so the CLI can call it before any credential
// exists. A backend that predates the endpoint (404) is treated as a
// non-enterprise zero value rather than an error.
package cliconfig

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Config is the non-secret discovery payload from GET /api/v1/cli-config.
// JSON tags mirror the wire contract byte-for-byte; absent optional keys
// decode to "" (absent ≡ empty for the consumer).
type Config struct {
	Enterprise       bool   `json:"enterprise"`
	ProductName      string `json:"productName"`
	OAuthClientID    string `json:"oauthClientId"`
	DashboardBaseURL string `json:"dashboardBaseUrl"`
}

// HTTPStatusError represents an HTTP error response from the cli-config endpoint.
// This allows error classification in cmd/login.go for better user messages.
type HTTPStatusError struct {
	StatusCode int
}

func (e HTTPStatusError) Error() string {
	return fmt.Sprintf("cli-config fetch failed: HTTP %d", e.StatusCode)
}

// DecodeError represents a JSON decode error from the cli-config endpoint.
// This allows error classification in cmd/login.go for better user messages.
type DecodeError struct {
	Err error
}

func (e DecodeError) Error() string {
	return fmt.Sprintf("cli-config parse failed: %v", e.Err)
}

func (e DecodeError) Unwrap() error {
	return e.Err
}

var client = &http.Client{
	Timeout: 10 * time.Second,
	// Mirror cdm.go: disable HTTP/2 (h2 hangs on some Windows TLS stacks).
	Transport: &http.Transport{
		Proxy:        http.ProxyFromEnvironment,
		TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
	},
}

// Fetch returns the deployment's CLI discovery config. A missing endpoint
// (older backend, 404) resolves to a non-enterprise zero value rather than an
// error, so the CLI degrades gracefully against pre-cli-config backends. The
// request carries no Authorization or protocol-version header.
func Fetch(baseURL string) (*Config, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		return &Config{}, nil
	}
	resp, err := client.Get(base + "/api/v1/cli-config")
	if err != nil {
		return nil, fmt.Errorf("cli-config fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &Config{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, HTTPStatusError{StatusCode: resp.StatusCode}
	}
	var cfg Config
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, DecodeError{Err: err}
	}
	return &cfg, nil
}
