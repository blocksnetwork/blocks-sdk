package cmd

import (
	"bufio"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/auth/partners"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/origin"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
	"github.com/spf13/cobra"
)

var loginApiKey string
var loginApiKeyStdin bool
var loginWriteEnv bool
var loginProvider string
var loginNoWriteEnv bool
var loginDir string
var loginNetwork bool

// Shared stdin scanner to avoid buffered input conflicts when multiple
// prompts need to read from stdin during login flow.
var stdinScanner *bufio.Scanner

// noInputMode tracks whether --no-input is set. When true, prompts must fail
// with actionable errors instead of reading stdin.
var noInputMode bool

// setNoInputMode is called from root.go PersistentPreRun to propagate the
// --no-input flag state to the prompt primitives.
func setNoInputMode(enabled bool) {
	noInputMode = enabled
}

// readStdinLine reads a line from stdin using the shared scanner, returning
// the trimmed line, whether a line was successfully read, and whether --no-input
// was set (requiring the caller to error instead of prompting). The helper
// checks scanner.Err() and handles read failures consistently.
func readStdinLine() (string, bool, bool) {
	if noInputMode {
		return "", false, true
	}
	if stdinScanner == nil {
		stdinScanner = bufio.NewScanner(os.Stdin)
	}
	if !stdinScanner.Scan() {
		if err := stdinScanner.Err(); err != nil {
			// In case of read error, log it but don't change behavior
			fmt.Fprintf(os.Stderr, "  Warning: stdin read error: %v\n", err)
		}
		return "", false, false
	}
	return strings.TrimSpace(stdinScanner.Text()), true, false
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVar(&loginApiKey, "api-key", "", "Use a pre-obtained API key")
	loginCmd.Flags().BoolVar(&loginApiKeyStdin, "api-key-stdin", false, "Read API key from stdin")
	loginCmd.Flags().BoolVar(&loginWriteEnv, "write-env", false, "Also write BLOCKS_API_KEY, and the deployment's BLOCKS_BACKEND_URL and BLOCKS_CDM_URL, to the project .env (non-interactive)")
	loginCmd.Flags().StringVar(&loginProvider, "provider", "blocks", "Provider to authenticate: blocks|cloudflare|vercel|netlify")
	loginCmd.Flags().BoolVar(&loginNoWriteEnv, "no-write-env", false, "Skip writing credentials to the project .env and skip the prompt")
	loginCmd.Flags().StringVar(&loginDir, "dir", "", "Directory to write .env into (default: current directory)")
	loginCmd.Flags().BoolVar(&loginNetwork, "network", false, "Target Blocks Network (for an Enterprise deployment, pass its URL or short name as an argument instead)")
	loginCmd.MarkFlagsMutuallyExclusive("write-env", "no-write-env")
}

var loginCmd = &cobra.Command{
	Use:   "login [instanceUrl|shortName]",
	Short: "Authenticate and store API credentials",
	Long: `Authenticates via browser login (or provided key) and stores the API key
in the active profile. An optional instance URL targets a specific deployment;
the CLI discovers whether it is an enterprise instance (and its branding /
OAuth client id) before authenticating. A short name is expanded to the default deployment scheme — blocks login
umbrella targets https://umbrella.blocks.ai. A name matching an existing
profile uses that profile's deployment instead, so custom domains keep working. With no argument, the active profile's
deployment is reused (defaults to Blocks Network). Pass --profile <name> to
store the deployment under a custom profile name instead of its host (an
existing profile can be renamed with 'blocks profile rename'). Always performs
a fresh login even if credentials already exist. In an interactive terminal, offers to
write the credential into the project .env; pass --write-env to opt in or
--no-write-env to opt out non-interactively (recommended for coding-agent /
scripted use). When stdin is not a TTY and no flag is given, the prompt is
skipped and .env is left untouched. Writing the .env stores BLOCKS_API_KEY plus
the deployment's BLOCKS_BACKEND_URL and BLOCKS_CDM_URL, so a script run directly
reaches the same deployment and the same keysets as 'blocks run'; a login to
Blocks Network needs neither and removes both.

Pass --network to target Blocks Network explicitly without prompting (useful
for scripted use). This flag conflicts with providing an instance argument.

Deployment targeting:
  blocks login --network                   Authenticate against Blocks Network
  blocks login acme                        Authenticate against an Enterprise instance (short name)
  blocks login https://blocks.acme.com     Authenticate against an Enterprise instance (custom domain)

Non-interactive usage (CI / automation):
  blocks login --api-key <key> --write-env --dir ./my_agent
  blocks login https://blocks.acme.com --no-write-env
  blocks login https://blocks.acme.com --profile acme
  blocks login --network --write-env`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()

		// Validate --network flag doesn't conflict with positional instance
		if loginNetwork && len(args) > 0 {
			return fmt.Errorf("--network flag conflicts with instance argument %q", args[0])
		}

		choice := deploymentChoice{}
		if len(args) > 0 {
			instanceURL, matchedProfile, err := expandInstanceArg(args[0])
			if err != nil {
				return err
			}
			if instanceURL == "" {
				return instanceArgError(args[0])
			}
			choice.instanceURL, choice.profileName = instanceURL, matchedProfile
		}
		return runLogin(ctx, choice)
	},
}

func runLogin(ctx context.Context, choice deploymentChoice) error {
	switch loginProvider {
	case "blocks", "":
		return runBlocksLogin(ctx, choice)
	case "cloudflare":
		return runPartnerLogin(ctx, &partners.CloudflareFlow{})
	case "vercel":
		return runPartnerLogin(ctx, &partners.VercelFlow{})
	case "netlify":
		return runPartnerLogin(ctx, &partners.NetlifyFlow{})
	default:
		return fmt.Errorf("unknown provider %q — must be one of: blocks, cloudflare, vercel, netlify", loginProvider)
	}
}

// runPartnerLogin runs the Ensure flow for a partner adapter and prints
// a confirmation message. Existing Blocks login is unaffected.
func runPartnerLogin(ctx context.Context, flow auth.CredentialFlow) error {
	creds, err := flow.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	providerName := strings.ToUpper(creds.Provider[:1]) + creds.Provider[1:]
	fmt.Printf("Logged in to %s\n", providerName)
	return nil
}

// runBlocksLogin runs the Blocks login path and reports the result. All
// credential persistence is delegated to loginToProfile; this function owns only
// the user-facing messaging and the optional .env write.
func runBlocksLogin(ctx context.Context, choice deploymentChoice) error {
	if choice.instanceURL == "" {
		var err error
		choice, err = promptDeploymentIfFirstLogin()
		if err != nil {
			return err
		}
	}
	out, err := loginToProfile(ctx, choice)
	if err != nil {
		return err
	}
	// The deployment's name comes from its own discovery payload and the profile's
	// from the store, so neither is text this CLI wrote.
	fmt.Printf("✓ Logged in to %s (profile: %s)\n", termsafe.Text(out.targetName), termsafe.Text(out.profileName))
	fmt.Printf("  API key: %s\n", registry.MaskAPIKey(out.apiKey))
	return maybeWriteEnv(out.apiKey, choice)
}

const helpDeploymentChoice = "  Blocks Network is the public deployment at app.blocks.ai.\n" +
	"  Choose Enterprise to target your company's instance — enter its full\n" +
	"  URL, or just its short name (e.g. `umbrella` for umbrella.blocks.ai).\n"

// unreachableKind classifies different types of connectivity failures for consistent
// handling across error messages, hints, and fail-fast decisions.
type unreachableKind int

const (
	kindHostnameMismatch unreachableKind = iota
	kindDNSNotFound
	kindRefused
	kindTimeout
	kindHTTPStatus
	kindDecode
	kindUnknown
)

// classifyUnreachable analyzes an error and returns its classification for
// consistent handling across describeUnreachable, formatHints, and fail-fast logic.
func classifyUnreachable(err error) unreachableKind {
	if err == nil {
		return kindUnknown
	}

	// Case 1: TLS hostname mismatch
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return kindHostnameMismatch
	}

	// Case 2: DNS name not found
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return kindDNSNotFound
	}

	// Case 3: Connection refused (portable check)
	if isConnectionRefused(err) {
		return kindRefused
	}

	// Case 4: Timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return kindTimeout
	}

	// Case 5: Non-404 HTTP status
	var httpErr cliconfig.HTTPStatusError
	if errors.As(err, &httpErr) {
		return kindHTTPStatus
	}

	// Case 6: Malformed response body
	var decodeErr cliconfig.DecodeError
	if errors.As(err, &decodeErr) {
		return kindDecode
	}

	// Case 7: Unclassified fallback
	return kindUnknown
}

