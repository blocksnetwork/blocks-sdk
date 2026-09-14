package cmd

// This file is the single home for the CLI's deployment-aware error wording.
// Every helper answers the same question — "which deployment is this
// invocation actually talking to" — through clictx, and returns one of two
// wordings: the stock Blocks Network text (byte-identical to what the CLI has
// always printed, so Network behavior cannot drift) or the Enterprise text,
// which names the deployment, drops internal jargon ("CDM", "remote config",
// "Blocks portal") and states the next step.
//
// The branch lives here rather than at the call sites so the two wordings
// cannot drift apart and no command re-derives the enterprise verdict. Call
// sites stay thin: they call the helper where they used to write a literal.
//
// Two substitution sources, both already resolved once per invocation:
//
//   - enterpriseLoginTarget() — the active profile's name and base URL, used
//     only while that profile describes the deployment being called. A user
//     who has never logged in to the deployment they are targeting (a
//     scripted BLOCKS_BACKEND_URL run, a fresh machine) has no such profile,
//     and those users get the '<your-instance-url>' variants, which is the
//     correct advice for them.
//   - clictx.DeploymentLabel() — the banner's rule for naming the deployment
//     being called: profile name while the profile is the target, else host.
//     An error and the banner can then never name different deployments.
//
// Every interpolated value is rendered through termsafe: profile and org names
// arrive from local files written by remote payloads, and these lines are what
// a user reads to decide what to run next.
//
// And every message follows the package's paste-safety shape: a line that reads
// as something to run carries no interpolated value (see
// command_shaped_output_test.go). The Enterprise wordings below state the
// value on one line and the remedy command on the next — several of them are
// two lines for that reason alone.

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// enterpriseLoginTarget reports the active profile's name and base URL when
// that profile describes the deployment being called and records an Enterprise
// instance. Zero values mean "no usable profile" — the caller is enterprise
// but has never logged in to the deployment they are targeting, and the
// '<your-instance-url>' wording is the advice that fits.
//
// The profile must both be the target and carry a deployment URL: the stock
// blocks-network profile is materialized before anyone logs in and describes
// no Enterprise deployment, and a profile an ambient URL has displaced
// describes a deployment this invocation is not calling.
func enterpriseLoginTarget() (name, baseURL string) {
	if !clictx.ProfileIsTarget() {
		return "", ""
	}
	_, p, err := profiles.Active()
	if err != nil || !p.Enterprise || p.BaseURL == "" {
		return "", ""
	}
	return clictx.Profile(), p.BaseURL
}

// deploymentLabel names the deployment being called for error wording, or ""
// when nothing resolvable names it. Enterprise wordings that need a label fall
// back to their stock text when this is empty, rather than printing a sentence
// with a hole in it.
func deploymentLabel() string {
	return clictx.DeploymentLabel()
}

// originHost reduces a backend origin to the host an error message shows. It
// answers for origins that are not the invocation's own target — a card lookup
// pointed at a specific backend — which clictx's label rule cannot see.
func originHost(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSpace(raw)
}

// backendNotConfigured replaces every "BLOCKS_BACKEND_URL must be set" shape.
// network is the site's current stock text — the sites predate this helper
// with three different phrasings, and the stock wording of each must not
// change.
func backendNotConfigured(network string) error {
	if clictx.Enterprise() {
		return fmt.Errorf("Backend URL not configured. Run 'blocks login <your-instance-url>' first, or set BLOCKS_BACKEND_URL in your environment.")
	}
	return errors.New(network)
}

// notLoggedInError is the shared "not logged in" failure used by whoami and
// every command that loads credentials.
func notLoggedInError() error {
	if clictx.Enterprise() {
		if name, baseURL := enterpriseLoginTarget(); name != "" {
			return fmt.Errorf("Not logged in — the active profile is %s (%s).\n  Run 'blocks login' to re-authenticate.",
				termsafe.Text(name), termsafe.Text(baseURL))
		}
		return fmt.Errorf("Not logged in — run 'blocks login <your-instance-url>' to authenticate")
	}
	return fmt.Errorf("not logged in — run 'blocks login' first")
}

