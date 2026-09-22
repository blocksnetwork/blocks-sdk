package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
)

// Build-time defaults injected via ldflags.
var defaultBackendURL = ""
var defaultCliClientID = ""

// defaultInstanceDomain is the DNS suffix for Blocks Enterprise deployments
// provisioned under the default scheme, `<customer>.blocks.ai`. Customers may
// instead use an agreed custom domain. It lets `blocks login umbrella`
// stand in for the full URL without any central tenant directory. Customers on
// an agreed custom domain pass a full URL or host instead, which
// resolveInstanceArg handles ahead of this expansion. Overridable at build time
// via -X github.com/pubnub/blocks-sdk/cli/cmd.defaultInstanceDomain=... (the
// release pipelines drive that from BLOCKS_INSTANCE_DOMAIN).
//
// Read it through instanceDomain(), never directly: the raw value arrives from
// outside the source tree and is concatenated into a URL the login sends a
// credential to.
var defaultInstanceDomain = "blocks.ai"

// instanceDomainEnv names the build-time variable defaultInstanceDomain is
// injected from, so the error a bad injection produces can point at the thing an
// operator has to fix.
const instanceDomainEnv = "BLOCKS_INSTANCE_DOMAIN"

// instanceDomain returns the DNS suffix a short name expands under, or an error
// when the value this binary was built with is not one.
//
// The value is validated here rather than at startup because a build that cannot
// expand short names is still a working CLI for every other form: a full-URL
// login, an already-stored profile, `whoami`, `profile list`. Refusing to start
// would turn a typo in one release variable into a bricked binary. Refusing the
// expansion, loudly, at the moment it is attempted keeps the failure proportional
// and points at the defect — a malformed suffix is a defect in the build, not in
// what the user typed, so the message names the variable and the value it carries
// instead of blaming the argument.
func instanceDomain() (string, error) {
	if !isDNSSuffix(defaultInstanceDomain) {
		return "", fmt.Errorf(
			"this build cannot expand short names: it was built with %s=%q, which is not a DNS suffix — pass the deployment's full URL (https://blocks.acme.com) instead, and report the bad build",
			instanceDomainEnv, defaultInstanceDomain)
	}
	return defaultInstanceDomain, nil
}

const (
	blocksAppBaseURLEnv   = "BLOCKS_APP_BASE_URL"
	blocksDashboardURLEnv = "BLOCKS_DASHBOARD_URL"
	blocksBackendURLEnv   = "BLOCKS_BACKEND_URL"
	blocksAPIKeyEnv       = "BLOCKS_API_KEY"
)

// resolveBackendURL is the backend origin a command sends its request to:
// BLOCKS_BACKEND_URL → active profile BaseURL → ldflag default → the api.baseUrl the
// CDM payload carries.
//
// Every tier of that precedence, the remote one included, lives in clictx and is
// resolved once for the whole invocation. This function adds none of its own — it used
// to add the CDM tier here, and that was the defect: the deployment the request
// reached could then differ from the one the resolver had already matched the profile
// against, chosen the credential for, decided the enterprise verdict for and named in
// the context banner. A stock Blocks Network profile plus a CDM endpoint naming an
// enterprise deployment sent that profile's Network key to the enterprise deployment
// while every line the operator read said Network.
//
// The fetch is deferred and memoized inside clictx, so resolving it here costs a
// round-trip only for the commands that reach a backend, and only the first time.
//
// A failure is reported rather than returned because every caller treats an
// unresolvable origin the same way — as an empty origin, which the request layer then
// rejects — while a user needs to know the reason was a remote-config fetch and not
// their own configuration.
func resolveBackendURL() string {
	url, err := clictx.EffectiveBackendURL()
	if err != nil {
		fmt.Fprintln(os.Stderr, remoteConfigWarning(err))
		return ""
	}
	return url
}

// resolveBackendURLOffline resolves the backend origin from the tiers that need
// no network round-trip: BLOCKS_BACKEND_URL → active profile BaseURL → ldflag
// default. Returns "" when none is set, leaving the (network-dependent) CDM
// fetch to resolveBackendURL. Split out so callers that must not stall on a CDM
// fetch (e.g. non-interactive publish) can still use the offline tiers.
//
// The precedence itself lives in clictx, resolved once for the whole invocation,
// so the origin a command posts to, the origin the context banner names, and the
// origin login pins into .env cannot disagree.
func resolveBackendURLOffline() string {
	return clictx.BackendURL()
}