// isConnectionRefused determines if an error represents a connection refused,
// using a portable approach that works on Windows and Unix.
func isConnectionRefused(err error) bool {
	// Look for a *net.OpError with Op == "dial" that isn't a timeout or DNS error
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		return false
	}

	// Exclude timeouts and DNS errors - what remains is likely connection refused
	var netErr net.Error
	if errors.As(opErr.Err, &netErr) && netErr.Timeout() {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(opErr.Err, &dnsErr) {
		return false
	}

	return true
}

// deploymentChoice represents the result of prompting for a deployment target.
type deploymentChoice struct {
	instanceURL     string // resolved instance URL, or "" for Network
	explicitNetwork bool   // true if user explicitly chose Network (flag or prompt), false for non-explicit cases
	// profileName is the local profile whose name the instance argument matched,
	// when it matched one. Carrying it keeps the credential in the alias the user
	// typed instead of forking a second profile named after the host, which would
	// leave the alias holding stale credentials.
	profileName string
}

// deploymentNoInputError is what a login fails with when it needs to know which
// deployment to target and --no-input forbids asking. Every site that reaches that
// state shares the wording, so a caller is told the same thing — and the same flags
// — wherever the question came up.
const deploymentNoInputError = "cannot ask which deployment to use with --no-input — pass --network, or an instance URL or short name"

// writeEnvNoInputError is the same for the .env question.
const writeEnvNoInputError = "cannot ask whether to write .env with --no-input — pass --write-env or --no-write-env"

// promptDeploymentIfFirstLogin asks which deployment to target when this genuinely is
// a first login, and returns the resolved instance URL and whether Network was
// explicitly chosen. Returns explicitNetwork=false in non-explicit cases: this
// invocation already has a deployment (re-login must not re-interrogate), stdin is not
// a TTY (CI and piped use stay unchanged), or --api-key paths. Returns an error when
// --no-input is set and a prompt would be required.
//
// "Already has a deployment" is clictx.DeploymentSettled(), which is true when either the
// target clictx resolved names an origin or the active profile records a completed login.
// Both facts are needed, for opposite reasons.
//
// An origin with no matching profile is the state a project .env pin leaves — `blocks
// login <B> --write-env` followed by `blocks profile use` something else, and the state
// `blocks logout` deliberately preserves so a later bare `blocks login` returns to B.
// Asking the profile alone found no deployment there, put the question, and offered
// Blocks Network as its default, so pressing Enter authenticated somewhere the directory
// does not name and stored the key in a profile no later command in that directory
// resolves — breaking the promise logout prints.
//
// A recorded login with no origin is the opposite state, and the reason
// DeploymentSettled() exists: a completed stock Blocks Network login stores an empty
// BaseURL, because empty is how "resolve via CDM" is recorded. Asking only whether an
// origin was named read that settled choice as no choice, so every later interactive bare
// login asked again, and --no-input failed unless --network was repeated every time. The
// question is asked of a *recorded* login because an empty stock-Network profile exists
// before the first one — which is what keeps the genuine first run being asked.
//
// ProfileIsTarget() is still deliberately not consulted: a deployment resolved from a pin
// the active profile does not describe is one this invocation is pointed at all the same,
// which is the first case above. The question here is only whether some earlier login
// settled the target, not whether the profile describes it. Nor is the precedence
// re-derived here — the resolver ran in PersistentPreRun and already applied it,
// declineForeignBackendPin included, so a second copy would only drift from the target
// the banner names and publish, register and unregister reach.
func promptDeploymentIfFirstLogin() (deploymentChoice, error) {
	if loginNetwork {
		return deploymentChoice{instanceURL: "", explicitNetwork: true}, nil // --network flag: explicit Network choice
	}
	if clictx.DeploymentSettled() {
		return deploymentChoice{instanceURL: "", explicitNetwork: false}, nil // this invocation already has a deployment
	}
	if credentialSupplied() {
		return deploymentChoice{instanceURL: "", explicitNetwork: false}, nil // API key path, no prompt
	}
	// --no-input is checked ahead of the TTY guard because the two mean different
	// things and only one of them is a licence to decide this question for the
	// caller. Not being on a terminal means the CLI cannot ask, so it takes the safe
	// default below — the mechanism CI relies on today. --no-input means the caller
	// asked not to be asked, which is also a refusal to be guessed at: the flag's
	// contract is that a prompt becomes an error naming the flag that answers it,
	// whether or not stdin is a terminal, and silently landing on Blocks Network
	// would authenticate somewhere the caller never named.
	if noInputMode {
		return deploymentChoice{}, errors.New(deploymentNoInputError)
	}
	if !isTTY() {
		return deploymentChoice{instanceURL: "", explicitNetwork: false}, nil // non-interactive, no prompt
	}
	idx, err := wizard.InteractiveSelect(
		"Which deployment?",
		[]string{"Blocks Network (app.blocks.ai)", "Enterprise instance (URL or short name)"},
		0,
		helpDeploymentChoice,
	)
	if err != nil {
		return deploymentChoice{}, errors.New(deploymentNoInputError)
	}
	if idx == 0 {
		return deploymentChoice{instanceURL: "", explicitNetwork: true}, nil // user explicitly picked Network
	}
	return promptEnterpriseInstance()
}

// enterpriseInstanceAttempts caps the re-prompt after an empty answer. One retry
// covers a stray Enter; looping until an answer arrives would trap a user who
// does not have the instance to hand with no way out but Ctrl-C.
const enterpriseInstanceAttempts = 2

// enterpriseInstanceRequiredError is the error returned when an Enterprise instance URL
// or short name is required but not provided.
const enterpriseInstanceRequiredError = "an Enterprise instance URL or short name is required — rerun as 'blocks login <instanceUrl|shortName>', or pass --network to use Blocks Network"

// promptEnterpriseInstance reads the deployment to target after the user has
// chosen an Enterprise instance. An empty answer is not "no preference": returning
// one would fall through to profile and environment resolution and land on Blocks
// Network, silently contradicting the choice just made. So it is asked for once
// more and then the login fails naming what it needs.
//
// A failed read (EOF) also contradicts the Enterprise choice and returns an error.
func promptEnterpriseInstance() (deploymentChoice, error) {
	for attempt := 0; attempt < enterpriseInstanceAttempts; attempt++ {
		fmt.Print("  Instance URL or short name: ")
		line, ok, noInputRequested := readStdinLine()
		if noInputRequested {
			return deploymentChoice{}, errors.New(deploymentNoInputError)
		}
		if !ok {
			return deploymentChoice{}, fmt.Errorf(enterpriseInstanceRequiredError) // EOF contradicts Enterprise choice
		}
		instanceURL, matchedProfile, err := expandInstanceArg(line)
		if err != nil {
			// A build that cannot expand short names will not expand this one on a
			// retry either, so re-prompting would only ask the user to fix something
			// that is not theirs to fix.
			return deploymentChoice{}, err
		}
		if instanceURL != "" {
			return deploymentChoice{instanceURL: instanceURL, profileName: matchedProfile}, nil // explicit enterprise URL
		}
		fmt.Println("  An Enterprise deployment needs its instance URL or short name — " + instanceArgForms + ".")
	}
	return deploymentChoice{}, fmt.Errorf(enterpriseInstanceRequiredError)
}

