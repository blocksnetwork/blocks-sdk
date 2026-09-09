// Package clictx resolves the CLI's request context once at startup: the
// deployment this invocation will actually reach, the credential it will actually
// send, the organization that credential belongs to, and whether that deployment
// is an enterprise instance. Every consumer — the context banner, the name a
// success or confirmation message gives that deployment, the `.env` pin written by
// login, the backend origin each command posts to, the credential each request
// carries, and the enterprise gating of publish's billing and prompts — reads this
// one resolution. None of them may recompute the precedence locally:
// two copies of "which deployment / which credential" is the defect this package
// exists to prevent.
//
// The context is the *effective request target*, not the active profile. They
// differ whenever an ambient backend URL, a redirected CDM endpoint, or an externally
// supplied API key displaces the profile, and anything derived from the profile alone
// would then describe a deployment and an organization the command is not going to
// use. The caller passes the flag- and build-supplied inputs in as Overrides;
// everything else is read from the canonical source — the profile store, and the CDM
// layer that owns the name and meaning of its own endpoint variable.
//
// Every tier of the target precedence lives here, the remote one included. A caller
// that resolved the last tier for itself would decide where a request goes *after*
// this package had already matched the profile, chosen the credential, settled the
// enterprise verdict and named the deployment in the banner — which is exactly how a
// stock Blocks Network profile came to send its key to an enterprise deployment while
// every line the operator read said Network.
//
// Resolution is split deliberately, and both halves matter:
//
//   - Resolve() does local-only work and makes NO network call, so it is safe
//     from every command's PersistentPreRun.
//   - EffectiveBackendURL(), Enterprise() and EffectiveCredential() complete lazily
//     and memoize. EffectiveBackendURL() because the last tier of the target
//     precedence is a deployment only remote config can name, and fetching it from
//     Resolve() would put a round-trip in front of every command, including the ones
//     that never reach a backend. Enterprise() because deciding the question for a
//     scripted --api-key run that never logged in needs a cli-config round-trip.
//     EffectiveCredential() because a key piped via --api-key-stdin can only be read
//     once — resolving it here, exactly once, is what lets the banner and the request
//     share a single read instead of the second consumer finding stdin already
//     drained.
//
// One input cannot be resolved from either half: which of several organizations
// on an enterprise deployment owns the agent, an answer only the user can give.
// SelectOrg folds it back in, so a command that has to ask still ends up with a
// single description of what it is about to do.
package clictx

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/cliconfig"
	"github.com/pubnub/blocks-sdk/cli/internal/origin"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// Source names the tier a resolved credential came from. Consumers use it to
// word errors ("the key you passed was rejected" vs "run blocks login") and to
// tell an externally supplied key from a stored one, so no command has to
// re-inspect the flags to find out.
type Source int

const (
	// SourceNone means no credential resolved: the invocation is anonymous.
	SourceNone Source = iota
	// SourceFlag is --api-key.
	SourceFlag
	// SourceStdin is --api-key-stdin.
	SourceStdin
	// SourceEnv is BLOCKS_API_KEY.
	SourceEnv
	// SourceProfile is the active profile's cached default-org key.
	SourceProfile
	// SourceStore is the legacy credential file kept for one migration cycle.
	SourceStore
)

// External reports whether the credential was supplied from outside the
// credential store for this one invocation (--api-key or --api-key-stdin).
func (s Source) External() bool { return s == SourceFlag || s == SourceStdin }

// Supplied reports whether the invocation itself named the credential —
// --api-key, --api-key-stdin or BLOCKS_API_KEY — rather than the CLI finding one
// in local storage. A named credential already fixes the organization the
// request acts as, so nothing the CLI resolves afterwards, least of all an
// interactive picker, may replace it: doing so would authorize the request as an
// organization the caller did not ask for and never handed a key for.
//
// It is deliberately wider than External(), which answers the narrower question
// of whether the key arrived on the command line at all.
func (s Source) Supplied() bool { return s.External() || s == SourceEnv }

