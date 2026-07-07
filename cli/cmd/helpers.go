package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
)

// Build-time defaults injected via ldflags.
var defaultBackendURL = ""
var defaultCliClientID = ""

const (
	blocksAppBaseURLEnv   = "BLOCKS_APP_BASE_URL"
	blocksDashboardURLEnv = "BLOCKS_DASHBOARD_URL"
)

func resolveBackendURL() string {
	if v := os.Getenv("BLOCKS_BACKEND_URL"); v != "" {
		return v
	}
	if _, p, err := profiles.Active(); err == nil && p.BaseURL != "" {
		return p.BaseURL
	}
	if defaultBackendURL != "" {
		return defaultBackendURL
	}
	cfg, err := cdm.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch remote config: %v\n", err)
		return ""
	}
	if cfg.Api.BaseURL != "" {
		return cfg.Api.BaseURL
	}
	return ""
}

// resolveWebappBackendURL picks the backend API origin baked into a webapp
// scaffold at `blocks init --mode webapp` time.
//
// Precedence:
//  1. explicit --backend-url flag (flagVal)
//  2. BLOCKS_BACKEND_URL env
//  3. active profile .BaseURL (profile-aware — the fix's core)
//  4. defaultBackendURL (ldflag-injected default for packaged/enterprise
//     builds; empty for stock source builds)
//  5. assetBaseURL (stock default; equals --blocks-base-url, or
//     https://app.blocks.ai when that flag is unset)
//
// Unlike resolveBackendURL it deliberately does NOT consult CDM: the baked
// value must be deterministic and byte-stable for stock users, and a
// static bundle should not depend on a remote config fetch to know its own
// backend.
//
// The resolved value is trailing-slash-normalized via trimURL before it is
// returned, so the origin baked into app.js byte-matches what the embed-auth
// widget stores as its partition key (the widget trims on sign-in; the
// scaffold's auto-resume path hashes the baked value verbatim). Without this,
// a trailing-slash backendBaseUrl would key sign-in and auto-resume under
// different partitions and silently break session resume.
func resolveWebappBackendURL(flagVal, assetBaseURL string) (string, error) {
	url, _, err := resolveWebappBackend(flagVal, assetBaseURL)
	return url, err
}

// backendSource identifies which precedence tier resolveWebappBackend selected.
type backendSource int

const (
	backendFromAsset   backendSource = iota // asset-base fallback (stock default)
	backendFromFlag                         // --backend-url
	backendFromEnv                          // BLOCKS_BACKEND_URL
	backendFromProfile                      // active profile .BaseURL
	backendFromLdflag                       // ldflag-injected defaultBackendURL
)

// resolveWebappBackend is the single source of truth for the webapp backend
// precedence. It returns the trailing-slash-normalized backend origin AND the
// tier it came from, so callers that only need the URL (resolveWebappBackendURL)
// and callers that only need the source (backendResolvedFromExplicitSource)
// cannot encode the precedence order differently and drift apart.
//
// Precedence (highest first): --backend-url flag, BLOCKS_BACKEND_URL env, active
// profile .BaseURL, ldflag defaultBackendURL, then assetBaseURL. See the
// resolveWebappBackendURL doc block above for why CDM is deliberately excluded
// and why trimURL normalization matters for partition-key parity.
func resolveWebappBackend(flagVal, assetBaseURL string) (string, backendSource, error) {
	raw := assetBaseURL
	src := backendFromAsset
	switch {
	case strings.TrimSpace(flagVal) != "":
		raw, src = strings.TrimSpace(flagVal), backendFromFlag
	case strings.TrimSpace(os.Getenv("BLOCKS_BACKEND_URL")) != "":
		raw, src = strings.TrimSpace(os.Getenv("BLOCKS_BACKEND_URL")), backendFromEnv
	default:
		// profiles.Active never returns a benign "no active profile" error: the
		// stock profile is always ensured, and an existing profile with an empty
		// BaseURL is handled by the p.BaseURL != "" guard below. So any error
		// here is a real failure (unreadable/corrupt contexts.json, or a
		// --profile/BLOCKS_PROFILE naming a profile that doesn't exist). Surface
		// it instead of silently falling through and baking the wrong origin.
		_, p, err := profiles.Active()
		if err != nil {
			return "", backendFromAsset, fmt.Errorf("resolve active profile: %w", err)
		}
		if p.BaseURL != "" {
			raw, src = p.BaseURL, backendFromProfile
		} else if defaultBackendURL != "" {
			raw, src = defaultBackendURL, backendFromLdflag
		}
	}
	return trimURL(raw), src, nil
}

// backendResolvedFromExplicitSource reports whether resolveWebappBackend picks a
// value from an explicit source (--backend-url flag, BLOCKS_BACKEND_URL env, the
// active profile, or the ldflag default) rather than falling back to the asset
// base. Derived from resolveWebappBackend so the precedence stays single-sourced.
func backendResolvedFromExplicitSource(flagVal string) bool {
	_, src, err := resolveWebappBackend(flagVal, "")
	if err != nil {
		return false
	}
	return src != backendFromAsset
}

