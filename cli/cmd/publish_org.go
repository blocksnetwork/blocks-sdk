package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

type orgChoice struct {
	Id   string
	Name string
}

// fetchUserOrgs lists the orgs the caller belongs to (GET /api/v1/orgs).
func fetchUserOrgs(backendURL, apiKey string) ([]orgChoice, error) {
	req, err := http.NewRequest("GET", strings.TrimRight(backendURL, "/")+"/api/v1/orgs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /api/v1/orgs failed (HTTP %d): %s", resp.StatusCode, string(body))
	}
	var envelope struct {
		Orgs []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"orgs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	out := make([]orgChoice, len(envelope.Orgs))
	for i, o := range envelope.Orgs {
		out[i] = orgChoice{Id: o.Id, Name: o.Name}
	}
	return out, nil
}

// promptOrgChoice asks which org to publish under.
func promptOrgChoice(orgs []orgChoice) (orgChoice, error) {
	fmt.Println("\nWhich organization should own this agent?")
	for i, o := range orgs {
		fmt.Printf("  [%d] %s\n", i+1, o.Name)
	}
	fmt.Print("Select organization: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return orgChoice{}, fmt.Errorf("no input received")
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(scanner.Text()), "%d", &n); err != nil || n < 1 || n > len(orgs) {
		return orgChoice{}, fmt.Errorf("invalid selection — expected 1..%d", len(orgs))
	}
	return orgs[n-1], nil
}

// resolveOrgPublishKey returns the API key to publish under for orgId: the cached
// per-org key if present/unexpired, otherwise one minted via CreateOrgAPIKey
// (bearer = the active key) and cached in the profile. The chosen org also becomes
// the profile's DefaultOrgID so later run/whoami resolve the just-published org.
func resolveOrgPublishKey(backendURL, bearerKey, profileName, orgId, orgName string) (string, error) {
	c, err := profiles.Load()
	if err != nil {
		return "", err
	}
	p := c.Profiles[profileName]

	apiKey := ""
	if k, ok := p.Orgs[orgId]; ok && k.ApiKey != "" && !k.IsExpired() {
		apiKey = k.ApiKey
	} else {
		hostname, _ := os.Hostname()
		keyName := auth.BuildApiKeyName(hostname)
		minted, mintErr := auth.CreateOrgAPIKey(backendURL, bearerKey, orgId, keyName)
		if mintErr != nil {
			return "", fmt.Errorf("failed to create an API key for org %s: %w", orgName, mintErr)
		}
		if p.Orgs == nil {
			p.Orgs = map[string]profiles.OrgKey{}
		}
		expiresAt := parseRFC3339OrZero(minted.ExpiresAt)
		p.Orgs[orgId] = profiles.OrgKey{OrgName: orgName, ApiKey: minted.ApiKey, KeyId: minted.KeyId, ExpiresAt: expiresAt}
		apiKey = minted.ApiKey
	}

	// The user explicitly picked this org to publish under, so make it the
	// profile's default. Otherwise later run/whoami resolve the active key via
	// DefaultOrgKey(), which prefers a stale DefaultOrgID once a second org is
	// cached — authenticating as the wrong org and tripping the fail-closed
	// cross-org registration check. Persist on the cached-key path too.
	p.DefaultOrgID = orgId
	c.Profiles[profileName] = p
	if err := profiles.Save(c); err != nil {
		return "", err
	}
	return apiKey, nil
}

// parseRFC3339OrZero parses an RFC3339 timestamp, returning the zero time for an
// empty string or an unparseable value (treated as "no expiry").
func parseRFC3339OrZero(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