// Overrides carries the inputs that can displace the active profile as this
// invocation's request target, plus the two side-effecting reads the resolver
// must own rather than perform itself. The caller supplies them because it owns
// flag parsing, environment lookup and the build-time backend default. A nil
// *Overrides means nothing displaces the profile.
type Overrides struct {
	// BackendURL is a backend origin that outranks the active profile's own
	// (BLOCKS_BACKEND_URL). Empty when nothing outranks the profile.
	BackendURL string
	// DefaultBackendURL is the build-time default, used only when neither
	// BackendURL nor the profile names a backend. It describes how a build finds
	// a target it was not told about, so it never counts as a deployment the user
	// picked.
	DefaultBackendURL string
	// FlagCredential is the API key passed as --api-key.
	FlagCredential string
	// EnvCredential is the API key found in BLOCKS_API_KEY.
	EnvCredential string
	// CredentialFromStdin records that --api-key-stdin was passed, so the key
	// must be read from stdin via ReadStdinCredential.
	CredentialFromStdin bool
	// ReadStdinCredential consumes the single line --api-key-stdin supplies. The
	// resolver calls it at most once per invocation. Nil means stdin cannot be
	// read, which is reported as a credential error rather than as no credential.
	ReadStdinCredential func() (string, error)
	// StoredCredential reads the legacy credential file — the last tier of the
	// precedence. It reports the key, whether a stored credential exists but has
	// expired, and whether the file could not be read at all. It decides nothing;
	// the ordering stays here. May be nil.
	StoredCredential func() (key string, expired bool, err error)
}

// Credential is the resolved credential for this invocation, together with
// enough detail for a command to word its own error. Exactly one of Key,
// Err/StoreErr, Expired, or "nothing at all" is meaningful.
type Credential struct {
	// Key is the API key the request will carry, or "" when none resolved.
	Key string
	// Source names the tier Key came from.
	Source Source
	// Err is set when the tier the user selected could not produce a key — an
	// unreadable or empty --api-key-stdin. It is fatal for the invocation.
	Err error
	// Expired is true when a stored credential exists but has expired.
	Expired bool
	// StoreErr is set when the stored credential file could not be read.
	StoreErr error
}

type state struct {
	o *Overrides
	// profileName is the active profile's name, or "" when none resolves. It
	// names where credentials are stored, not necessarily what is targeted.
	profileName string
	// profile is the active profile, or nil when none resolves.
	profile *profiles.Profile
	// backendURL is the target origin resolved from the local tiers, or "" when
	// only remote config can name it (stock Network on a source build).
	backendURL string
	// chosenBackendURL is the deployment the user pointed this invocation at, as far
	// as local facts go: the ambient backend URL, else the active profile's own. It is
	// "" when nothing local pointed anywhere, which is what distinguishes "target
	// Blocks Network however this build finds it" from "target this deployment".
	//
	// It deliberately excludes the remote-config tier, because the check that decides
	// whether a project-supplied CDM endpoint is honoured at all reads it, and that
	// check has to run before anything fetches that endpoint. chosenOrigin() is the
	// same question with the remote tier folded in, for consumers that may fetch.
	chosenBackendURL string
	// profileMatchesLocalTarget is true when the active profile describes the backend
	// the local tiers resolved. Read it through profileIsTarget(), which also accounts
	// for a target only remote config can name.
	profileMatchesLocalTarget bool
	enterprise                bool

	credOnce sync.Once
	cred     Credential
	entOnce  sync.Once
	// remoteOnce guards the one CDM fetch this invocation may make to learn the
	// backend origin no local tier named. remoteTarget/remoteErr hold its outcome.
	remoteOnce   sync.Once
	remoteTarget string
	remoteErr    error
	// targetErr holds a locally-resolved backend origin that failed validation.
	targetErr error
	// selected is the organization this invocation was steered to after Resolve()
	// ran, and the credential its requests must carry. Nil until something asks.
	selected *selection
}

// selection is an organization chosen for this invocation after the local
// resolution finished, together with the key that organization's requests carry.
type selection struct {
	orgName    string
	credential string
}

var cur = &state{}