// resolveWebappAssetBase picks the widget-bundle asset host baked into a webapp
// scaffold's index.html at `blocks init --mode webapp` time.
//
// Precedence:
//  1. explicit --blocks-base-url flag (assetFlag), trailing-slash normalized
//  2. the already-resolved backend origin (resolvedBackendURL)
//
// Mirroring the backend origin when the flag is unset is deliberate: the asset
// host and backend "MUST agree" for on-prem / split deployments (see
// scaffold.EmbedVars.BlocksAssetBaseUrl), and stock users already resolve the
// backend to https://app.blocks.ai — so stock scaffolds are byte-stable while
// enterprise profiles (which set only BaseURL) get the enterprise host for free.
// --blocks-base-url stays the escape hatch when asset and API legitimately differ.
func resolveWebappAssetBase(assetFlag, resolvedBackendURL string) string {
	if s := strings.TrimSpace(assetFlag); s != "" {
		return trimURL(s)
	}
	return resolvedBackendURL
}

// resolveWebappURLs is the single seam both webapp init paths (runWebapp,
// runWebappWizard) use to resolve the two origins frozen into a scaffold. It
// keeps the asset host and backend URL derived from one precedence source so
// they cannot drift: the backend's last-tier fallback is the asset flag (or the
// stock default), and the asset host mirrors the resolved backend when the flag
// is unset. Returns (assetBase, backendURL).
func resolveWebappURLs(backendFlag, assetFlag string) (string, string, error) {
	assetFallback := scaffold.DefaultAssetBaseURL
	if s := strings.TrimSpace(assetFlag); s != "" {
		assetFallback = trimURL(s)
	}
	backendURL, err := resolveWebappBackendURL(backendFlag, assetFallback)
	if err != nil {
		return "", "", err
	}
	return resolveWebappAssetBase(assetFlag, backendURL), backendURL, nil
}

// currentIntendedBackendURL reports the backend origin the CURRENT
// environment intends, for `blocks deploy`'s divergence check. It is
// resolveWebappBackendURL with no flag and no asset-base tier: passing an
// empty asset base means the fallback yields the ldflag defaultBackendURL
// (on packaged builds) or "" (stock source builds / intent unknown), which
// the caller treats as "skip the check". Keeping the env→profile→ldflag
// precedence in one place (resolveWebappBackendURL) avoids drift.
func currentIntendedBackendURL() (string, error) {
	return resolveWebappBackendURL("", "")
}

func resolveAppBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(blocksAppBaseURLEnv)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(blocksDashboardURLEnv)); v != "" {
		return v
	}
	if _, p, err := profiles.Active(); err == nil && p.DashboardBaseURL != "" {
		return p.DashboardBaseURL
	}
	return ""
}

func resolveClientID() string {
	if v := os.Getenv("BLOCKS_CLI_CLIENT_ID"); v != "" {
		return v
	}
	if defaultCliClientID != "" {
		return defaultCliClientID
	}
	cfg, err := cdm.Get()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to fetch remote config: %v\n", err)
		return ""
	}
	if cfg.Api.ClientID != "" {
		return cfg.Api.ClientID
	}
	return ""
}

// openBrowserFunc is the browser-open implementation. Tests replace it with a no-op.
var openBrowserFunc = auth.OpenBrowser

func openBrowser(rawURL string) error {
	return openBrowserFunc(rawURL)
}

// activeProfileAPIKey returns the active profile's default-org API key, if a
// usable (non-empty, unexpired) one exists.
func activeProfileAPIKey() (string, bool) {
	_, p, err := profiles.Active()
	if err != nil {
		return "", false
	}
	if k, ok := p.DefaultOrgKey(); ok && k.ApiKey != "" && !k.IsExpired() {
		return k.ApiKey, true
	}
	return "", false
}

// loadCredentials loads credentials and returns the API key or an error.
// API keys are long-lived and do not require refresh. The active profile is
// preferred; the legacy credentials.json remains a fallback for one migration
// cycle.
func loadCredentials() (string, error) {
	if key, ok := activeProfileAPIKey(); ok {
		return key, nil
	}

	creds, err := auth.Load()
	if err != nil {
		return "", fmt.Errorf("not logged in — run 'blocks login' first")
	}

	if creds.IsExpired() {
		return "", fmt.Errorf("API key has expired — run 'blocks login' to create a new one")
	}

	return creds.ApiKey, nil
}

// optionalCredentials returns the stored API key when a valid, unexpired
// credential exists, or an empty string otherwise (anonymous access). Unlike
// loadCredentials it never errors — callers use it when the operation can
// proceed against public-only resources without a login.
func optionalCredentials() string {
	creds, err := auth.Load()
	if err != nil || creds.IsExpired() {
		return ""
	}
	return creds.ApiKey
}

// confirmYesNo prints prompt to stdout and reads one line from in. It returns
// false only when the user explicitly answers "n"/"no" (case-insensitive);
// empty input, EOF, and any other answer default to yes (true). Shared by the
// init and deploy "(Y/n)" prompts so the default-yes semantics live in one place.
func confirmYesNo(in io.Reader, prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return true
	}
	ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
	return ans != "n" && ans != "no"
}