// describeUnreachable classifies the failure and returns a human-readable error message.
// It drops the raw error for cases it recognizes, keeping it only for unclassified failures.
//
// Both halves of every message are outside this CLI's control — the URL is what the
// user or a .env named, and an unclassified error's text can carry whatever the far
// end put in it — so both are sanitized here, where they are formatted for a terminal,
// rather than at each of the places this message is printed.
func describeUnreachable(resolvedURL string, err error) string {
	at := termsafe.Text(resolvedURL)
	if err == nil {
		return fmt.Sprintf("could not reach %s", at)
	}

	kind := classifyUnreachable(err)
	switch kind {
	case kindHostnameMismatch:
		return fmt.Sprintf("no Blocks deployment at %s", at)
	case kindDNSNotFound:
		return fmt.Sprintf("%s does not resolve", at)
	case kindRefused:
		return fmt.Sprintf("nothing is listening at %s", at)
	case kindTimeout:
		return fmt.Sprintf("timed out connecting to %s", at)
	case kindHTTPStatus:
		var httpErr cliconfig.HTTPStatusError
		if errors.As(err, &httpErr) {
			return fmt.Sprintf("%s returned HTTP %d", at, httpErr.StatusCode)
		}
		return fmt.Sprintf("%s returned a non-success status", at)
	case kindDecode:
		return fmt.Sprintf("%s did not return a Blocks configuration", at)
	default:
		return fmt.Sprintf("could not reach %s: %s", at, termsafe.Text(err.Error()))
	}
}

// formatHints returns appropriate hints based on the error type and resolved URL structure.
func formatHints(resolvedURL string, err error) string {
	kind := classifyUnreachable(err)
	switch kind {
	case kindHTTPStatus:
		return "  The deployment may be temporarily unavailable. Try again, or contact your administrator."
	case kindDecode, kindUnknown:
		// No hints for malformed response or unclassified errors
		return ""
	case kindHostnameMismatch, kindDNSNotFound, kindRefused, kindTimeout:
		// Host/network reachability issues
		var hints []string

		// Show spelling hint if this looks like a short name expansion. The domain is
		// injected at build time, so it is quoted back sanitized like any other value
		// this CLI did not write.
		if isShortNameExpansion(resolvedURL) {
			hints = append(hints, fmt.Sprintf("  A short name expands to <name>.%s — check the spelling.", termsafe.Text(defaultInstanceDomain)))
		}

		hints = append(hints, "  If your deployment uses a custom domain, pass its full URL instead.")

		return strings.Join(hints, "\n")
	default:
		return ""
	}
}

// isShortNameExpansion determines if the resolved URL matches the pattern of
// a short name expansion: https://<single-label>.<instanceDomain()>. A build whose
// instance domain is unusable expands no short names, so no URL can be one — and
// the hint that quotes the domain is suppressed with it.
func isShortNameExpansion(resolvedURL string) bool {
	u, err := url.Parse(resolvedURL)
	if err != nil || u.Scheme != "https" {
		return false
	}

	domain, err := instanceDomain()
	if err != nil {
		return false
	}

	host := u.Hostname()
	if !strings.HasSuffix(host, "."+domain) {
		return false
	}

	// Extract the part before the instance domain
	prefix := strings.TrimSuffix(host, "."+domain)
	// If it's a single label (no dots), it's likely a short name expansion
	return !strings.Contains(prefix, ".")
}

// requireReachableInstance fails the login when discovery could not reach an
// instance the user explicitly named. For genuine network-level unreachability
// (TLS hostname mismatch, DNS not-found, connection refused, timeout), login
// fails fast. For HTTP status errors, decode errors, and unclassified failures,
// it warns on stderr and continues, since the host answered and browser OAuth
// might succeed where the unauthenticated cli-config probe failed.
//
// Bare `blocks login` passes instanceURL == "" and is unaffected.
func requireReachableInstance(instanceURL, resolvedURL string, err error) error {
	if err == nil || instanceURL == "" {
		return nil
	}

	kind := classifyUnreachable(err)
	message := describeUnreachable(resolvedURL, err)
	hints := formatHints(resolvedURL, err)

	// Only fail fast for genuine network-level unreachability
	switch kind {
	case kindHostnameMismatch, kindDNSNotFound, kindRefused, kindTimeout:
		// Network-level issues: fail fast
		if hints == "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("%s\n\n%s", message, hints)
	case kindHTTPStatus, kindDecode, kindUnknown:
		// Host answered or unclassified: warn and continue
		fmt.Fprintf(os.Stderr, "Warning: %s", message)
		if hints != "" {
			fmt.Fprintf(os.Stderr, "\n\n%s", hints)
		}
		fmt.Fprintf(os.Stderr, "\n")
		return nil
	default:
		return nil
	}
}

// ensureCredentials obtains the credential a login stores: the key handed to
// --api-key, or a fresh one created on the deployment by the browser flow.
//
// It is indirected for one reason: the browser flow binds a fixed local port, opens a
// real browser and then waits minutes for a callback, so no test can reach the state
// this function's callers care most about — a key that now exists on the deployment
// while the local work after it fails. Nothing but a test replaces it.
var ensureCredentials = auth.EnsureCredentialsProfile

// loginOutcome is what a completed login learned: the key it minted, the profile
// it stored the key under, and how to name the deployment it authenticated
// against. The name travels with the result because only loginToProfile knows the
// deployment it actually reached — the caller's own view of the target predates
// the instance argument and the discovery this login performed.
type loginOutcome struct {
	apiKey      string
	profileName string
	targetName  string
}

// loginTargetName names the deployment a login authenticated against, for the
// success line. It is a different question from the one the context banner
// answers: the login has just probed that deployment, so its own product name is
// available and is what the user should read.
//
// Failing that, only three things can be said honestly. An explicit Blocks Network
// request reached Blocks Network, whatever brand a profile for some other
// deployment may have left configured. A login that named no deployment reached
// the one the active profile already describes, so that profile's brand describes
// it too. Any other deployment is named by host, because the active profile's
// brand may belong to an instance this login never touched — naming it is how an
// enterprise brand ends up captioning a login somewhere else entirely.
func loginTargetName(choice deploymentChoice, backendURL string, disco *cliconfig.Config) string {
	if disco != nil && disco.ProductName != "" {
		return disco.ProductName
	}
	d := resolveLoginDeployment(choice)
	switch {
	case d.network:
		return branding.Default()
	case d.url == "":
		return branding.ProductName()
	}
	if _, p, err := profiles.Active(); err == nil && profiles.SameBaseURL(p.BaseURL, backendURL) {
		return branding.ProductName()
	}
	return hostSlug(backendURL)
}