// Resolve reads the active profile, folds in the overrides that displace it, and
// caches the resulting local facts. It never makes a network call and never reads
// stdin; both are deferred to EffectiveBackendURL(), Enterprise() and
// EffectiveCredential(). It runs from every command's PersistentPreRun, so a fetch
// here would be a round-trip for commands that never reach a backend at all.
func Resolve(o *Overrides) {
	if o == nil {
		o = &Overrides{}
	}
	s := &state{o: o}
	override := strings.TrimSpace(o.BackendURL)
	name, p, err := profiles.Active()

	switch {
	case override != "":
		s.chosenBackendURL = override
	case err == nil && p.BaseURL != "":
		s.chosenBackendURL = p.BaseURL
	}
	s.backendURL = s.chosenBackendURL
	source := "BLOCKS_BACKEND_URL"
	if override == "" && s.backendURL != "" {
		source = fmt.Sprintf("the %q profile", name)
	}
	if s.backendURL == "" {
		s.backendURL = strings.TrimSpace(o.DefaultBackendURL)
		source = "this build's default backend"
	}
	// Validated here, for whichever tier supplied it, rather than only where a user
	// types one. Credentials go to this origin, and the tiers nobody types reach the
	// same requests: an exported variable, a profile written before this check existed,
	// and the linker default. The error is held rather than returned because Resolve has
	// no error to return and no consumer of the local facts needs the target; the ones
	// that send a request go through EffectiveBackendURL, which surfaces it.
	if s.backendURL != "" {
		if err := origin.Validate(s.backendURL); err != nil {
			s.targetErr = fmt.Errorf("the backend URL from %s (%s) %w", source, s.backendURL, err)
		}
	}

	if err == nil {
		s.profileName = name
		s.profile = p
		s.profileMatchesLocalTarget = profileDescribesLocalTarget(p, s.backendURL)
	}
	cur = s
}

// profileDescribesLocalTarget reports whether p is the deployment the local tiers just
// resolved, which is what decides whether p's cached credential belongs to this request.
//
// It compares against the resolved target rather than asking "was there an override",
// which is the question this used to ask. Absent an override it answered yes
// unconditionally — and that is wrong precisely where it matters most: with no override
// and a stock Blocks Network profile, chosenBackendURL is empty, so the target falls
// through to this build's linker default. In a packaged Enterprise build that default is
// an Enterprise deployment, and the Network profile was then treated as describing it —
// so the resolver handed a Blocks Network key over as the Bearer token for Enterprise,
// silently, while the banner named Enterprise.
//
// Stock Blocks Network is an empty origin on both sides, and SameBaseURL is false for an
// empty value, so the empty cases are compared as a pair instead of being treated as a
// wildcard. Both empty is the one arrangement where a Network profile does describe the
// target: nothing local named a deployment, which is Blocks Network.
func profileDescribesLocalTarget(p *profiles.Profile, localTarget string) bool {
	if p == nil {
		return false
	}
	if p.BaseURL == "" || localTarget == "" {
		return p.BaseURL == "" && localTarget == ""
	}
	return profiles.SameBaseURL(p.BaseURL, localTarget)
}

// Reset clears the resolved context (tests).
func Reset() { cur = &state{} }

// Profile is the active profile name, or "" when none resolves. It names where
// credentials are stored, not necessarily what this invocation targets — use
// Banner for anything the user is asked to check.
func Profile() string { return cur.profileName }

// ProfileIsTarget reports whether the active profile describes the deployment
// this invocation will actually call. When it is false, nothing cached in the
// profile — its organization, its branding, its dashboard origin, its enterprise
// bit — describes the request.
func ProfileIsTarget() bool { return cur.profileIsTarget() }

// BackendURL is the backend origin resolved from the tiers that need no network
// round-trip: the ambient backend URL, then the active profile's own, then the
// build-time default. It returns "" when only remote config can name the target.
//
// It is the offline half of EffectiveBackendURL, for the one caller that must not
// stall on a fetch. Anything that sends a request wants EffectiveBackendURL.
func BackendURL() string { return cur.backendURL }

// EffectiveBackendURL is the origin this invocation's requests actually reach, and
// the last word on the target: the local tiers, then — when none of them named a
// deployment — the api.baseUrl carried by the CDM payload. The fetch happens at most
// once and is memoized, so Resolve() stays local while every consumer still gets the
// settled answer.
//
// The remote tier is part of *this* resolution rather than a fallback bolted on at the
// call site, and that is the whole point. It can name a different deployment from the
// active profile, so a caller that resolved it for itself would send a request to a
// deployment whose enterprise verdict was never asked, whose credential was never
// selected, and which the context banner never named. The error is returned rather
// than reported here: it belongs to whichever command could not resolve a backend, and
// only that command knows whether the failure is fatal for what it was about to do.
func EffectiveBackendURL() (string, error) {
	s := cur
	if s.targetErr != nil {
		return "", s.targetErr
	}
	if s.backendURL != "" {
		return s.backendURL, nil
	}
	return s.remoteBackendURL()
}

