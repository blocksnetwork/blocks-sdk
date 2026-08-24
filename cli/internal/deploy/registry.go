package deploy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// targetNameRe is the shared shape gate for deploy-target names. Applied
// uniformly to (a) built-in adapter registrations, (b) on-disk plugin
// manifest loading (plugin.go), and (c) the deployTarget field in
// blocks.config.json (internal/config). Without one canonical pattern the
// three layers can disagree — a plugin manifest with name "AWS" used to
// load fine, deploy once, and persist "AWS" into the config file, where
// config validation then rejected it on the next deploy.
var targetNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidateTargetName checks that a deploy-target slug matches the shape
// allowed across the deploy surface. Empty input is rejected; callers that
// want to treat empty as "use default" must short-circuit before calling.
// The returned error is shape-only ("must match ^...$"); callers wrap with
// field-specific context (e.g. `deployTarget %q ...`, `plugin manifest %q
// ...`) so the error message tells the user which field they got wrong.
func ValidateTargetName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !targetNameRe.MatchString(name) {
		return fmt.Errorf("%q must match ^[a-z0-9][a-z0-9_-]*$ (lowercase letters/digits, separated by - or _)", name)
	}
	return nil
}

// CredentialFlowKind labels how an Adapter expects to acquire partner credentials.
type CredentialFlowKind string

const (
	CredentialFlowAPIToken     CredentialFlowKind = "api-token"
	CredentialFlowBrowserGrant CredentialFlowKind = "browser-grant"
	CredentialFlowNone         CredentialFlowKind = "none"
)

// AdapterSource records where an Adapter was registered from. Built-ins are
// compiled into the binary; disk adapters are loaded from
// ~/.config/blocks/deploy-targets/*.yml. Disk adapters take precedence over
// built-ins of the same name so users can override the built-in behavior.
type AdapterSource string

const (
	SourceBuiltin AdapterSource = "builtin"
	SourceDisk    AdapterSource = "disk"
)

// UploadFunc is the deploy entry point. Returns the public URL of the deployed site.
type UploadFunc func(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error)

// Adapter describes a single deploy target.
type Adapter struct {
	Name        string
	Description string
	Source      AdapterSource
	Credential  CredentialFlowKind
	// CredentialEnvVar is the environment variable name that holds the
	// partner credential. Built-in adapters delegate to their internal
	// partner flow; disk adapters use this to inject the credential into
	// the plugin subprocess.
	CredentialEnvVar string
	// CredentialPrompt is shown to the user when an interactive credential
	// flow is required. Empty for built-in adapters (they manage their own
	// prompts via internal/auth/partners).
	CredentialPrompt string
	Upload           UploadFunc
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Adapter{}
)

// Register adds or replaces an adapter by name. Disk-source adapters override
// built-in adapters of the same name.
func Register(a Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	existing, ok := registry[a.Name]
	if ok && existing.Source == SourceDisk && a.Source == SourceBuiltin {
		return
	}
	registry[a.Name] = a
}

// Resolve looks up an adapter by name.
func Resolve(name string) (Adapter, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	a, ok := registry[name]
	return a, ok
}

// List returns all registered adapters sorted by name. Useful for
// `blocks deploy --list`.
func List() []Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Adapter, 0, len(registry))
	for _, a := range registry {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reset clears the registry and re-registers built-ins. Test-only.
func Reset() {
	registryMu.Lock()
	registry = map[string]Adapter{}
	registryMu.Unlock()
	registerBuiltins()
}

func init() {
	registerBuiltins()
}

func registerBuiltins() {
	for _, a := range builtinAdapters() {
		Register(a)
	}
}

func builtinAdapters() []Adapter {
	return []Adapter{
		{
			Name:             "cloudflare",
			Description:      "Cloudflare Pages (direct upload)",
			Source:           SourceBuiltin,
			Credential:       CredentialFlowAPIToken,
			CredentialEnvVar: "CLOUDFLARE_API_TOKEN",
			Upload:           CloudflareUpload,
		},
		{
			Name:             "vercel",
			Description:      "Vercel (REST file upload + deployment)",
			Source:           SourceBuiltin,
			Credential:       CredentialFlowAPIToken,
			CredentialEnvVar: "VERCEL_TOKEN",
			Upload:           VercelUpload,
		},
		{
			Name:             "netlify",
			Description:      "Netlify (zip deploy)",
			Source:           SourceBuiltin,
			Credential:       CredentialFlowAPIToken,
			CredentialEnvVar: "NETLIFY_AUTH_TOKEN",
			Upload:           NetlifyUpload,
		},
	}
}