// loginToProfile is the single source of truth for "log in and store a Blocks
// credential". It discovers enterprise/branding/oauth metadata for the target
// deployment, runs the browser (or --api-key) flow, and persists the minted key
// under the resolved profile in contexts.json. Both `blocks login` and the init
// webapp wizard call it, so neither can diverge on where the key is stored. It does
// not touch .env — the caller owns that. The one thing it prints is the deployment
// it is about to authenticate against, because that has to be said here: this is
// where the target is resolved and where the credential leaves the process.
//
// The browser path creates a credential on the deployment partway through, so this
// function has one step no later failure can undo. Every error returned after that
// step announces it; see the deferred report below.
func loginToProfile(ctx context.Context, choice deploymentChoice) (out loginOutcome, retErr error) {
	backendURL := resolveLoginBackend(choice)
	announceLoginTarget(backendURL, resolveLoginDeployment(choice))

	// Discover enterprise/branding/oauth before the OAuth flow. Lenient: a 404
	// or empty URL yields a non-enterprise zero value (nil-safe handling below).
	// Branding must be applied here (not after) so OAuth-flow prompts are branded.
	disco, discoErr := cliconfig.Fetch(backendURL)
	if err := requireReachableInstance(choice.instanceURL, backendURL, discoErr); err != nil {
		return loginOutcome{}, err
	}
	applyBranding(disco)
	clientID := resolveLoginClientID(disco)

	// Named before anything is written to the store: the tiers below read the
	// profile that is active now, and the upsert at the end of this function
	// replaces it with the one this login is creating.
	targetName := loginTargetName(choice, backendURL, disco)

	// A key handed in via --api-key / --api-key-stdin is read from the shared
	// resolver, not from the flags: a piped key can only be consumed once, and the
	// resolver is where that one read happens.
	supplied, wasSupplied, supplyErr := externalCredential()
	if supplyErr != nil {
		return loginOutcome{}, supplyErr
	}

	// Browser path: evict any stale legacy "blocks" credential from
	// credentials.json so the migration-fallback readers (publish's fallback,
	// profiles.Load's migration) can't resurface the old key after this login
	// persists the freshly minted key to the profile store. Remove only the
	// "blocks" namespace so partner credentials are preserved. (--api-key paths
	// skip this — they don't run the browser flow.)
	if !wasSupplied {
		if credPath, pathErr := auth.CredentialPathFunc(); pathErr == nil {
			_ = auth.DeleteProviderCredential(credPath, "blocks")
		}
	}

	minted, apiKey, err := ensureCredentials(ctx, backendURL, clientID, supplied)
	if err != nil {
		return loginOutcome{}, fmt.Errorf("login failed: %w", err)
	}

	profileName := resolveProfileName(choice)

	// From here on the browser path has a live credential on the deployment that no
	// local failure undoes, and every remaining step is local and fallible: the store
	// read, the organization lookup, the write. A caller told only "could not save
	// profile" would never learn that a key exists, so it could be neither reused nor
	// revoked — and because `blocks login` always performs a fresh login, each retry
	// mints another one beside it.
	//
	// The announcement is deferred rather than repeated at each return so a failure path
	// added later cannot be the one that stays silent, and it is registered here rather
	// than at the top of the function because the failures above this line happen before
	// anything was created. It reuses the report the publish picker prints for the same
	// event: one wording for "a key was created and not stored", wherever it happens.
	//
	// A key handed to --api-key was not created by this login, so there is nothing to
	// announce on that path, and a login that gets as far as its success line has already
	// named the key and the profile holding it.
	if !wasSupplied {
		defer func() {
			if retErr != nil {
				reportMintedOrgKey(minted.OrgName, profileName, false)
			}
		}()
	}

	// Load the existing profile (if any) and MERGE — so re-login preserves the
	// deployment target and other cached org keys instead of wiping them.
	store, err := profiles.Load()
	if err != nil {
		return loginOutcome{}, err
	}
	p := store.Profiles[profileName] // zero value if new
	if p.Orgs == nil {
		p.Orgs = map[string]profiles.OrgKey{}
	}
	// A login can land in a profile that describes a different deployment: an explicit
	// --profile names one outright, and two path-prefixed deployments on one host used to
	// share a slug. The profile's BaseURL is about to be repointed by mergeDiscovery, and
	// its cached organization keys were minted at the deployment it described until now —
	// so leaving them would produce a profile that describes B while holding A's keys, and
	// the credential tiers would then hand one of A's keys to B, since the profile does
	// describe the target. Forget them, and say so, rather than mixing deployment-scoped
	// state under one name.
	if retargeted := retargetsAnotherDeployment(p, choice); retargeted {
		fmt.Fprintf(os.Stderr, "Note: profile %q described a different deployment; its cached organization keys and deployment metadata have been discarded.\n", profileName)
		p.Orgs = map[string]profiles.OrgKey{}
		p.DefaultOrgID = ""
		// The branding and OAuth metadata are deployment-scoped too, and mergeDiscovery
		// only overwrites each when discovery answers with a non-empty value — so a new
		// deployment that reports no product name would leave the old one's showing on a
		// profile that now points elsewhere. That is the "believing you are on Enterprise
		// while actually on Network" state the context banner exists to prevent, since the
		// banner reads its name from here. Cleared so discovery repopulates from scratch.
		p.Enterprise = false
		p.ProductName = ""
		p.OAuthClientID = ""
		p.DashboardBaseURL = ""
		// The branding and OAuth metadata are deployment-scoped too, and mergeDiscovery
		// only overwrites each when discovery answers with a non-empty value — so a new
		// deployment that reports no product name would leave the old one's showing on a
		// profile that now points elsewhere. That is the "believing you are on Enterprise
		// while actually on Network" state the context banner exists to prevent, since the
		// banner reads its name from here. Cleared so discovery repopulates from scratch.
	}
	mergeDiscovery(&p, choice, disco)

	orgId, orgName, err := resolveOrgForKey(backendURL, apiKey, minted)
	if err != nil {
		return loginOutcome{}, err
	}
	if orgId != "" {
		p.DefaultOrgID = orgId
		p.Orgs[orgId] = profiles.OrgKey{OrgName: orgName, ApiKey: apiKey, KeyId: minted.KeyId, ExpiresAt: minted.ExpiresAt}
	}

	if err := profiles.Upsert(profileName, p, true); err != nil {
		return loginOutcome{}, fmt.Errorf("failed to save profile: %w", err)
	}
	return loginOutcome{apiKey: apiKey, profileName: profileName, targetName: targetName}, nil
}

// retargetsAnotherDeployment reports whether this login is about to repoint an existing
// profile at a deployment other than the one it already describes, with organization
// state cached against the old one.
//
// An empty BaseURL is stock Blocks Network rather than "no deployment", so the comparison
// asks the Network question separately instead of treating "" as a wildcard — otherwise
// every ordinary Network re-login would look like a retarget and discard its own keys.
// A profile with nothing cached cannot mix state, so it is not a retarget worth reporting.
func retargetsAnotherDeployment(p profiles.Profile, choice deploymentChoice) bool {
	// Anything deployment-scoped is enough to make a retarget worth cleaning, not just a
	// cached key. A logged-out profile has no keys but keeps its enterprise verdict,
	// product name, OAuth client id and dashboard origin — and discovery only overwrites
	// each of those when it answers with a non-empty value, so gating on keys alone let a
	// logged-out profile carry deployment A's branding into deployment B.
	if len(p.Orgs) == 0 && p.DefaultOrgID == "" && !p.Enterprise &&
		p.ProductName == "" && p.OAuthClientID == "" && p.DashboardBaseURL == "" {
		return false
	}
	deployment := resolveLoginDeployment(choice)
	if p.BaseURL == "" || deployment.network {
		// One of the two is stock Network; they differ only if the other is not.
		return (p.BaseURL == "") != deployment.network
	}
	return !profiles.SameBaseURL(p.BaseURL, deployment.url)
}

// loginDeployment is the deployment a login targets, expressed as the user's own
// choice rather than as a URL. It is the single place that choice is decided:
// resolveLoginBackend turns it into an origin to authenticate against, and
// loginBackendPin turns it into the BLOCKS_BACKEND_URL that belongs in .env
// beside the minted key. Deriving both from one value is what stops .env pairing
// a key minted at one deployment with the URL of another.
type loginDeployment struct {
	// url is the deployment's origin, or "" when the user named no deployment and
	// the build's own resolution decides the target.
	url string
	// network is an explicit Blocks Network request (--network, or picking Network
	// at the prompt). It outranks every ambient input and needs no .env pin.
	network bool
}

// resolveLoginDeployment layers the login's explicit choice over the deployment
// this invocation is already pointed at. An explicitly named instance wins; then
// an explicit Network request, which deliberately bypasses BLOCKS_BACKEND_URL and
// the active profile so it cannot be dragged back to an enterprise instance;
// otherwise the deployment the caller pointed at, as resolved once by clictx —
// BLOCKS_BACKEND_URL, else the active profile's own backend, else the deployment a
// CDM endpoint the environment redirected names (redirectedCDMDeployment). When the
// caller pointed at nothing, url is "" and the build resolves Blocks Network itself.
//
// The last tier is the whole reason this function may not stop at
// clictx.ChosenBackendURL(), which is local by design: a bare login under a
// redirected endpoint authenticates against the deployment that endpoint names, and a
// login has to record the deployment it authenticated against. Only that tier can
// need a fetch, and only the bare case reaches it — the two explicit targets return
// above, so neither can be made to resolve the endpoint it is about to set aside.
//
// Login needs no rule of its own about where an ambient BLOCKS_BACKEND_URL came
// from. That rule lives in declineForeignBackendPin (cmd/root.go), which runs before
// the target is resolved and, for every command alike, drops a value that only a
// project .env supplied and that names a deployment no saved profile describes. So
// by the time this runs, the deployment the caller pointed at is one the user has
// deliberately logged in to, and login follows it like everything else does. Do not
// add a second copy of that rule here: two copies drift, and the drift shows up as a
// login that authenticates against a deployment other than the one the banner names
// and publish, register and unregister reach.
func resolveLoginDeployment(choice deploymentChoice) loginDeployment {
	switch {
	case choice.instanceURL != "":
		return loginDeployment{url: choice.instanceURL}
	case choice.explicitNetwork:
		return loginDeployment{network: true}
	case clictx.ChosenBackendURL() != "":
		return loginDeployment{url: clictx.ChosenBackendURL()}
	}
	return loginDeployment{url: redirectedCDMDeployment()}
}