// remoteBackendURL fetches the CDM payload once and reports the backend origin it
// names. Both the value and the failure are memoized: the target may not change
// between two consumers of it, and a deployment that could not be resolved must not
// be re-attempted per call site.
func (s *state) remoteBackendURL() (string, error) {
	s.remoteOnce.Do(func() {
		cfg, err := cdm.Get()
		switch {
		case err != nil:
			s.remoteErr = err
		case cfg != nil:
			target := strings.TrimSpace(cfg.Api.BaseURL)
			// The payload is remote input and this is the one tier where the origin was
			// never seen by a human, so it is held to the same rule as a typed argument.
			if target != "" {
				if verr := origin.Validate(target); verr != nil {
					s.remoteErr = fmt.Errorf("the backend URL the remote config named (%s) %w", target, verr)
					return
				}
			}
			s.remoteTarget = target
		}
	})
	return s.remoteTarget, s.remoteErr
}

// redirectedCDMEndpoint reports the CDM endpoint the environment pointed this
// invocation at, or "" when it named none and the build's own default answers.
//
// The distinction matters because the payload carries api.baseUrl. Left alone, that
// value is how a stock Blocks Network profile — which records no deployment of its
// own — finds its backend, and the profile is still the target. Pointed elsewhere, it
// names a deployment the caller chose, exactly as an ambient backend URL does, and the
// profile no longer describes the request.
//
// It reads the variable live rather than latching it during Resolve() because the
// value is not final at that point: the caller drops a CDM endpoint that only a
// project file supplied, and an explicitly targeted login drops one that answers for
// another deployment, both after the resolution has run. Latching would treat a
// withdrawn endpoint as though it still decided the target.
func redirectedCDMEndpoint() string {
	pinned := trimOrigin(os.Getenv(cdm.URLEnv))
	if pinned == "" || pinned == trimOrigin(cdm.DefaultCDMURL) {
		return ""
	}
	return pinned
}

func trimOrigin(raw string) string { return strings.TrimRight(strings.TrimSpace(raw), "/") }

// remoteChosenOrigin is the deployment a redirected CDM endpoint names, and "" when
// the environment redirected nothing. It is the tier that turns "how this build finds
// Blocks Network" into "the deployment this invocation was pointed at".
func (s *state) remoteChosenOrigin() string {
	if redirectedCDMEndpoint() == "" {
		return ""
	}
	url, _ := s.remoteBackendURL()
	return url
}

// chosenOrigin is the deployment the user pointed this invocation at, with the
// remote-config tier folded in: the ambient backend URL, else the active profile's
// own, else the deployment a redirected CDM endpoint names. It is "" when the user
// pointed at nothing in particular.
//
// The build-time default is deliberately not a tier here, as it is not in
// ChosenBackendURL: it describes how a build finds a target it was not told about, so
// a build carrying one has still been pointed nowhere, and the enterprise question is
// still not worth a round-trip.
func (s *state) chosenOrigin() string {
	switch {
	case s.chosenBackendURL != "":
		return s.chosenBackendURL
	case s.backendURL != "":
		// Only the build-time default named an origin, so nothing remote is consulted
		// and nothing was chosen.
		return ""
	}
	return s.remoteChosenOrigin()
}

// targetOrigin is the origin the target is *named* by, which is the effective backend
// minus the one case where it has a better name than a host: stock Blocks Network,
// where no local tier and no redirected CDM endpoint named a deployment and the label
// is the profile's or the product's name instead.
func (s *state) targetOrigin() string {
	if s.backendURL != "" {
		return s.backendURL
	}
	return s.remoteChosenOrigin()
}

// profileIsTarget reports whether the active profile describes the deployment this
// invocation will call. Beyond the local comparison Resolve() made, a CDM endpoint the
// environment redirected names a deployment of its own, and a profile that records no
// deployment — the stock Blocks Network shape — does not describe that one.
func (s *state) profileIsTarget() bool {
	if s.profile == nil {
		return false
	}
	return s.profileMatchesLocalTarget && !s.cdmNamesTheTarget()
}

// cdmNamesTheTarget reports whether the deployment this invocation reaches is one only
// a redirected CDM endpoint named. It is a local question — the presence of the
// redirect, not its payload — so the credential tiers can withhold a stored key
// without any consumer forcing a fetch to find out.
func (s *state) cdmNamesTheTarget() bool {
	return s.backendURL == "" && redirectedCDMEndpoint() != ""
}