// notAuthenticatedError is publish/register's pre-flight credential failure.
func notAuthenticatedError() error {
	if clictx.Enterprise() {
		if name, _ := enterpriseLoginTarget(); name != "" {
			return fmt.Errorf("Not authenticated — the active profile is %s.\n  Run 'blocks login' to re-authenticate, or provide --api-key.",
				termsafe.Text(name))
		}
		return fmt.Errorf("Not authenticated — run 'blocks login <your-instance-url>' first, or provide --api-key. You can generate an API key from the dashboard.")
	}
	return fmt.Errorf("not authenticated — run 'blocks login' first, or provide --api-key")
}

// credentialsExpiredError is publish/register's expired-credential failure.
// An expired credential is by definition one the CLI stored, but the store it
// came from may be the legacy file rather than a profile, so the deployment is
// named by label and the stock wording answers when nothing can name it.
func credentialsExpiredError() error {
	if label := deploymentLabel(); clictx.Enterprise() && label != "" {
		return fmt.Errorf("Credentials expired for %s.\n  Run 'blocks login' to re-authenticate, or provide --api-key.", label)
	}
	return fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate, or provide --api-key")
}

// apiKeyExpiredError is the shared stored-key expiry failure.
func apiKeyExpiredError() error {
	if label := deploymentLabel(); clictx.Enterprise() && label != "" {
		return fmt.Errorf("API key for %s has expired.\n  Run 'blocks login' to create a new one.", label)
	}
	return fmt.Errorf("API key has expired — run 'blocks login' to create a new one")
}

// cardLookupExpiredCredentialError is the webapp scaffold's expired-credential
// refusal: an expired login answers with where to renew rather than falling
// back to an anonymous card lookup that would report the user's own private
// agents as missing. Its stock arm preserves the wording that site has always
// printed, which differs from the publish family's ("or provide --api-key").
func cardLookupExpiredCredentialError() error {
	if label := deploymentLabel(); clictx.Enterprise() && label != "" {
		return fmt.Errorf("Credentials expired for %s.\n  Run 'blocks login' to re-authenticate.", label)
	}
	return fmt.Errorf("credentials expired — run 'blocks login' to re-authenticate")
}

// authFailedStoredCredentialError maps a 401 on stored credentials.
// commandName is the CLI verb the user ran, kept in the stock wording only —
// the Enterprise wording names the deployment instead.
func authFailedStoredCredentialError(commandName string) error {
	if label := deploymentLabel(); clictx.Enterprise() && label != "" {
		return fmt.Errorf("Authentication failed on %s.\n  Run 'blocks login' to re-authenticate, then retry.", label)
	}
	return fmt.Errorf("authentication failed — run 'blocks login' to re-authenticate, then retry '%s'", commandName)
}

// permissionDenied403Error maps a registry 403. The organization is named when
// the invocation's credential establishes it; a key no local record accounts
// for belongs to an organization only the backend can name, and the message
// says "your organization" rather than guessing.
//
// The "run 'blocks whoami' to see your permissions" sentence is deliberate
// wording, not an oversight, even though whoami today prints profile/org/key
// rather than a permission list; correct it when whoami grows a permissions
// view.
func permissionDenied403Error() error {
	if clictx.Enterprise() {
		if org := clictx.Org(); org != "" {
			return fmt.Errorf("Permission denied — your account does not have 'agent:manage' permission in %s.\n  Contact your org admin, or run 'blocks whoami' to see your permissions.",
				termsafe.Text(org))
		}
		return fmt.Errorf("Permission denied — your account does not have 'agent:manage' permission for this organization.\n  Contact your org admin, or run 'blocks whoami' to see your permissions.")
	}
	return fmt.Errorf("permission denied (HTTP 403) — check that your API key owns this agent")
}