// redirectedCDMDeployment is the deployment named only by a CDM endpoint the
// environment pointed this invocation at, and "" whenever some other tier named the
// target or nothing named one at all.
//
// It is a tier of the login's target precedence because the CDM payload carries
// api.baseUrl: with a stock Blocks Network profile and BLOCKS_CDM_URL exported, that
// value is where a bare login actually authenticates — clictx resolves it and every
// request follows it — so it is also the deployment the login must record. Recording
// anything else left the minted key in a profile that describes somewhere else, beside
// no pin at all, which is a key no later command in that directory can spend.
//
// Whether the endpoint was redirected is asked of clictx rather than recomputed here.
// With no local tier having named an origin, the active profile stops being the target
// exactly when that endpoint names a deployment of its own, so ProfileIsTarget() is
// that verdict — and it costs no round-trip, because the presence of a redirect is a
// local fact. A second copy of the rule here would be free to drift from the one the
// credential tiers apply, and the drift would show up as a login that stores a key in a
// profile the next invocation refuses to spend it from.
//
// Each guard excludes a tier that is not a deployment the user chose:
//
//   - clictx.BackendURL() != "" — a local tier, or the build's own default, already
//     named the origin. The local tiers are the case above; the build-time default
//     describes how a build finds a target it was not told about, and recording it
//     would freeze an answer the CLI is meant to resolve for itself.
//   - no active profile — nothing was displaced, so there is no evidence that a
//     redirect rather than the built-in default answered, and the login records what it
//     recorded before this tier existed.
//   - the profile is still the target — the endpoint is the build's own, the deployment
//     behind it is Blocks Network, and Blocks Network needs no pin.
//
// A fetch that fails yields "", which leaves resolveLoginBackend to fall through to
// resolveBackendURL — the one place that reports the failure, so it is not reported
// twice for one invocation.
func redirectedCDMDeployment() string {
	if clictx.BackendURL() != "" {
		return ""
	}
	if clictx.Profile() == "" || clictx.ProfileIsTarget() {
		return ""
	}
	url, err := clictx.EffectiveBackendURL()
	if err != nil {
		return ""
	}
	return url
}

// announceLoginTarget names the deployment this login will authenticate against
// before any credential is sent: a key supplied to --api-key is transmitted to the
// resolved deployment during org resolution, so naming the host that will receive it
// while the login can still be stopped is worthwhile even when the target is one the
// user already trusts.
func announceLoginTarget(backendURL string, d loginDeployment) {
	target := branding.Default()
	if !d.network && backendURL != "" {
		target = hostSlug(backendURL)
	}
	// The host is whatever named this deployment — an argument, a .env, a profile —
	// and this line is the last thing printed before a credential can leave, so it is
	// the one line that must not be forgeable.
	fmt.Printf("  Logging in to %s\n", termsafe.Text(target))
}

// resolveLoginBackend is the origin the login authenticates against. Both explicit
// targets resolve through a helper that first drops the CDM state belonging to
// whatever deployment this invocation was already pointed at, because that state
// carries the OAuth client id the login falls back to.
func resolveLoginBackend(choice deploymentChoice) string {
	d := resolveLoginDeployment(choice)
	if d.network {
		return networkBackendURL()
	}
	if choice.instanceURL != "" {
		return instanceBackendURL(choice.instanceURL)
	}
	if d.url != "" {
		return d.url
	}
	return resolveBackendURL()
}

// instanceBackendURL is the origin an explicitly named instance login authenticates
// against: the instance itself, with any CDM state that answers for a different
// deployment dropped first.
//
// An instance argument names the target outright and outranks every ambient input,
// BLOCKS_BACKEND_URL included. BLOCKS_CDM_URL is an ambient input of exactly the same
// kind — the payload it serves carries api.baseUrl and the OAuth client id, and that
// client id is the one this login falls back to when the named deployment's own
// discovery reports none. So a directory pinned to deployment A had `blocks login <B>`
// present A's client id to B: an authorization request for one deployment's OAuth
// client sent to another, which is the hole the --network path already closes.
//
// A pin naming this instance's own CDM endpoint is kept, because that is precisely
// what `blocks login <B> --write-env` leaves behind in B's project directory and it
// answers for the deployment being logged in to.
func instanceBackendURL(instanceURL string) string {
	dropForeignCDMState(hostSlug(instanceURL), cdm.EndpointFor(instanceURL))
	return instanceURL
}

// networkBackendURL is Blocks Network's own API origin, resolved from remote config
// with any ambient BLOCKS_CDM_URL dropped first.
//
// The CDM payload carries api.baseUrl, so that variable is an answer to "where is
// Blocks Network" — and an endpoint served by an enterprise deployment answers it
// with that deployment. The login would authenticate there and store the key it
// minted in the profile that describes Blocks Network, whose enterprise metadata an
// explicit Network choice deliberately clears: a key minted at one deployment, in the
// profile every later command resolves to another. An explicit Network choice is made
// on the command line or at the prompt and already outranks every ambient input,
// BLOCKS_BACKEND_URL included; this variable can redirect a credential exactly as
// that one can, so it cannot be the one ambient value that still redefines it.
//
// The value is unset rather than ignored, for the reason declineForeignBackendPin
// unsets its own: cdm.Get is not its only reader, and everything else this login
// resolves from remote config — the OAuth client id among them — has to answer for
// the deployment this function returned. Unsetting also drops the payload cdm may
// already have memoized from it, so the fetch that follows is the one this login
// asked for.
func networkBackendURL() string {
	dropForeignCDMState(branding.Default(), "")
	cfg, err := cdm.Get()
	if err != nil || cfg == nil || cfg.Api.BaseURL == "" {
		return "" // remote config failed, caller will handle
	}
	return cfg.Api.BaseURL
}

// dropForeignCDMState removes an ambient BLOCKS_CDM_URL that does not answer for the
// deployment this login asked for, and invalidates anything cdm has already resolved
// from it, saying so on stderr when there was a value to drop: a user who set the
// variable has to be able to see that this login did not follow it, and the value is
// attacker-chosen text on that path, so it cannot reach the terminal unsanitized.
//
// target names the deployment for the message. keep is the one CDM endpoint this
// login accepts — a named instance's own. An explicit Network choice accepts none:
// Blocks Network's configuration is the build's own default, so no endpoint named by
// the environment can be it, and "" therefore drops every value.
//
// Both explicit targets share this one function deliberately. They are the same rule
// — an explicitly named target may not have its remote configuration redefined by the
// environment — and two copies of it drift, which is how the instance path came to be
// missing the half the --network path had.
func dropForeignCDMState(target, keep string) {
	pinned, ok := os.LookupEnv(cdm.URLEnv)
	if !ok {
		return
	}
	// EndpointFor normalizes its own output, so both sides are compared trimmed.
	if keep != "" && strings.TrimRight(strings.TrimSpace(pinned), "/") == keep {
		return
	}
	os.Unsetenv(cdm.URLEnv)
	cdm.Reset()
	if strings.TrimSpace(pinned) == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "  Not using %s=%s — you asked for %s, whose configuration that endpoint does not serve.\n",
		cdm.URLEnv, termsafe.Text(pinned), termsafe.Text(target))
}

// applyBranding sets the global product name from discovery metadata, if present.
func applyBranding(disco *cliconfig.Config) {
	if disco != nil && disco.ProductName != "" {
		branding.Set(disco.ProductName)
	}
}

// resolveLoginClientID resolves the OAuth client id, preferring the deployment's
// discovered id over the env/CDM default.
func resolveLoginClientID(disco *cliconfig.Config) string {
	clientID := resolveClientID()
	if disco != nil && disco.OAuthClientID != "" {
		clientID = disco.OAuthClientID
	}
	return clientID
}