// ChosenBackendURL is the deployment the user pointed this invocation at as far as
// local facts go — the ambient backend URL, else the active profile's own — and "" when
// nothing local pointed anywhere. Callers that must distinguish "the user picked this
// deployment" from "this build resolves Blocks Network like so" need exactly this
// split: it decides whether a backend URL belongs in a project `.env` beside a freshly
// minted key.
//
// It stops short of the remote-config tier on purpose, and one caller depends on that:
// the check that decides whether a project-supplied CDM endpoint is honoured at all
// reads this, and it has to reach its verdict before anything fetches that endpoint.
// Consumers that may fetch — the banner, the credential, the enterprise verdict — go
// through the internal chosenOrigin() instead, which is the same question with the
// remote tier folded in.
func ChosenBackendURL() string { return cur.chosenBackendURL }

// DeploymentSettled reports whether this invocation already knows which deployment it
// targets, and so must not ask again.
//
// Two independent facts answer yes, and both are needed. A local tier may have named an
// origin — an ambient backend URL, or the active profile's own. Or the active profile may
// record a completed login while naming no origin at all: that is how a finished stock
// Blocks Network login is stored, since empty BaseURL is how "resolve via CDM" is
// recorded. An empty origin is then a settled choice rather than the absence of one.
//
// That second fact is the whole reason this predicate exists. ChosenBackendURL() == "" is
// true both for "nobody has chosen a deployment" and for "Blocks Network was chosen and
// stored", and a caller that reads it alone conflates them — re-interrogating a user who
// already answered, and failing under --no-input unless --network is repeated on every
// invocation. Keeping both facts joined here means a second caller cannot reintroduce
// that conflation by asking the narrower question.
//
// The profile must be asked for a recorded login, not merely for its existence: an empty
// stock-Network profile is materialized on first run before anyone has logged in, so
// profile != nil would suppress the question for the genuine first run it exists to
// serve. RecordsCompletedLogin() draws that line.
func DeploymentSettled() bool {
	return cur.chosenBackendURL != "" || (cur.profile != nil && cur.profile.RecordsCompletedLogin())
}

// EffectiveCredential resolves the API key this invocation will send, once, and memoizes
// it. Precedence: --api-key, --api-key-stdin, BLOCKS_API_KEY, the active
// profile's cached default-org key, then the legacy credential store.
//
// The last two tiers read local storage, so they apply only while the active
// profile still describes the deployment being called. Once an ambient backend URL or a
// redirected CDM endpoint has displaced it and nothing above supplied a key, a stored
// key found there is withheld — sending it would carry one deployment's credential to
// another — and the result is an error naming the deployment a key is needed for,
// because the caller is logged in, just not to that deployment.
//
// Every consumer must read the credential from here. Beyond keeping one
// precedence, it is what makes --api-key-stdin correct: stdin is consumable, so
// a second independent resolution would find it drained and silently produce an
// empty key.
func EffectiveCredential() Credential {
	s := cur
	if s.selected != nil {
		// A selected organization supersedes the precedence: its key came out of the
		// profile store (cached there, or minted and cached on the spot), so it is a
		// stored credential like any other, and a rejection is answered by logging in
		// again rather than by fixing a flag.
		return Credential{Key: s.selected.credential, Source: SourceProfile}
	}
	s.credOnce.Do(func() { s.cred = s.resolveCredential() })
	return s.cred
}

// SelectOrg records the organization this invocation was steered to after the
// local resolution finished, together with the key its requests must carry from
// here on. Every consumer then reads one answer: the banner names the
// organization the request is authorized as, and the request carries the key that
// banner described.
//
// It exists because the organization is the one part of the request target that
// no local fact can settle. On an enterprise deployment holding several
// organizations, only the user can say which one owns the agent, and that answer
// necessarily arrives after Resolve() — which may neither prompt nor call out —
// has already picked a credential. Recording the answer here, rather than
// swapping a key inside the command, is what stops the two from disagreeing.
//
// Callers must not offer the choice at all when EffectiveCredential().Source
// reports Supplied(): a credential the invocation named already fixes the
// organization. credential must be non-empty — it is the key the request sends.
func SelectOrg(orgName, credential string) {
	cur.selected = &selection{orgName: orgName, credential: credential}
}

