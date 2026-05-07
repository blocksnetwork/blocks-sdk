package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// EnsureCredentials checks for valid stored credentials. If missing or expired,
// runs the appropriate auth flow based on flags. Returns the API key string.
//
// Supports three paths:
//   - apiKeyDirect: use this API key directly (--api-key flag)
//   - apiKeyStdin: read API key from stdin (--api-key-stdin flag)
//   - Browser OAuth PKCE flow (default)
func EnsureCredentials(ctx context.Context, backendURL, clientID, apiKeyDirect string, apiKeyStdin bool) (string, error) {
	// Direct API key
	if apiKeyDirect != "" {
		creds := &Credentials{ApiKey: apiKeyDirect}
		if err := Save(creds); err != nil {
			return "", fmt.Errorf("failed to save credentials: %w", err)
		}
		return apiKeyDirect, nil
	}

	// Stdin API key
	if apiKeyStdin {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return "", fmt.Errorf("--api-key-stdin: no input received on stdin")
		}
		key := scanner.Text()
		if key == "" {
			return "", fmt.Errorf("--api-key-stdin: empty API key received")
		}
		creds := &Credentials{ApiKey: key}
		if err := Save(creds); err != nil {
			return "", fmt.Errorf("failed to save credentials: %w", err)
		}
		return key, nil
	}

	// Check existing credentials
	creds, err := Load()
	if err == nil && !creds.IsExpired() && creds.ApiKey != "" {
		return creds.ApiKey, nil
	}

	// Browser OAuth PKCE flow
	if backendURL == "" {
		return "", fmt.Errorf("BLOCKS_BACKEND_URL must be set (run from a project directory with .env, or set BLOCKS_BACKEND_URL)")
	}
	if clientID == "" {
		return "", fmt.Errorf("BLOCKS_CLI_CLIENT_ID must be set")
	}

	authURL := backendURL + "/api/auth/oauth2/authorize"
	tokenURL := backendURL + "/api/auth/oauth2/token"

	fmt.Println("  Opening browser for login...")
	fmt.Println("  (If the browser doesn't open, check the URL printed below)")

	result, err := RunBrowserFlow(ctx, authURL, clientID, backendURL)
	if err != nil {
		return "", fmt.Errorf("browser login failed: %w", err)
	}

	exchangeResp, err := ExchangeCode(tokenURL, result.Code, result.CodeVerifier, result.RedirectURI, clientID, result.Audience)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}

	newCreds, err := FetchOrCreateApiKey(backendURL, exchangeResp.AccessToken)
	if err != nil {
		return "", fmt.Errorf("API key creation failed: %w", err)
	}

	if err := Save(newCreds); err != nil {
		return "", fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Printf("  Logged in to %s (org: %s)\n", newCreds.OrgName, newCreds.OrgId)
	return newCreds.ApiKey, nil
}

// orgMembership represents a user's membership in an organization.
type orgMembership struct {
	OrgId   string `json:"orgId"`
	OrgName string `json:"orgName"`
}

// apiKeyCreateResponse represents the response from the API key creation endpoint.
type apiKeyCreateResponse struct {
	ApiKey    string `json:"apiKey"`
	KeyId     string `json:"keyId"`
	ExpiresAt string `json:"expiresAt"`
}

// FetchOrCreateApiKey fetches the user's org memberships, selects an org,
// and creates an API key for the CLI.
func FetchOrCreateApiKey(backendURL, sessionToken string) (*Credentials, error) {
	orgs, err := fetchOrgs(backendURL, sessionToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch organizations: %w", err)
	}

	if len(orgs) == 0 {
		return nil, fmt.Errorf("your account has no organization memberships — create or join an org first")
	}

	var selected orgMembership
	if len(orgs) == 1 {
		selected = orgs[0]
		fmt.Printf("  Using organization: %s\n", selected.OrgName)
	} else {
		selected, err = promptOrgSelection(orgs)
		if err != nil {
			return nil, err
		}
	}

	hostname, _ := os.Hostname()
	keyName := BuildApiKeyName(hostname)

	keyResp, err := createApiKey(backendURL, sessionToken, selected.OrgId, keyName)
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	var expiresAt time.Time
	if keyResp.ExpiresAt != "" {
		expiresAt, _ = time.Parse(time.RFC3339, keyResp.ExpiresAt)
	}

	return &Credentials{
		ApiKey:    keyResp.ApiKey,
		OrgId:     selected.OrgId,
		OrgName:   selected.OrgName,
		KeyId:     keyResp.KeyId,
		ExpiresAt: expiresAt,
	}, nil
}