// resolveProfileName chooses where to store the login: an explicit
// --profile / BLOCKS_PROFILE selection names it; otherwise when Network is
// explicitly requested return the default profile; otherwise the profile the
// instance argument named, then a host slug for an explicit instance, then a host
// slug for a deployment the active profile does not describe, else the active
// profile (default blocks-network).
//
// The matched-profile tier keeps `blocks login <alias>` writing back into
// <alias>. Resolving the alias to its base URL and then naming the destination
// after that URL's host would create a second profile and leave the alias
// holding the credential it just replaced.
//
// The tier below it exists because a credential belongs to the deployment it was
// minted at, and only that deployment. A login pointed at another deployment by an
// ambient BLOCKS_BACKEND_URL used to land in whichever profile happened to be
// active, leaving that profile holding a key for somewhere else — and, since the
// profile's own BaseURL was untouched, sending that key to its own deployment as
// soon as the ambient value went away. So a target the active profile does not
// describe gets its own profile — the one that already describes that deployment
// where there is one, and otherwise a new profile named after its host, with
// mergeDiscovery recording the deployment there.
func resolveProfileName(choice deploymentChoice) string {
	if sel := profiles.SelectedName(); sel != "" {
		return sel
	}
	if resolveLoginDeployment(choice).network {
		return profiles.DefaultProfile
	}
	if choice.profileName != "" {
		return choice.profileName
	}
	if choice.instanceURL != "" {
		return profileNameFor(choice.instanceURL)
	}
	name, p, err := profiles.Active()
	if err != nil {
		return profiles.DefaultProfile
	}
	if target := resolveLoginDeployment(choice).url; target != "" && !profiles.SameBaseURL(p.BaseURL, target) {
		return profileNameFor(target)
	}
	return name
}

// profileNameFor names the profile a login against target belongs in: the profile
// that already describes that deployment, else the target's host.
//
// Preferring the existing profile is the same rule the matched-alias tier applies to
// `blocks login <alias>`, asked the other way round — by deployment rather than by the
// name that was typed — because the argument is not the only way a login can arrive at
// a deployment a profile already describes. A project .env pin is honoured precisely
// when some profile describes the deployment it names, and the profile that describes
// it may be an alias like `acme` rather than the host. Naming the destination after the
// host in that case forks a second profile for one deployment and leaves the alias
// holding the credential this login replaced.
func profileNameFor(target string) string {
	if matched := profileDescribing(target); matched != "" {
		return matched
	}
	return hostSlug(target)
}

// profileDescribing returns the name of the saved profile whose deployment is the one
// at rawURL, or "" when no profile describes it. "Same deployment" is asked of
// profiles.SameBaseURL, so a trailing slash or a default port cannot hide a match.
//
// Where more than one profile describes a deployment the first name in lexical order
// wins, so the answer does not depend on map iteration order: a login that alternated
// between two profiles for one deployment would leave whichever it missed holding a
// stale credential.
func profileDescribing(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	c, err := profiles.Load()
	if err != nil {
		return ""
	}
	matched := ""
	for name, p := range c.Profiles {
		if !profiles.SameBaseURL(rawURL, p.BaseURL) {
			continue
		}
		if matched == "" || name < matched {
			matched = name
		}
	}
	return matched
}

// mergeDiscovery folds the deployment this login authenticated against, and its
// discovery metadata, into the profile — preserving existing values when the new
// ones are empty. A login that named no deployment at all leaves BaseURL untouched
// (DO NOT overwrite it with an empty value) so re-login against the default profile
// keeps CDM resolution.
//
// BaseURL is recorded for every deployment the login actually reached, not only for
// an explicit instance argument: the profile that receives the key is the one every
// later command resolves its backend from, and a profile holding a key minted
// elsewhere while recording a different deployment — or none — is exactly how that
// key gets sent to the wrong place.
//
// Only applies discovery metadata when the profile genuinely corresponds
// to the discovered backend, or when Network is explicitly chosen (self-healing).
func mergeDiscovery(p *profiles.Profile, choice deploymentChoice, disco *cliconfig.Config) {
	// Self-healing: when Network is explicitly requested, actively clear stale
	// enterprise metadata so users whose store is already corrupted get fixed
	if choice.explicitNetwork {
		p.BaseURL = ""
		p.Enterprise = false
		p.ProductName = ""
		p.OAuthClientID = ""
		p.DashboardBaseURL = ""
		return // no discovery metadata to apply for explicit Network
	}

	if target := resolveLoginDeployment(choice).url; target != "" && !profiles.SameBaseURL(p.BaseURL, target) {
		p.BaseURL = target
	}

	// Only apply discovery metadata when the profile genuinely corresponds to
	// the discovered backend: either an explicit instanceURL is given, or the
	// profile's existing BaseURL matches the backend that was probed
	if disco == nil {
		return
	}

	shouldApplyMetadata := choice.instanceURL != "" ||
		profiles.SameBaseURL(p.BaseURL, resolveLoginBackend(choice))

	if shouldApplyMetadata {
		p.Enterprise = disco.Enterprise
		if disco.ProductName != "" {
			p.ProductName = disco.ProductName
		}
		if disco.OAuthClientID != "" {
			p.OAuthClientID = disco.OAuthClientID
		}
		if disco.DashboardBaseURL != "" {
			p.DashboardBaseURL = disco.DashboardBaseURL
		}
	}
}

// resolveOrgForKey determines the org for the minted/supplied key. The OAuth path
// returns org metadata directly; the --api-key / --stdin path does not, so it is
// resolved from the backend (publish-context returns {orgId, orgName}). A key with
// no resolvable org can't be cached (DefaultOrgKey can't surface it), so storing
// nothing while printing success would strand the user with an unusable login —
// fail loudly instead. (The browser/OAuth path always resolves an org, so this
// only guards the --api-key / --api-key-stdin path when publish-context lookup
// fails — e.g. an invalid key or wrong URL.)
func resolveOrgForKey(backendURL, apiKey string, minted *auth.Credentials) (string, string, error) {
	orgId, orgName := minted.OrgId, minted.OrgName
	if orgId == "" && apiKey != "" && backendURL != "" {
		if pc := registry.FetchPublishContext(backendURL, apiKey); pc != nil {
			orgId, orgName = pc.OrgID, pc.OrgName
		}
	}
	if orgId == "" && apiKey != "" {
		target := backendURL
		if target == "" {
			target = "the target instance"
		}
		return "", "", fmt.Errorf("could not determine the organization for this API key from %s — the key was not stored; verify the key is valid and the instance URL is correct, then retry", termsafe.Text(target))
	}
	return orgId, orgName, nil
}

// applyEnvMutations rewrites the project .env with every mutation at once,
// applying the login command's error policy: a failure is fatal only when
// --write-env was explicitly requested, otherwise it degrades to a warning so the
// login still succeeds. Returns a non-nil error only in the fatal case.
func applyEnvMutations(envDir string, mutations ...auth.EnvMutation) error {
	if err := auth.ApplyEnvAt(envDir, mutations...); err != nil {
		if loginWriteEnv {
			return fmt.Errorf("failed to write .env: %w", err)
		}
		fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
	}
	return nil
}

// maybeWriteEnv injects BLOCKS_API_KEY into the project .env when shouldWriteEnv
// allows it, and the two values that decide where that key is spent alongside it
// when the login targeted a specific deployment: BLOCKS_BACKEND_URL and that
// deployment's own CDM endpoint. Without them, a script the user runs directly (a
// trigger or consumer, not `blocks run`) resolves everything from the public CDM and
// silently reaches Blocks Network regardless of which deployment they logged in to.
//
// Both are needed because they steer different halves of a runtime. The backend URL
// overrides only the REST origin, and only for a consumer; an agent runtime resolves
// its PubNub keysets — and its own REST origin — from the CDM, so a pin on its own
// leaves a script talking REST to one deployment while subscribed to another's
// keyset. A deployment serves its own CDM payload and reports itself as the api
// origin in it, so pointing a runtime at that endpoint settles both questions.
//
// Stock Network needs neither and is deliberately left unpinned; stale values left
// over from an earlier deployment are removed, so the file can never pair a Network
// key with an enterprise deployment's targeting.
//
// The pin is derived from the deployment the login actually reached, not from the
// active profile: a login that followed an ambient BLOCKS_BACKEND_URL stores its
// key in a profile named after that deployment, and reading the active profile here
// would delete the very pin that sent the login to that backend. A write failure is
// fatal only when explicitly requested via --write-env; otherwise it degrades to a
// warning so the login still succeeds.
//
// The key and the targeting are one fact about one deployment, so they are applied
// as one rewrite. Written separately, a failure or interruption between them leaves
// the freshly minted key beside the previous deployment's URL — the wrong-target
// state the pin exists to prevent, and the more dangerous of the partial outcomes.
func maybeWriteEnv(apiKey string, choice deploymentChoice) error {
	writeEnv, err := shouldWriteEnv()
	if err != nil {
		return err
	}
	if !writeEnv {
		return nil
	}
	envDir := loginDir
	if envDir == "" {
		envDir = mustCwd()
	}

	// The CDM endpoint travels with the pin rather than with the enterprise verdict:
	// a local development backend is not an enterprise deployment and still serves
	// the keysets a runtime must resolve from it. Both are settled before the key in
	// the same rewrite, and both are dropped together for stock Network.
	pin := loginBackendPin(choice)
	cdmEndpoint := cdm.EndpointFor(pin)
	return applyEnvMutations(envDir,
		auth.EnvMutation{Key: blocksBackendURLEnv, Value: pin, Remove: pin == ""},
		auth.EnvMutation{Key: cdm.URLEnv, Value: cdmEndpoint, Remove: cdmEndpoint == ""},
		auth.EnvMutation{Key: blocksAPIKeyEnv, Value: apiKey},
	)
}