func (s *state) resolveCredential() Credential {
	o := s.o
	if o == nil {
		o = &Overrides{}
	}
	if key := strings.TrimSpace(o.FlagCredential); key != "" {
		return Credential{Key: key, Source: SourceFlag}
	}
	if o.CredentialFromStdin {
		if o.ReadStdinCredential == nil {
			return Credential{Source: SourceStdin, Err: fmt.Errorf("--api-key-stdin: stdin is not readable")}
		}
		key, err := o.ReadStdinCredential()
		if err != nil {
			return Credential{Source: SourceStdin, Err: err}
		}
		return Credential{Key: key, Source: SourceStdin}
	}
	if key := strings.TrimSpace(o.EnvCredential); key != "" {
		return Credential{Key: key, Source: SourceEnv}
	}
	// Both remaining tiers read a key out of local storage, and neither records the
	// deployment it belongs to beyond the profile that holds it. Once an ambient
	// backend URL has displaced that profile, handing either one over would send one
	// deployment's credential to another — the same reason Enterprise() refuses the
	// displaced profile's recorded verdict. So a key found in either tier is
	// withheld, and because nothing above outranked the profile, the invocation has
	// no credential for the target and says which deployment needs one.
	displaced := s.profileDisplaced()
	if s.profile != nil {
		if k, ok := s.profile.DefaultOrgKey(); ok && k.ApiKey != "" && !k.IsExpired() {
			if displaced {
				return Credential{Err: s.displacedCredentialError()}
			}
			return Credential{Key: k.ApiKey, Source: SourceProfile}
		}
	}
	if o.StoredCredential != nil {
		key, expired, err := o.StoredCredential()
		switch {
		case err != nil:
			return Credential{StoreErr: err}
		case expired:
			return Credential{Expired: true}
		case strings.TrimSpace(key) != "":
			if displaced {
				return Credential{Err: s.displacedCredentialError()}
			}
			return Credential{Key: key, Source: SourceStore}
		}
	}
	// No tier held a key at all, displaced or not: the caller is simply not
	// authenticated, and saying anything about a displaced profile here would
	// describe a credential that does not exist.
	return Credential{}
}

// profileDisplaced reports whether an active profile exists but describes a
// deployment other than the one this invocation will call. It is the credential
// tiers' form of the check the enterprise verdict already makes: a displaced
// profile's organization, branding, enterprise bit and cached key all belong to a
// deployment the request will not reach.
func (s *state) profileDisplaced() bool { return s.profile != nil && !s.profileIsTarget() }

// displacedCredentialError says why a logged-in user has no credential for this
// target, and how to supply one. It is returned only where a locally stored key
// existed and was withheld, so it never fires at a caller who is simply not
// authenticated.
//
// It asserts only what is locally verifiable: an active profile exists, it does
// not describe the backend being called, and that backend is named by host —
// following the same rule as Banner and TargetName — because a host is all that is
// known about a deployment no local record describes. Both named remedies outrank the
// profile in the precedence, so either one clears the error.
//
// The host can be missing in exactly one shape: the target is the deployment a
// redirected CDM endpoint names and that endpoint could not be fetched. The key is
// still withheld, because the redirect is enough to know the profile does not describe
// the target, but the deployment has no name to give — so the message names the
// variable that redirected instead, which is the thing the reader has to look at
// either way.
func (s *state) displacedCredentialError() error {
	host := s.host()
	if host == "" {
		return fmt.Errorf("no credential for the deployment %s names — the active profile %q is for a different deployment and cannot supply one; set BLOCKS_API_KEY or pass --api-key with a key for it",
			cdm.URLEnv, termsafe.Text(s.profileName))
	}
	return fmt.Errorf("no credential for %s — the active profile %q is for a different deployment and cannot supply one; set BLOCKS_API_KEY or pass --api-key with a key for %s",
		host, termsafe.Text(s.profileName), host)
}