// redirectedCDMDeploymentErr yields ("", nil) — not an error — when any other
// tier named the target or none did. Each guard drops a tier that is not a
// deployment the caller chose, so the build-time default never becomes one.
// redirectedCDMDeployment (login.go) reads it with the error dropped.
func redirectedCDMDeploymentErr() (string, error) {
	if clictx.BackendURL() != "" {
		return "", nil
	}
	if clictx.Profile() == "" || clictx.ProfileIsTarget() {
		return "", nil
	}
	return clictx.EffectiveBackendURL()
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
	case strings.TrimSpace(os.Getenv(blocksBackendURLEnv)) != "":
		raw, src = strings.TrimSpace(os.Getenv(blocksBackendURLEnv)), backendFromEnv
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

// resolveAppBaseURL resolves an explicit dashboard origin (empty if none).
// Precedence: BLOCKS_APP_BASE_URL → BLOCKS_DASHBOARD_URL → active profile
// DashboardBaseURL. The two env vars are the caller's direct intent and always
// win. The profile's stored DashboardBaseURL is trusted only while the profile is
// still the deployment being targeted: once an ambient backend URL points
// somewhere else, the caller is publishing to a different backend than the one
// the profile was logged into, so the saved dashboard origin is stale and is
// skipped — the caller then falls back to resolveBackendURL(). Without this guard
// a publish to deployment B via BLOCKS_BACKEND_URL would still open deployment
// A's dashboard. Whether the profile is still the target is answered by clictx,
// so this cannot drift from the banner's view of the same question.
func resolveAppBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(blocksAppBaseURLEnv)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv(blocksDashboardURLEnv)); v != "" {
		return v
	}
	if _, p, err := profiles.Active(); err == nil && p.DashboardBaseURL != "" && clictx.ProfileIsTarget() {
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
		fmt.Fprintln(os.Stderr, remoteConfigWarning(err))
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

// readAPIKeyFromStdin consumes the single line --api-key-stdin supplies. It is a
// var so tests can observe how often it runs: stdin is consumable, so the
// invariant that it runs at most once per invocation is a correctness property,
// not an optimization. Commands never call it — it is wired into clictx, which
// performs the read once and shares the result with every consumer.
var readAPIKeyFromStdin = func() (string, error) {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("--api-key-stdin: no input received on stdin")
	}
	key := scanner.Text()
	if key == "" {
		return "", fmt.Errorf("--api-key-stdin: empty API key received")
	}
	return key, nil
}

// loadStoredCredential reads the legacy credentials.json — the last tier of the
// credential precedence, kept for one migration cycle. It reports the key,
// whether a stored credential exists but has expired, and whether the file could
// not be read at all, and decides nothing: the ordering lives in clictx.
//
// A file that is present and readable but holds no Blocks key is reported as no
// credential rather than as a store failure: that is the standard shape after
// the profile migration drains the "blocks" namespace, and its owner is simply
// not authenticated — the opposite of an unreadable store, which may belong to
// a logged-in user and must not be answered with "log in again". Both the
// missing file and the drained file therefore fall through to the resolver's
// no-credential arm.
func loadStoredCredential() (string, bool, error) {
	creds, err := auth.Load()
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, auth.ErrNoBlocksCredential) {
			return "", false, nil
		}
		return "", false, err
	}
	if creds.IsExpired() {
		return "", true, nil
	}
	return creds.ApiKey, false, nil
}

// loadCredentials returns the API key this invocation will send, or an error.
// API keys are long-lived and do not require refresh.
//
// The key comes from clictx, which resolved it once for the whole invocation, so
// a command cannot send one credential while the context banner describes
// another. Only the error wording belongs here.
//
// StoreErr is reported rather than folded into "not logged in", and the arm order
// matches resolvePublishApiKey so the two mappers cannot disagree about the same
// Credential. A credential store that cannot be read is not an absent login: the
// caller may well be logged in, and telling them to log in again invites them to
// mint a second key against a store that will fail to record it. It sits after Key
// because a credential resolved from a higher tier makes the unreadable store
// irrelevant, and before Expired because an unreadable store is why expiry could
// not be determined at all.
func loadCredentials() (string, error) {
	c := clictx.EffectiveCredential()
	switch {
	case c.Err != nil:
		return "", c.Err
	case c.Key != "":
		return c.Key, nil
	case c.StoreErr != nil:
		return "", fmt.Errorf("failed to load credentials: %w", c.StoreErr)
	case c.Expired:
		return "", apiKeyExpiredError()
	default:
		return "", notLoggedInError()
	}
}

// externalCredential returns the API key supplied for this invocation from
// outside the credential store (--api-key or --api-key-stdin) and whether one was
// supplied at all. Commands ask this instead of re-reading their own flags so
// there is one answer to "did the caller hand us a key", and so a piped key is
// consumed by the single resolver rather than a second time here.
func externalCredential() (key string, supplied bool, err error) {
	c := clictx.EffectiveCredential()
	if !c.Source.External() {
		return "", false, nil
	}
	return c.Key, true, c.Err
}

// credentialSupplied reports whether a key was handed to this invocation from
// outside the credential store.
func credentialSupplied() bool {
	_, supplied, _ := externalCredential()
	return supplied
}

// interactiveSession reports whether this invocation may prompt at all. Being
// attached to a terminal is necessary but not sufficient: --no-input is an
// explicit request not to be asked, so a command that keys its prompts off TTY
// state alone will still prompt for a caller who asked it never to. Every prompt
// decision that is allowed to depend on interactivity must go through here.
func interactiveSession() bool {
	return isInteractive() && !noInputMode
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