// validationBeforeCommandError is the agent-card validation summary.
// commandName decides the verb so `blocks register` no longer tells the user
// to fix errors "before publishing".
func validationBeforeCommandError(cardPath, commandName string) error {
	if clictx.Enterprise() {
		verb := "publishing"
		if commandName == "blocks register" {
			verb = "registering"
		}
		return fmt.Errorf("Fix validation errors in %s before %s.\n  Run 'blocks check' for details.", cardPath, verb)
	}
	return fmt.Errorf("fix validation errors in %s before publishing", cardPath)
}

// existingPaidAgentMessage explains a BillingModeInvalid rejection.
// dashboardURL is the deployment's dashboard origin when one is locally
// resolvable; empty omits the parenthetical rather than printing a URL this
// CLI cannot stand behind.
func existingPaidAgentMessage(agentName, dashboardURL string) string {
	agentLabel := "This agent"
	if strings.TrimSpace(agentName) != "" {
		agentLabel = fmt.Sprintf("Agent %s", agentName)
	}
	if clictx.Enterprise() {
		if dashboardURL != "" {
			return fmt.Sprintf("%s is already configured as a Paid agent. Delete it via the dashboard (%s) before re-registering.",
				agentLabel, termsafe.Text(dashboardURL))
		}
		return fmt.Sprintf("%s is already configured as a Paid agent. Delete it via the dashboard before re-registering.", agentLabel)
	}
	return fmt.Sprintf("%s is already configured as a Paid agent. Please delete via the Blocks portal before publishing it as a Free agent.", agentLabel)
}

// agentCardNotFoundError is `blocks run`'s missing-card failure. cardPath is
// the stock wording's subject; the Enterprise wording names the file the way a
// user meets it.
func agentCardNotFoundError(cardPath string) error {
	if clictx.Enterprise() {
		return fmt.Errorf("agent-card.json not found. Run 'blocks init' to create a new project, or see https://blocks.ai/docs/connect-your-agent for creating an agent-card manually.")
	}
	return fmt.Errorf("could not read %s\nCreate an agent-card.json or run: blocks init <name>", cardPath)
}

// pythonVenvNotFoundError is `blocks run`'s no-venv failure for Python agents.
// The stock text assumes this repository's own Makefile, which an Enterprise
// customer's project does not have.
func pythonVenvNotFoundError() error {
	if clictx.Enterprise() {
		return fmt.Errorf("No Python virtual environment found. Run 'pip install -e .' in a venv, or check that .venv exists in your project directory.")
	}
	return fmt.Errorf("no Python venv found. Run 'make setup' or activate a virtualenv with the Blocks SDK")
}

// inviteRequestFailedError maps a non-2xx invite response. detail is the
// backend's own error text when the body carried one.
func inviteRequestFailedError(statusCode int, detail string) error {
	label := deploymentLabel()
	if detail != "" {
		if clictx.Enterprise() && label != "" {
			return fmt.Errorf("Request to %s failed (HTTP %d): %s", label, statusCode, detail)
		}
		return fmt.Errorf("request failed (HTTP %d): %s", statusCode, detail)
	}
	if clictx.Enterprise() && label != "" {
		return fmt.Errorf("Request to %s failed (HTTP %d): no details returned", label, statusCode)
	}
	return fmt.Errorf("request failed: HTTP %d", statusCode)
}

// dashboardURLUnresolvableError is `blocks dashboard`'s resolution failure.
func dashboardURLUnresolvableError() error {
	if clictx.Enterprise() {
		return fmt.Errorf("Could not resolve dashboard URL. Run 'blocks login <your-instance-url>' first to configure your deployment.")
	}
	return fmt.Errorf("could not resolve dashboard URL - set %s or %s, or ensure CDM config is reachable",
		blocksAppBaseURLEnv, blocksDashboardURLEnv)
}