// Org is the display name of the organization owning the credential this
// invocation will send, or "" when no local fact establishes it. An organization
// recorded by SelectOrg answers first, since it and the key stored with it are
// what the request will use. Otherwise the active profile's organization names the
// caller only when the profile supplies both halves of the request — the backend
// being called and the key being sent — so a key from elsewhere is never
// attributed to it.
func Org() string {
	s := cur
	if s.selected != nil {
		return s.selected.orgName
	}
	if !s.profileIsTarget() {
		return ""
	}
	c := EffectiveCredential()
	if c.Key == "" {
		return ""
	}
	if c.Source == SourceProfile {
		if k, ok := s.profile.DefaultOrgKey(); ok {
			return k.OrgName
		}
		return ""
	}
	// An externally supplied key names an organization only when the profile has
	// that exact key cached — the `--write-env` shape, where .env carries the key
	// login already stored there. A key the store has never seen belongs to an
	// organization only the backend can name.
	return orgOfCachedKey(s.profile, c.Key)
}

// OrgKnown reports whether the organization behind this invocation's credential
// is locally knowable. It is false for a key no local record accounts for — one
// the profile store has never cached, or one belonging to another deployment.
//
// A key the invocation supplied is not automatically unknowable, which the wording
// here used to claim: when it matches a key the target profile already holds, byte
// for byte, the store itself names the organization, and that is evidence rather
// than a guess. It is the ordinary `login --write-env` shape, where the `.env`
// carries the key login stored there.
func OrgKnown() bool { return Org() != "" }

func orgOfCachedKey(p *profiles.Profile, credential string) string {
	for _, k := range p.Orgs {
		if k.ApiKey == credential {
			return k.OrgName
		}
	}
	return ""
}

// Enterprise reports whether this invocation targets an enterprise deployment.
//
// The active profile's recorded answer is authoritative while that profile is
// still the target — a profile saved by a prior `blocks login <instanceUrl>` records
// whether that deployment is an enterprise instance, and no round-trip can improve on
// it. When an override displaces it, or when no enterprise profile is present but the
// user pointed at a specific deployment (a scripted --api-key publish that never ran
// login), it confirms via a lenient cli-config discovery against the deployment
// actually being called. A target the user did not pick cannot be an enterprise
// instance, so that case skips the round-trip entirely. Discovery errors and older
// backends (404) yield false and never block the caller. The result is memoized for the
// process.
//
// The deployment discovery runs against is chosenOrigin(), which includes the target a
// redirected CDM endpoint names: that endpoint can point at an enterprise deployment,
// and reading the question off the local tiers alone answered "not enterprise" for a
// request that was about to reach one.
//
// The verdict gates request behaviour, not just wording: it forces free billing
// on publish and suppresses the marketplace prompts. That is why it may not
// trust profile metadata an override has displaced.
func Enterprise() bool {
	s := cur
	if s.profile != nil && s.profile.Enterprise && s.profileIsTarget() {
		return true
	}
	s.entOnce.Do(func() {
		origin := s.chosenOrigin()
		if origin == "" {
			return // no deployment was picked, so it cannot be an enterprise one
		}
		disco, err := cliconfig.Fetch(origin)
		s.enterprise = err == nil && disco != nil && disco.Enterprise
	})
	return s.enterprise
}

// BannerOption declares what the banner's second half must describe. A command
// whose organization does not scope its operation passes ActsOn or
// OrgIsNotTheScope; every other command passes nothing and gets the organization.
type BannerOption func(*bannerScope)

// bannerScope is what a banner will say the operation is about to affect.
type bannerScope struct {
	// orgScopes is true while the organization the credential belongs to is also the
	// scope of the operation. It is the default because it is the create case:
	// publish and register bring an agent into existence under one organization.
	orgScopes bool
	// subject names what authorization actually turns on when the organization does
	// not. Empty means the command states its own subject in a line the operator
	// reads anyway, so the banner names the deployment alone rather than repeating it.
	subject string
}

// ActsOn declares that this invocation's organization does not scope the operation
// the deployment is about to perform, and names the subject that decides it
// instead — the agent being changed, the grantee being invited or revoked. The
// subject is passed raw; the banner escapes it.
func ActsOn(subject string) BannerOption {
	return func(s *bannerScope) { s.orgScopes = false; s.subject = subject }
}

// OrgIsNotTheScope declares the same as ActsOn with no subject to add: the
// organization does not scope the operation, and the command already names its
// subject in a line of its own — unregister's confirmation prompt, which asks about
// the agent by name one line later. The banner then states the deployment only.
func OrgIsNotTheScope() BannerOption { return ActsOn("") }