// fetchOrgs retrieves the user's organization memberships from the backend.
func fetchOrgs(backendURL, sessionToken string) ([]orgMembership, error) {
	req, err := http.NewRequest("GET", backendURL+"/api/v1/orgs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /api/v1/orgs failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	// Backend returns { orgs: [{ id, name, slug, ... }], billingEnabled }
	var envelope struct {
		Orgs []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"orgs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode org response: %w", err)
	}

	result := make([]orgMembership, len(envelope.Orgs))
	for i, o := range envelope.Orgs {
		result[i] = orgMembership{OrgId: o.Id, OrgName: o.Name}
	}
	return result, nil
}

// promptOrgSelection presents the user with a numbered list of orgs and reads
// their choice from stdin.
func promptOrgSelection(orgs []orgMembership) (orgMembership, error) {
	fmt.Println("\n  You belong to multiple organizations. Select one:")
	for i, org := range orgs {
		fmt.Printf("    [%d] %s (%s)\n", i+1, org.OrgName, org.OrgId)
	}
	fmt.Print("  Enter number: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return orgMembership{}, fmt.Errorf("no input received")
	}
	input := strings.TrimSpace(scanner.Text())

	var choice int
	if _, err := fmt.Sscanf(input, "%d", &choice); err != nil || choice < 1 || choice > len(orgs) {
		return orgMembership{}, fmt.Errorf("invalid selection: %q — expected a number between 1 and %d", input, len(orgs))
	}

	selected := orgs[choice-1]
	fmt.Printf("  Selected: %s\n", selected.OrgName)
	return selected, nil
}

// createApiKey calls the backend to create an API key for the given org.
func createApiKey(backendURL, sessionToken, orgId, keyName string) (*apiKeyCreateResponse, error) {
	payload := map[string]string{
		"name":  keyName,
		"orgId": orgId,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", backendURL+"/api/auth/api-key/create", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /api/auth/api-key/create failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result apiKeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode API key response: %w", err)
	}
	if result.ApiKey == "" {
		return nil, fmt.Errorf("server returned empty API key")
	}
	return &result, nil
}

// InjectEnv writes the given key=value to the .env file in the current
// working directory, if the file exists. If the file does not exist,
// it prints a hint.
func InjectEnv(key, value string) {
	const envFile = ".env"
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		// Create .env with the key
		if err := os.WriteFile(envFile, []byte(key+"="+value+"\n"), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not create .env: %v\n", err)
			return
		}
		fmt.Printf("  .env created with %s\n", key)
		return
	}

	if err := UpsertEnvKey(envFile, key, value); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not update .env: %v\n", err)
		return
	}
	fmt.Printf("  .env updated with %s\n", key)
}

// EnvLineMatchesKey returns true if the line is an uncommented assignment for the
// given key (e.g. key="FOO" matches "FOO=bar" but not "FOO_EXTRA=bar" or "# FOO=bar").
func EnvLineMatchesKey(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return false
	}
	prefix := key + "="
	return strings.HasPrefix(trimmed, prefix)
}

// UpsertEnvKey reads the .env file line-by-line, replaces a matching KEY= line
// in-place, or appends the key if not found. Preserves all comments, spacing,
// and original key ordering.
func UpsertEnvKey(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, statErr := os.Stat(path)
	mode := os.FileMode(0644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	lines := strings.Split(string(data), "\n")
	prefix := key + "="
	found := false
	for i, line := range lines {
		if EnvLineMatchesKey(line, key) {
			lines[i] = prefix + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, prefix+value)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), mode)
}

// RemoveEnvKey removes all lines matching the given key from the .env file.
func RemoveEnvKey(path, key string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	info, statErr := os.Stat(path)
	mode := os.FileMode(0644)
	if statErr == nil {
		mode = info.Mode().Perm()
	}
	lines := strings.Split(string(data), "\n")
	var filtered []string
	for _, line := range lines {
		if !EnvLineMatchesKey(line, key) {
			filtered = append(filtered, line)
		}
	}
	if len(filtered) != len(lines) {
		os.WriteFile(path, []byte(strings.Join(filtered, "\n")), mode)
	}
}