// loginBackendPin reports the backend URL that belongs in .env beside the key the
// login just minted, or "" when the login targeted stock Network (which needs no
// pin, and whose key must not be left sitting next to a stale one).
//
// It reads the same loginDeployment resolveLoginBackend authenticated against, so
// the pin can only ever name the deployment the key was minted at. What a build
// resolves for itself is excluded — the build-time default, and remote config the
// environment did not redirect — because those describe how the CLI finds Blocks
// Network rather than a deployment the user chose, and pinning them would freeze an
// answer the CLI is meant to resolve for itself.
//
// A CDM endpoint the environment did redirect is not one of those: it names a
// deployment as deliberately as BLOCKS_BACKEND_URL does, the key really was minted
// there, and a key beside no pin is exactly what left one unusable. See
// redirectedCDMDeployment.
func loginBackendPin(choice deploymentChoice) string {
	d := resolveLoginDeployment(choice)
	if d.network {
		return ""
	}
	return d.url
}

// resolveInstanceArg expands a `blocks login <arg>` argument into a base URL and
// reports the local profile the argument named, when it named one. Order matters
// — the first matching rule wins:
//
//  1. contains a scheme          -> that URL, once it is one a deployment can have
//  2. names a local profile      -> that profile's BaseURL, and the profile name
//     (custom domains, --profile aliases)
//  3. a plausible bare host      -> prepend https://
//  4. a single DNS label         -> https://<arg>.<defaultInstanceDomain>
//
// Rule 2 sits ahead of rules 3 and 4 so a customer on an agreed custom domain
// can keep using a short alias after their first login. It returns the matched
// name alongside the URL because the alias, not the URL's host, is where the
// login belongs — see resolveProfileName. Rule 3 fixes an input that fails
// today: a schemeless host was passed through raw and produced a schemeless URL
// that errored inside http.Get.
//
// Every rule answers with an authority the login will send an API key to, so every
// rule has to establish that the authority is the host the argument appears to name,
// and every rule that builds a URL is held to one definition of what a deployment
// origin may be: deploymentURL. Rules 3 and 4 first refuse anything that is not a
// host before they prefix a scheme — "https://" + "acme/blocks.example.com" is a URL
// whose HOST is acme with everything else a path, and the login would then discover,
// authenticate and send its API key to acme — and then hand the URL they built to
// that same check, so a schemeless argument cannot carry something a schemed one is
// refused for. It carried exactly one: a port outside 1-65535, which
// `acme.example.com:99999` was accepted with and `https://acme.example.com:99999`
// refused for, two answers to one input. Discovery accepts a 404 as reachable, so
// nothing later in the flow would notice any of these mistakes. A "" result means
// "this argument cannot name a deployment"; the caller words the error, since only it
// knows whether the argument came from the command line or from the prompt.
//
// Rule 4 concatenates a second value the user did not type — the build-time
// instance domain — so it goes through instanceDomain(), which refuses a suffix
// that would relocate the authority the same way, and then measures the hostname it
// actually built: the suffix being a legal DNS name says nothing about whether
// <label>.<suffix> still is, and an overlength hostname resolves nowhere. See
// expandInstanceArg for why those failures carry a reason instead of a bare "".
//
// Rule 2 is the one rule that validates nothing: it returns a saved profile's stored
// BaseURL exactly as saved. That is deliberate — the store holds it only because a
// login already reached that deployment through these rules, and re-checking it would
// refuse an alias for a deployment saved before a rule tightened.
func resolveInstanceArg(arg string) (string, string) {
	url, profile, _ := expandInstanceArg(arg)
	return url, profile
}

// expandInstanceArg is resolveInstanceArg plus the reason an argument was refused,
// when that reason is a defect in the binary rather than in the argument. Only
// rule 4 can produce one: it is the only rule that concatenates a build-time value
// into a URL, and so the only rule whose failure the user cannot fix by retyping.
// Callers that word an error use this form and surface that reason as-is;
// resolveInstanceArg is the same resolution for callers that only need the result.
func expandInstanceArg(arg string) (string, string, error) {
	arg = strings.TrimRight(strings.TrimSpace(arg), "/")
	if arg == "" {
		return "", "", nil
	}
	if strings.Contains(arg, "://") {
		return deploymentURL(arg), "", nil
	}
	if c, err := profiles.Load(); err == nil {
		if p, ok := c.Profiles[arg]; ok && p.BaseURL != "" {
			// The *argument* rules deliberately do not re-run on an alias — it is a saved
			// name, not an address someone typed. The origin rule is a different question
			// and does apply: this value is about to receive OAuth codes and an API key,
			// and a profile saved before a rule tightened, or edited by hand, can hold
			// cleartext HTTP or a deceptive authority. Refused by name rather than dialled.
			if verr := origin.Validate(p.BaseURL); verr != nil {
				// The profile name goes on its own line, away from the commands, because a
				// line that reads as something to paste gets pasted and this value comes
				// from the profile store.
				return "", "", fmt.Errorf("profile %q records a deployment URL that %w\nRe-run 'blocks login <url>' for that deployment, or remove the profile with 'blocks profile remove'", arg, verr)
			}
			return p.BaseURL, arg, nil
		}
	}
	if strings.Contains(arg, ".") {
		if !isPlausibleHost(arg) {
			return "", "", nil
		}
		return deploymentURL("https://" + arg), "", nil
	}
	if !isDNSLabel(arg) {
		return "", "", nil
	}
	domain, err := instanceDomain()
	if err != nil {
		return "", "", err
	}
	host := arg + "." + domain
	if len(host) > maxDNSNameLength {
		return "", "", fmt.Errorf(
			"this build cannot expand %q: with %s=%q the hostname would be %d characters, and a hostname may be at most %d — pass the deployment's full URL (https://blocks.acme.com) instead, and report the bad build",
			arg, instanceDomainEnv, domain, len(host), maxDNSNameLength)
	}
	return deploymentURL("https://" + host), "", nil
}

// deploymentURL returns an explicitly schemed instance argument unchanged once it is
// a URL a deployment can have, and "" when it is not one.
//
// A scheme is not evidence that the authority is the host it looks like:
// `https://blocks.acme.example@collector.example` is a URL whose HOST is
// collector.example, because everything before the @ is userinfo. So the schemed form
// is parsed and held to the same rule the schemeless forms are — the authority has to
// be a host and nothing else — and, additionally, to a scheme rule the schemeless
// forms do not need, since they choose their own scheme: a credential must not be
// sent in clear text to anywhere but the local machine. That is the policy
// deploy.ValidateWebAppURL already applies to a URL this CLI accepts, down to the
// three spellings of loopback, and stating it differently here would be a second
// answer to one question.
//
// Everything a real deployment carries is preserved: a custom port, and a path prefix
// for a deployment served under one.
//
// A query and a fragment are refused because this value is an origin the login
// appends endpoint paths to as a string, not a URL it re-parses. `https://host?x=1`
// parses with the host it appears to name, so the authority rule above passes it —
// and then every probe and every authenticated request goes to
// `https://host?x=1/api/v1/...`, a path on that host that no deployment serves. A
// fragment is the same mistake with a `#`, which additionally comments out the path
// the CLI meant to reach. Neither can be part of a deployment's origin, so neither is
// dropped silently: an argument carrying one may have been mistyped from something
// else entirely, and only the user knows what.
//
// The port is range-checked here for the same reason: url.Parse only requires digits,
// so `https://host:99999` is accepted as a URL and fails much later, inside the first
// request — after the login has announced its target and begun. What a deployment can
// be reached at is knowable now. A bracketed host is checked here for the same reason
// again; see bracketedHostIsAnIPLiteral.
// The rules themselves live in internal/origin, because the tiers that reach the same
// requests without being typed — an exported BLOCKS_BACKEND_URL, a stored profile, the
// linker default, a CDM api.baseUrl — have to be held to them too, and a check that
// lives here is a check only this argument gets.
func deploymentURL(arg string) string {
	if origin.Valid(arg) {
		return arg
	}
	return ""
}