// Banner renders the context line shown before state-mutating commands. It names
// the deployment the invocation will actually reach, then what the operation is
// about to affect there.
//
// That second half is the command's to declare, because only the command knows what
// its organization means for the request it makes. Where the organization scopes the
// operation — publish and register, which create an agent under one — naming it
// states what will be affected. Where it does not, naming it would be a true
// statement about the credential that reads as a false one about the blast radius:
// unregister and invite are authorized by the caller's rights over a named agent or
// invitation, so a multi-organization user can delete another organization's agent,
// or share and revoke another organization's invitation, with this organization's
// key. A banner reassuringly naming this one would then invite exactly the "I
// thought I was operating on X" inference the banner exists to prevent, which is why
// those commands name their subject instead of an organization that decides nothing.
//
// It returns "" when no credential will be sent, so an anonymous command never
// prints a half-empty banner, and when the deployment cannot be named at all. In the
// organization-scoped shape a credential whose organization is not locally
// verifiable — a key no profile has cached, or one belonging to another
// deployment — is reported as unknown rather than borrowed from the active profile,
// which would name an org the command is not acting as.
//
// Every half is rendered through termsafe: the organization name comes from the
// backend, the subject from an argument or an agent card, and this line is what an
// operator reads to check a destructive command's target, so text carrying an
// erase-line or cursor-up sequence could scroll the banner away or overwrite it with
// a different deployment's.
func Banner(opts ...BannerOption) string {
	s := cur
	deployment := s.deployment()
	if deployment == "" || EffectiveCredential().Key == "" {
		return ""
	}
	scope := bannerScope{orgScopes: true}
	for _, opt := range opts {
		opt(&scope)
	}
	if !scope.orgScopes {
		if scope.subject == "" {
			return "[" + deployment + "]"
		}
		return "[" + deployment + " / " + termsafe.Text(scope.subject) + "]"
	}
	org := Org()
	if org == "" {
		return "[" + deployment + " / organization unknown]"
	}
	return "[" + deployment + " / " + termsafe.Text(org) + "]"
}

// deployment labels the target: the active profile's name when that profile
// describes the backend being called, otherwise the backend's host so the label
// can never imply a profile the request is not using.
func (s *state) deployment() string {
	if s.profileIsTarget() && s.profileName != "" {
		return termsafe.Text(s.profileName)
	}
	return s.host()
}

// host is the target origin's host, falling back to the raw origin when it cannot be
// parsed into one. It is sanitized because it is only ever a label, never a request
// target, and the origin it derives from can come from a project `.env` or from a CDM
// payload.
func (s *state) host() string {
	origin := s.targetOrigin()
	if u, err := url.Parse(origin); err == nil && u.Host != "" {
		return termsafe.Text(u.Host)
	}
	return termsafe.Text(strings.TrimSpace(origin))
}

// TargetName names the deployment this invocation acts on, for the sentences a
// user reads: "removed from X", "published to X", "creating a project for X". It
// follows the same rule as Banner — never assert what cannot be verified.
//
// The locally cached product name describes the deployment only while the active
// profile is the target. Once an ambient backend URL displaces it, the target's
// product name is knowable only by asking the deployment, and these messages must
// not add a round-trip to find out, so the deployment is named by host instead.
// Naming a host is a weaker sentence than naming a brand, and deliberately so: it
// is the difference between naming a product the request never reached and saying
// where the agent was actually removed from.
//
// With no profile and nothing pointing anywhere the target is the public
// deployment, whose name is the stock one regardless of any brand Set() recorded.
func TargetName() string {
	s := cur
	switch {
	case s.profileIsTarget():
		// The product name is whatever the deployment called itself during discovery,
		// so it is sanitized for the same reason the organization name is: these
		// sentences include the confirmation prompt of a destructive command.
		return termsafe.Text(branding.ProductName())
	case s.chosenOrigin() == "":
		return branding.Default()
	}
	if h := s.host(); h != "" {
		return h
	}
	return branding.Default()
}

// PrintBanner writes the context banner when one is resolvable. Mutating
// commands call it immediately before acting, passing the options that say what
// their organization means for the operation (see Banner). It is intentionally not
// gated on Enterprise(): the dangerous state is believing you are on an enterprise
// deployment while actually on Network, and an enterprise-gated banner would be
// silent in exactly that case.
func PrintBanner(opts ...BannerOption) {
	if b := Banner(opts...); b != "" {
		fmt.Println(b)
	}
}
