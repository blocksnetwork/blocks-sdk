// Package profiles manages the CLI's contexts.json store: named deployment
// targets (Blocks Network or Enterprise instances), each with its own base URL
// and a per-org API-key cache. It replaces the single legacy credentials.json
// while transparently migrating it on first run.
//
// Store boundary: contexts.json holds deployment profiles and their per-org Blocks
// API keys (a Blocks key is meaningless outside a deployment target); account-global
// partner tokens live in credentials.json (internal/auth).
package profiles

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// schemaVersion is the contexts.json format version. Note: this version line is
// independent of internal/auth's schema_version; the two files version separately.
const schemaVersion = 3

// DefaultProfile is the implicit stock Blocks Network target.
const DefaultProfile = "blocks-network"

// OrgKey is a cached, org-scoped API key for a deployment.
type OrgKey struct {
	OrgName   string    `json:"org_name"`
	ApiKey    string    `json:"api_key"`
	KeyId     string    `json:"key_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IsExpired reports whether the cached key has passed its expiry. A zero
// ExpiresAt (no expiry recorded) is treated as never expiring.
func (k OrgKey) IsExpired() bool {
	return !k.ExpiresAt.IsZero() && time.Now().After(k.ExpiresAt)
}

// Profile is a named deployment target with its own credentials.
type Profile struct {
	// BaseURL empty → resolve via CDM (stock Blocks Network).
	BaseURL          string            `json:"base_url,omitempty"`
	Enterprise       bool              `json:"enterprise"`
	ProductName      string            `json:"product_name,omitempty"`
	DashboardBaseURL string            `json:"dashboard_base_url,omitempty"`
	OAuthClientID    string            `json:"oauth_client_id,omitempty"`
	DefaultOrgID     string            `json:"default_org_id,omitempty"`
	Orgs             map[string]OrgKey `json:"orgs"`
}

// Contexts is the on-disk profile store.
type Contexts struct {
	SchemaVersion int                `json:"schema_version"`
	Active        string             `json:"active"`
	Profiles      map[string]Profile `json:"profiles"`
}

// ContextsPathFunc returns ~/.config/blocks/contexts.json (XDG). Overridable in tests.
var ContextsPathFunc = func() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "blocks", "contexts.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "blocks", "contexts.json"), nil
}

// Load reads contexts.json. If absent, it migrates a legacy v2 credentials.json
// into a blocks-network profile, or returns a store with a single empty default
// profile. Never returns nil on success.
func Load() (*Contexts, error) {
	path, err := ContextsPathFunc()
	if err != nil {
		return nil, err
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		var c Contexts
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", path, err)
		}
		if c.SchemaVersion > schemaVersion {
			return nil, fmt.Errorf("contexts file version %d is not supported — please upgrade blocks", c.SchemaVersion)
		}
		ensureDefault(&c)
		return &c, nil
	}
	if !os.IsNotExist(readErr) {
		return nil, readErr
	}
	// No contexts.json — try migrating legacy credentials.
	c := &Contexts{SchemaVersion: schemaVersion, Active: DefaultProfile, Profiles: map[string]Profile{}}
	if creds, err := auth.Load(); err == nil && creds.ApiKey != "" {
		p := Profile{Orgs: map[string]OrgKey{}}
		if creds.OrgId != "" {
			p.DefaultOrgID = creds.OrgId
			p.Orgs[creds.OrgId] = OrgKey{OrgName: creds.OrgName, ApiKey: creds.ApiKey, KeyId: creds.KeyId, ExpiresAt: creds.ExpiresAt}
		}
		c.Profiles[DefaultProfile] = p
		if err := Save(c); err != nil {
			return nil, err
		}
		// Active-drain: the Blocks key now lives in the profile (its single home),
		// so remove the legacy credentials.json "blocks" slot. Best-effort — a drain
		// failure must not block login resolution. Partner namespaces are untouched.
		if credPath, perr := auth.CredentialPathFunc(); perr == nil {
			_ = auth.DeleteProviderCredential(credPath, "blocks")
		}
		return c, nil
	}
	ensureDefault(c)
	return c, nil
}

// ensureDefault guarantees the store has a non-nil profile map containing the
// stock blocks-network profile and a resolvable active name.
func ensureDefault(c *Contexts) {
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if _, ok := c.Profiles[DefaultProfile]; !ok {
		c.Profiles[DefaultProfile] = Profile{Orgs: map[string]OrgKey{}}
	}
	// A persisted active name that no longer resolves is repaired to the default rather
	// than left standing. `blocks profile remove` cannot leave one behind — it refuses to
	// remove the default and rewrites Active — but a hand-edited store, a partial write, or
	// a store from a build that named profiles differently can. Left alone, Active() returns
	// an error, every consumer of the resolved context discards it as "no profile", and the
	// invocation quietly falls through to the ambient URL, the legacy store or a CDM default
	// — acting on a deployment nobody selected. Self-healing keeps that from being silent.
	if _, ok := c.Profiles[c.Active]; c.Active == "" || !ok {
		c.Active = DefaultProfile
	}
}

// Save writes the store with 0600 perms.
func Save(c *Contexts) error {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = schemaVersion
	}
	path, err := ContextsPathFunc()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// activeOverride is set by the root command's --profile flag (highest priority).
var activeOverride string

// SetActiveOverride records a per-invocation profile override (from --profile).
// Empty string clears it. Resolution: override → BLOCKS_PROFILE → file → default.
func SetActiveOverride(name string) { activeOverride = name }

// SelectedName returns an explicitly chosen profile name — the --profile override
// or BLOCKS_PROFILE env — or "" when neither is set. Unlike Active it does NOT
// fall back to the saved active profile or the default, so callers can tell
// "the user named a profile" apart from "use whatever is active".
func SelectedName() string {
	if activeOverride != "" {
		return activeOverride
	}
	return os.Getenv("BLOCKS_PROFILE")
}

// Active returns the active profile name and value. Resolution order:
// --profile override → BLOCKS_PROFILE env → contexts.json `active` → DefaultProfile.
func Active() (string, *Profile, error) {
	c, err := Load()
	if err != nil {
		return "", nil, err
	}
	name := c.Active
	if env := os.Getenv("BLOCKS_PROFILE"); env != "" {
		name = env
	}
	if activeOverride != "" {
		name = activeOverride
	}
	p, ok := c.Profiles[name]
	if !ok {
		return "", nil, fmt.Errorf("profile %q not found — run 'blocks profile list'", name)
	}
	return name, &p, nil
}

// SetActive persists the active profile (must exist). Returns the resolved profile.
func SetActive(name string) (string, *Profile, error) {
	c, err := Load()
	if err != nil {
		return "", nil, err
	}
	p, ok := c.Profiles[name]
	if !ok {
		return "", nil, fmt.Errorf("profile %q not found", name)
	}
	c.Active = name
	if err := Save(c); err != nil {
		return "", nil, err
	}
	return name, &p, nil
}

// Upsert writes a profile by name and (optionally) makes it active.
func Upsert(name string, p Profile, makeActive bool) error {
	c, err := Load()
	if err != nil {
		return err
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	c.Profiles[name] = p
	if makeActive {
		c.Active = name
	}
	return Save(c)
}

// Remove deletes a profile (cannot remove the default). If it was active, the
// default becomes active.
func Remove(name string) error {
	if name == DefaultProfile {
		return fmt.Errorf("cannot remove the %q profile", DefaultProfile)
	}
	c, err := Load()
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(c.Profiles, name)
	if c.Active == name {
		c.Active = DefaultProfile
	}
	return Save(c)
}

// Rename changes a profile's name, preserving its data. The default
// blocks-network profile cannot be renamed, the source must exist, and the new
// name must be free. If the renamed profile was active, the active selection
// follows it.
func Rename(oldName, newName string) error {
	if oldName == DefaultProfile {
		return fmt.Errorf("cannot rename the %q profile", DefaultProfile)
	}
	if newName == "" {
		return fmt.Errorf("new profile name cannot be empty")
	}
	c, err := Load()
	if err != nil {
		return err
	}
	p, ok := c.Profiles[oldName]
	if !ok {
		return fmt.Errorf("profile %q not found", oldName)
	}
	if _, exists := c.Profiles[newName]; exists {
		return fmt.Errorf("profile %q already exists", newName)
	}
	delete(c.Profiles, oldName)
	c.Profiles[newName] = p
	if c.Active == oldName {
		c.Active = newName
	}
	return Save(c)
}

// SameBaseURL reports whether two base URLs address the same deployment. It is
// the single definition of "same deployment" for callers that must decide
// whether a profile still describes the backend being called. Either side being
// unparseable, or empty, means "not the same".
func SameBaseURL(a, b string) bool {
	na := NormalizeBaseURL(a)
	return na != "" && na == NormalizeBaseURL(b)
}

// NormalizeBaseURL parses raw into a comparable "scheme://host[:port][/path]"
// base URL: scheme/host lowercased, the scheme's default port (443 for https, 80
// for http) dropped, and any trailing slash on the path trimmed. The path is
// retained because the CLI uses a base URL as a full request prefix
// (`BaseURL + "/api/v1/..."`), so path-prefixed multi-tenant deployments on one
// host are distinct. Returns "" when raw has no parseable host.
func NormalizeBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
}

// RecordsCompletedLogin reports whether this profile holds evidence that a login
// finished against the deployment it describes: an organization recorded as its default,
// or any cached organization key. Both are written together when a login succeeds.
//
// A profile merely existing is not that evidence. ensureDefault materializes an empty
// DefaultProfile on first run, so the stock Blocks Network profile is present before
// anyone has logged in — and because empty BaseURL is how "resolve via CDM" is recorded,
// neither its existence nor its origin distinguishes "not logged in yet" from "logged in
// to Blocks Network". The cached organization is what separates them.
func (p *Profile) RecordsCompletedLogin() bool {
	return p.DefaultOrgID != "" || len(p.Orgs) > 0
}

// DefaultOrgKey returns the active-org key for a profile (its DefaultOrgID, else
// the sole org if there is exactly one). ok=false when no usable key exists.
func (p *Profile) DefaultOrgKey() (OrgKey, bool) {
	if p.DefaultOrgID != "" {
		if k, ok := p.Orgs[p.DefaultOrgID]; ok {
			return k, true
		}
	}
	if len(p.Orgs) == 1 {
		for _, k := range p.Orgs {
			return k, true
		}
	}
	return OrgKey{}, false
}