// remoteConfigWarning is the non-fatal warning printed when the deployment
// configuration endpoint cannot be fetched. err is retained in the Enterprise
// wording too: the fetch failure's cause is what an operator has to fix,
// whatever the phrasing around it.
func remoteConfigWarning(err error) string {
	if clictx.Enterprise() {
		return fmt.Sprintf("Warning: could not reach deployment configuration. Commands may fall back to defaults. (%v)", err)
	}
	return fmt.Sprintf("Warning: failed to fetch remote config: %v", err)
}

// loginOrgUnresolvableError is `blocks login --api-key`'s failure to map a key
// to an organization. target names the deployment the login was pointed at, or
// "" when even that is unknown; enterprise is the verdict that login's own
// discovery just recorded for that target, so the failure names the deployment
// the way an Enterprise user meets it.
func loginOrgUnresolvableError(target string, enterprise bool) error {
	label := termsafe.Text(target)
	if label == "" {
		label = "the target instance"
	}
	if enterprise {
		return fmt.Errorf("Could not determine the organization for this API key. Verify the key is valid for %s and try again.", label)
	}
	return fmt.Errorf("could not determine the organization for this API key from %s — the key was not stored; verify the key is valid and the instance URL is correct, then retry", label)
}

// logoutIncompleteError is `blocks logout`'s partial-failure error. The
// cleanup suggestion names the profile only when that profile can actually be
// removed — the default blocks-network profile cannot, and suggesting it would
// send the user to a command that refuses. The cause is wrapped in both arms
// so error introspection behaves the same whichever wording printed.
func logoutIncompleteError(profileName string, err error) error {
	if clictx.Enterprise() && profileName != "" && profileName != profiles.DefaultProfile {
		return fmt.Errorf("Logout incomplete — credentials for %s could not be fully removed (%w).\n  Try 'blocks profile remove <name>' to clean up.",
			termsafe.Text(profileName), err)
	}
	return fmt.Errorf("logout incomplete — Blocks credentials remain on disk: %w", err)
}

// agentNotFoundOnDeploymentError is the webapp scaffold's agent-not-found
// failure. deployment names where the card lookup went. The name stays quoted
// and off any line carrying a runnable command: it came from the registry's
// own suggestion list, and the stock wording keeps that separation for the
// same reason.
func agentNotFoundOnDeploymentError(name, deployment string) error {
	if clictx.Enterprise() && deployment != "" {
		return fmt.Errorf("Agent %q not found on %s. Check spelling, or verify the agent is registered on this deployment.",
			name, termsafe.Text(deployment))
	}
	return fmt.Errorf("agent %q not found — check the spelling.\n  If it is a private agent your account can access, log in first: run 'blocks login'.", name)
}

// privateOrgGroup is one organization's private agents in a webapp agent
// list, as the scaffold's multi-org check groups them.
type privateOrgGroup struct {
	orgID   string
	orgName string
	agents  []string
}

// webappMultiOrgPrivateAgentsError is the webapp scaffold's refusal for a
// page wiring private agents from more than one organization: the sign-in
// popup binds a session to a single organization and rejects such a mix, so
// the page would fail for every visitor at the sign-in step — reported here
// instead, before anything is written.
//
// The paste-safety shape of the not-found wording applies: agent and org
// names are registry-sourced, so they stay on their own lines and off the
// line that words the remedy. Org names are display-only (not a documented
// contract field), rendered through termsafe like every interpolated value.
func webappMultiOrgPrivateAgentsError(groups []privateOrgGroup) error {
	var lines []string
	for _, g := range groups {
		label := g.orgName
		if label == "" {
			label = g.orgID
		}
		lines = append(lines, fmt.Sprintf("  %s — %s", strings.Join(g.agents, ", "), termsafe.Text(label)))
	}
	return fmt.Errorf("cannot scaffold: private agents from more than one organization cannot share one page —\n"+
		"a sign-in session is bound to a single organization, so every visitor's sign-in would be rejected.\n"+
		"%s\n"+
		"  Keep private agents from one organization per page, or make the others public:\n"+
		"  run 'blocks publish --listing public' for the agents that should be reachable by anyone.",
		strings.Join(lines, "\n"))
}
