package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
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
		return nil, fmt.Errorf("GET /api/v1/orgs failed (HTTP %d): %s", resp.StatusCode, termsafe.Text(string(body)))
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

// printOrgPickerTarget states the deployment this invocation is about to act on,
// before the picker asks anything and before the picker can mint or store
// anything. The full context banner cannot print here: the organization half is
// the very thing still being decided, and naming the one the local precedence
// resolved is the defect the banner's placement exists to prevent. The deployment
// half is already settled — the active profile or the ambient backend URL fixed it
// before the command body ran — and it is the half that answers the question a
// user needs answered before anything changes: am I pointed at the right place at
// all.
//
// It names the deployment exactly as the banner's first slot does, by the active
// profile, so the line printed before the choice and the banner printed after it
// cannot describe one target two ways. That label is always the right one here:
// the picker runs only when the credential came out of the active profile, which
// in turn requires that profile to describe the backend being called — an
// invocation whose profile has been displaced fails while resolving its
// credential, long before any prompt.
func printOrgPickerTarget(profileName string) {
	fmt.Printf("\nDeployment: %s\n", termsafe.Text(profileName))
}

// promptOrgChoice asks which org to publish under.
//
// The names come from the deployment, so they are rendered through termsafe: a
// name carrying an erase-line or cursor-up sequence could otherwise rewrite the
// deployment line printed just above it, or make two entries in the list render
// alike, and the list is what the user's answer refers to.
func promptOrgChoice(orgs []orgChoice) (orgChoice, error) {
	fmt.Println("\nWhich organization should own this agent?")
	for i, o := range orgs {
		fmt.Printf("  [%d] %s\n", i+1, termsafe.Text(o.Name))
	}
	fmt.Print("Select organization: ")
	line, ok, refused := readStdinLine()
	if refused {
		return orgChoice{}, fmt.Errorf("cannot ask organization selection with --no-input")
	}
	if !ok {
		return orgChoice{}, fmt.Errorf("no input received")
	}
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > len(orgs) {
		return orgChoice{}, fmt.Errorf("invalid selection — expected 1..%d", len(orgs))
	}
	return orgs[n-1], nil
}

// resolveOrgPublishKey returns the API key to publish under for orgId — the cached
// per-org key when one is present and unexpired, otherwise one minted via
// CreateOrgAPIKey (bearer = the active key) and cached in the profile — and
// reports whether it had to mint.
//
// Minting cannot be deferred until the publish succeeds: the key is what
// authorizes the registry request as the chosen organization, so it has to exist
// before the request is made. Caching it is what keeps that unavoidable write
// cheap. The key exists on the server the moment it is minted, so dropping it
// locally would leave behind a credential the user can neither see nor reuse, and
// the next attempt would mint a second one. Cached, a retry reuses it.
//
// It deliberately does NOT re-point the profile's default organization. That is
// the one part of the selection that must wait until the registry has accepted
// the agent — see setProfileDefaultOrg — so a publish the user cancels at a later
// prompt, or one the registry rejects, leaves every later command resolving the
// organization it resolved before.
//
// The minted flag reports the remote write, not the local one: it is true even when
// the write to the profile store then fails. The credential exists on the server
// from the moment the mint returns, so a caller told nothing about it — because the
// only signal was an error from a local file write — would leave an unreferenced
// key on the deployment that nobody knows to revoke. Callers must therefore
// announce a mint before they act on the error.
func resolveOrgPublishKey(backendURL, bearerKey, profileName, orgId, orgName string) (string, bool, error) {
	c, err := profiles.Load()
	if err != nil {
		return "", false, err
	}
	p := c.Profiles[profileName]

	if k, ok := p.Orgs[orgId]; ok && k.ApiKey != "" && !k.IsExpired() {
		return k.ApiKey, false, nil
	}

	hostname, _ := os.Hostname()
	minted, mintErr := auth.CreateOrgAPIKey(backendURL, bearerKey, orgId, auth.BuildApiKeyName(hostname))
	if mintErr != nil {
		return "", false, fmt.Errorf("failed to create an API key for org %s: %w", termsafe.Text(orgName), mintErr)
	}
	if p.Orgs == nil {
		p.Orgs = map[string]profiles.OrgKey{}
	}
	p.Orgs[orgId] = profiles.OrgKey{
		OrgName:   orgName,
		ApiKey:    minted.ApiKey,
		KeyId:     minted.KeyId,
		ExpiresAt: auth.ParseKeyExpiry(minted.ExpiresAt),
	}
	c.Profiles[profileName] = p
	if err := profiles.Save(c); err != nil {
		return "", true, fmt.Errorf("created an API key for %s but could not store it in the %s profile: %w",
			termsafe.Text(orgName), termsafe.Text(profileName), err)
	}
	return minted.ApiKey, true, nil
}

// reportMintedOrgKey tells the user that a credential now exists for the
// organization they picked, whether it was stored locally, and what to do about it.
//
// The mint is the one side effect of the picker that cannot wait for the publish
// to succeed, so a publish that is then cancelled or rejected leaves the key
// behind. Naming it is what keeps it from being invisible: an orphan credential
// the user knows about can be revoked, one they never heard of cannot.
//
// stored=false is the worse of the two cases and must still be reported. The key
// was created on the deployment and then not recorded anywhere, so nothing local
// can reuse it or even name it; only this line can, which is why it names both the
// organization and the deployment.
func reportMintedOrgKey(orgName, profileName string, stored bool) {
	org, deployment := termsafe.Text(orgName), termsafe.Text(profileName)
	fmt.Printf("Created an API key for %s on %s.\n", org, deployment)
	if stored {
		fmt.Printf("It is stored in the %s profile and stays there even if this command does not finish; revoke it from the dashboard if that is not what you wanted.\n", deployment)
		return
	}
	fmt.Printf("It could NOT be stored in the %s profile, so nothing here can reuse it and a retry will create another one — revoke it from the dashboard.\n", deployment)
}

// setProfileDefaultOrg records orgId as profileName's default organization, so a
// later `blocks run` or `blocks whoami` authenticates as the organization the
// agent was just published under.
//
// It is separate from resolveOrgPublishKey, and called only once the registry has
// accepted the agent, because DefaultOrgKey() prefers DefaultOrgID as soon as a
// second organization is cached: written up front, an abandoned publish would
// silently re-point every later command at an organization nothing was published
// to, and one that would then trip the fail-closed cross-org registration check.
func setProfileDefaultOrg(profileName, orgId string) error {
	c, err := profiles.Load()
	if err != nil {
		return err
	}
	p, ok := c.Profiles[profileName]
	if !ok {
		return fmt.Errorf("profile %q is no longer present", profileName)
	}
	p.DefaultOrgID = orgId
	c.Profiles[profileName] = p
	return profiles.Save(c)
}