// bracketedHostIsAnIPLiteral reports whether a bracketed authority really contains an
// IP address. authority is the parsed URL's Host (which carries the brackets and any
// port) and hostname is its Hostname() (which carries neither).
//
// Brackets are the IPv6-literal syntax, and url.Parse checks only that they are
// balanced and that whatever follows them is a well-formed port — not that they contain
// an address. So `https://[x]` parses, reports its host as `[x]` and its hostname as
// `x`, and satisfied every other rule here; the login then announced a target and died
// inside its first request against an authority that can never resolve.
// `https://[gggg::1]` is the same mistake spelled to look like an address. Both are
// knowable now, which is the rule this whole function group follows.
//
// Anything bracketed is therefore held to net.ParseIP, which leaves exactly the
// bracketed hosts a deployment can have — `[::1]`, the loopback spelling this CLI
// documents, and a real global address — accepted.
//
// A host with no brackets is not this function's business: url.Parse gives it no
// special meaning, and isPlausibleHost already holds it to one shape.
func bracketedHostIsAnIPLiteral(authority, hostname string) bool {
	if !strings.HasPrefix(authority, "[") {
		return true
	}
	return net.ParseIP(hostname) != nil
}

// isDeploymentPort reports whether port — the port component of a parsed URL, which
// is "" when none was given — is one a deployment can actually listen on. url.Parse
// checks only that it is digits, and a TCP port is 1 to 65535: 0 is the "any port"
// wildcard and never an address to dial, and anything above the range cannot be
// dialled at all.
func isDeploymentPort(port string) bool {
	if port == "" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= maxTCPPort
}

// maxTCPPort is the highest port number a TCP address can name.
const maxTCPPort = 65535

// isLoopbackHost reports whether host is one of the three spellings of the local
// machine — the only hosts a plain-http instance URL may name.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// instanceArgForms names every form `blocks login <arg>` accepts. The argument
// error and the prompt's retry line share it so a user is told the same thing
// wherever they meet it.
//
// The port clause is there because a bare host may carry one and is held to the same
// range a full URL is. Without it the refusal of `acme.example.com:99999` named three
// forms the argument already matched and never mentioned the rule it broke.
const instanceArgForms = "pass a short name (acme), a bare host (blocks.acme.com), or a full URL (https://blocks.acme.com), with a port in 1-65535 if any"

// instanceURLForms names what a full URL has to be. An argument that already carried
// a scheme has chosen its form, so listing the three forms back at it explains
// nothing; what it needs is the rule its own form failed.
const instanceURLForms = "a deployment URL is https://<host> (http:// only for localhost, 127.0.0.1 or [::1]), its authority must be a host, optionally with a port in 1-65535 — not user@host — and it may carry a path prefix but no query string and no fragment"

// instanceArgError reports an argument that cannot name a deployment, naming what
// is accepted instead. It deliberately does not guess at an intended host: an
// argument carrying a path or userinfo could be read several ways and only the user
// knows which deployment was meant.
func instanceArgError(arg string) error {
	forms := instanceArgForms
	if strings.Contains(arg, "://") {
		forms = instanceURLForms
	}
	return fmt.Errorf("%q does not name a deployment — %s", arg, forms)
}

// hostReservedChars are the characters that make a schemeless argument mean
// something other than the host it appears to name once a scheme is prefixed: `/`
// (and `\`, which some parsers read as `/`) ends the authority and turns the rest
// into a path, `@` turns everything before it into userinfo, and `?` and `#` cut the
// authority short.
const hostReservedChars = "/\\?#@"

// isPlausibleHost reports whether a schemeless argument can only be read as a
// host, optionally with a port. It rejects the characters that would relocate the
// authority and anything at or below a space (whitespace and control characters),
// and deliberately allows everything else a real deployment host might carry —
// including a port and non-ASCII labels — because refusing a reachable host is its
// own kind of failure.
func isPlausibleHost(s string) bool {
	if strings.ContainsAny(s, hostReservedChars) {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// isDNSLabel reports whether s is a single DNS label (RFC 1035): 1 to 63 letters,
// digits and hyphens, not starting or ending with a hyphen. A short name becomes
// one label of a hostname, so anything else — a dot, a slash, a colon, an @,
// whitespace — means the argument is not a short name and must not be expanded as
// though it were.
func isDNSLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// maxDNSNameLength is the longest a DNS name may be (RFC 1035), counting the dots
// between its labels.
const maxDNSNameLength = 253

// maxInstanceSuffixLength is the longest suffix a short name can expand under. The
// expansion is <label>.<suffix>, and the shortest label plus the dot joining it
// accounts for two of the characters a name is allowed, so a suffix past this length
// can expand nothing at all — it is not a usable instance domain, however
// well-formed it is. It is a floor, not the whole rule: a suffix inside the limit
// still leaves a longer short name overflowing, which is why expandInstanceArg
// measures the name it actually built.
const maxInstanceSuffixLength = maxDNSNameLength - 2

// isDNSSuffix reports whether s is a plausible DNS suffix: one or more
// dot-separated DNS labels, short enough to expand a short name under. It is defined in
// terms of isDNSLabel so "what is a DNS label" has exactly one definition here —
// which is also what rules out everything that would make a suffix relocate the
// authority it is appended to, without enumerating those characters a second
// time: a scheme, userinfo (@), a path (/), a query, a port (:), an empty label
// from a leading or trailing dot, whitespace and control characters.
func isDNSSuffix(s string) bool {
	if s == "" || len(s) > maxInstanceSuffixLength {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !isDNSLabel(label) {
			return false
		}
	}
	return true
}

// hostSlug turns https://blocks.acme.com into "blocks.acme.com". It falls back
// to the raw URL when it cannot be parsed into a host.
func hostSlug(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	// The path prefix is part of the name because it is part of the deployment: two
	// tenants on one host (`https://host/tenant-a` and `/tenant-b`) are different
	// deployments with different credentials, and naming both after the host alone put
	// them in one profile — which then described one tenant while holding the other's
	// keys. A host-only deployment, which is nearly all of them, is unaffected.
	if prefix := strings.TrimRight(u.EscapedPath(), "/"); prefix != "" && prefix != "/" {
		return u.Host + prefix
	}
	return u.Host
}

// shouldWriteEnv decides whether to inject BLOCKS_API_KEY into the project .env.
// Returns an error when --no-input is set and a prompt would be required.
// Order of precedence:
//  1. --no-write-env flag set -> no, unconditionally (skip the prompt too).
//  2. --write-env flag set -> yes, unconditionally.
//  3. a key supplied via --api-key / --api-key-stdin (implies automation) -> no,
//     unless --write-env.
//  4. --no-input with neither flag -> error naming the flag that answers this. The
//     tiers above are answers the caller already supplied; from here on there is
//     none, and --no-input refuses a guess as much as it refuses a prompt.
//  5. Non-interactive stdin (piped / CI) -> no, keep login side-effect-free.
//  6. Interactive terminal -> prompt the user (default yes).
//
// Tier 4 sits ahead of tier 5 deliberately: "cannot ask" and "asked not to ask" are
// different requests and only the first licenses a default. Off a terminal without
// --no-input, tier 5 keeps today's silent no — the behaviour CI depends on.
func shouldWriteEnv() (bool, error) {
	if loginNoWriteEnv {
		return false, nil
	}
	if loginWriteEnv {
		return true, nil
	}
	if credentialSupplied() {
		return false, nil
	}
	if noInputMode {
		return false, errors.New(writeEnvNoInputError)
	}
	if !isInteractive() {
		return false, nil
	}
	fmt.Print("  Write credentials to project .env? (Y/n): ")
	// The --no-input signal readStdinLine reports is already handled above, so this
	// read can only be a real one.
	line, ok, _ := readStdinLine()
	if !ok {
		return true, nil
	}
	ans := strings.ToLower(line)
	if ans == "" {
		return true, nil
	}
	return ans != "n" && ans != "no", nil
}
