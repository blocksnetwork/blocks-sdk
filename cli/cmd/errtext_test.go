package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
)

// The wording helpers are exercised across the three shapes that decide their
// output: a Network invocation (stock profile, nothing pointed anywhere), an
// Enterprise invocation whose active profile describes the target, and an
// Enterprise invocation with no usable profile — a scripted BLOCKS_BACKEND_URL
// run against a deployment the user never logged in to. The Network cases
// assert the exact stock strings: those bytes are the backward-compatibility
// contract, and a wording "improvement" that leaks into Network output is a
// regression this file exists to catch.

// enterpriseTestProfile is an Enterprise deployment profile as a completed
// `blocks login <instance-url>` leaves it: deployment URL, enterprise verdict,
// dashboard origin and one cached organization key.
func enterpriseTestProfile() profiles.Profile {
	return profiles.Profile{
		BaseURL:          "https://umbrella.blocks.example",
		Enterprise:       true,
		ProductName:      "Umbrella Blocks",
		DashboardBaseURL: "https://umbrella-dashboard.blocks.example",
		DefaultOrgID:     "org-eng",
		Orgs: map[string]profiles.OrgKey{
			"org-eng": {OrgName: "Engineering", ApiKey: "bk_ent", KeyId: "key-1"},
		},
	}
}

// inNetworkContext resolves the invocation against a fresh store: the stock
// blocks-network profile, no keys, nothing pointing anywhere.
//
// The profile-store isolation is registered with t.Cleanup rather than defer:
// a defer in this helper would restore the store when the helper returns, and
// the wording helpers under test re-read the store live (enterpriseLoginTarget
// calls profiles.Active), which would silently read the developer's own
// contexts.json. Cleanup also keeps the ordering with restoreCLIState: the
// store restore runs first, then the resolved context is reset against the
// real store, exactly as the existing tests' defer-inside-the-test does.
func inNetworkContext(t *testing.T) {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	// The resolver reads the CDM endpoint variable live, so a developer who
	// exports one would redirect the target and displace the seeded profiles —
	// the same non-hermeticity class the clictx suite fixed for its own store.
	// Cleared here so the byte-exact Network assertions cannot flip on the
	// machine they run on.
	t.Setenv(cdm.URLEnv, "")
	clictx.Resolve(nil)
}

// inEnterpriseProfileContext resolves the invocation with an Enterprise
// profile as the active target.
func inEnterpriseProfileContext(t *testing.T) {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	t.Setenv(cdm.URLEnv, "")
	seedProfile(t, "umbrella.blocks.example", enterpriseTestProfile())
	clictx.Resolve(nil)
}

// inEnterpriseNoProfileContext resolves the invocation the way a scripted
// Enterprise run does: no Enterprise profile exists, and an ambient backend URL
// points at a deployment whose discovery answers enterprise. The discovery
// server is real, because the no-profile variants exist precisely for the case
// where the verdict has to come from the wire. The server's URL is returned so
// callers can compute the host the deployment label will render as.
func inEnterpriseNoProfileContext(t *testing.T) string {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	t.Setenv(cdm.URLEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/cli-config" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"enterprise":true,"productName":"Umbrella Blocks"}`)
	}))
	t.Cleanup(srv.Close)
	clictx.Resolve(&clictx.Overrides{BackendURL: srv.URL})
	return srv.URL
}

// assertNoValueOnACommandLine pins the paste-safety property of the Enterprise
// wordings: the deployment/profile/org values these messages interpolate never
// share a line with the remedy command, so a value carrying `;` or $(...) can
// ride along a pasted command. It is the runtime complement of the source scan
// in command_shaped_output_test.go, which cannot know which verbs carry
// untrusted values.
func assertNoValueOnACommandLine(t *testing.T, msg string, values ...string) {
	t.Helper()
	for _, line := range commandLines(msg) {
		for _, v := range values {
			if v != "" && strings.Contains(line, v) {
				t.Errorf("value %q shares a command-shaped line:\n%s", v, msg)
			}
		}
	}
}

func TestBackendNotConfiguredWording(t *testing.T) {
	const networkStock = "BLOCKS_BACKEND_URL must be set (or configure via CDM)"

	cases := []struct {
		name string
		want string
	}{
		{"network", networkStock},
		{"enterprise_profile", "Backend URL not configured. Run 'blocks login <your-instance-url>' first, or set BLOCKS_BACKEND_URL in your environment."},
		{"enterprise_no_profile", "Backend URL not configured. Run 'blocks login <your-instance-url>' first, or set BLOCKS_BACKEND_URL in your environment."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.name {
			case "network":
				inNetworkContext(t)
			case "enterprise_profile":
				inEnterpriseProfileContext(t)
			case "enterprise_no_profile":
				inEnterpriseNoProfileContext(t)
			}
			if got := backendNotConfigured(networkStock).Error(); got != tc.want {
				t.Errorf("backendNotConfigured:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestNotLoggedInWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "not logged in — run 'blocks login' first"
		if got := notLoggedInError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		got := notLoggedInError().Error()
		want := "Not logged in — the active profile is umbrella.blocks.example (https://umbrella.blocks.example).\n  Run 'blocks login' to re-authenticate."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		assertNoValueOnACommandLine(t, got, "umbrella.blocks.example", "https://umbrella.blocks.example")
	})
	t.Run("enterprise_no_profile", func(t *testing.T) {
		inEnterpriseNoProfileContext(t)
		want := "Not logged in — run 'blocks login <your-instance-url>' to authenticate"
		if got := notLoggedInError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("displaced_profile_lends_no_name", func(t *testing.T) {
		// An ambient URL pointing at an Enterprise deployment the user never
		// logged in to must not borrow the active profile's name — the no-profile
		// advice is the correct one for that caller.
		inEnterpriseNoProfileContext(t)
		seedProfile(t, "elsewhere.blocks.example", enterpriseTestProfile())
		profiles.SetActive("elsewhere.blocks.example")
		want := "Not logged in — run 'blocks login <your-instance-url>' to authenticate"
		if got := notLoggedInError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestNotAuthenticatedWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "not authenticated — run 'blocks login' first, or provide --api-key"
		if got := notAuthenticatedError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		got := notAuthenticatedError().Error()
		want := "Not authenticated — the active profile is umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate, or provide --api-key."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		assertNoValueOnACommandLine(t, got, "umbrella.blocks.example")
	})
	t.Run("enterprise_no_profile", func(t *testing.T) {
		inEnterpriseNoProfileContext(t)
		want := "Not authenticated — run 'blocks login <your-instance-url>' first, or provide --api-key. You can generate an API key from the dashboard."
		if got := notAuthenticatedError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestExpiredCredentialsWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "credentials expired — run 'blocks login' to re-authenticate, or provide --api-key"
		if got := credentialsExpiredError().Error(); got != want {
			t.Errorf("publish: got %q, want %q", got, want)
		}
		want = "API key has expired — run 'blocks login' to create a new one"
		if got := apiKeyExpiredError().Error(); got != want {
			t.Errorf("shared: got %q, want %q", got, want)
		}
		want = "credentials expired — run 'blocks login' to re-authenticate"
		if got := cardLookupExpiredCredentialError().Error(); got != want {
			t.Errorf("card lookup: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		got := credentialsExpiredError().Error()
		want := "Credentials expired for umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate, or provide --api-key."
		if got != want {
			t.Errorf("publish: got %q, want %q", got, want)
		}
		got = apiKeyExpiredError().Error()
		want = "API key for umbrella.blocks.example has expired.\n  Run 'blocks login' to create a new one."
		if got != want {
			t.Errorf("shared: got %q, want %q", got, want)
		}
		got = cardLookupExpiredCredentialError().Error()
		want = "Credentials expired for umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate."
		if got != want {
			t.Errorf("card lookup: got %q, want %q", got, want)
		}
		assertNoValueOnACommandLine(t, got, "umbrella.blocks.example")
	})
	t.Run("enterprise_no_profile_names_the_host", func(t *testing.T) {
		// The verdict came from discovery, so no profile lends its name — the
		// deployment is named by host, the same rule the banner follows.
		srvURL := inEnterpriseNoProfileContext(t)
		host := originHost(srvURL)
		want := "Credentials expired for " + host + ".\n  Run 'blocks login' to re-authenticate, or provide --api-key."
		if got := credentialsExpiredError().Error(); got != want {
			t.Errorf("publish: got %q, want %q", got, want)
		}
		want = "API key for " + host + " has expired.\n  Run 'blocks login' to create a new one."
		if got := apiKeyExpiredError().Error(); got != want {
			t.Errorf("shared: got %q, want %q", got, want)
		}
		want = "Credentials expired for " + host + ".\n  Run 'blocks login' to re-authenticate."
		if got := cardLookupExpiredCredentialError().Error(); got != want {
			t.Errorf("card lookup: got %q, want %q", got, want)
		}
	})
}

func TestAuthFailedAndPermissionDeniedWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "authentication failed — run 'blocks login' to re-authenticate, then retry 'blocks publish'"
		if got := authFailedStoredCredentialError("blocks publish").Error(); got != want {
			t.Errorf("401: got %q, want %q", got, want)
		}
		want = "permission denied (HTTP 403) — check that your API key owns this agent"
		if got := permissionDenied403Error().Error(); got != want {
			t.Errorf("403: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		got := authFailedStoredCredentialError("blocks publish").Error()
		want := "Authentication failed on umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate, then retry."
		if got != want {
			t.Errorf("401: got %q, want %q", got, want)
		}
		got = permissionDenied403Error().Error()
		want = "Permission denied — your account does not have 'agent:manage' permission in Engineering.\n  Contact your org admin, or run 'blocks whoami' to see your permissions."
		if got != want {
			t.Errorf("403: got %q, want %q", got, want)
		}
		assertNoValueOnACommandLine(t, got, "Engineering")
	})
	t.Run("enterprise_org_unknown", func(t *testing.T) {
		// A key no local record accounts for names an organization only the
		// backend can, so the message must not guess one.
		inEnterpriseNoProfileContext(t)
		got := permissionDenied403Error().Error()
		want := "Permission denied — your account does not have 'agent:manage' permission for this organization.\n  Contact your org admin, or run 'blocks whoami' to see your permissions."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_no_profile_names_the_host", func(t *testing.T) {
		srvURL := inEnterpriseNoProfileContext(t)
		host := originHost(srvURL)
		got := authFailedStoredCredentialError("blocks publish").Error()
		want := "Authentication failed on " + host + ".\n  Run 'blocks login' to re-authenticate, then retry."
		if got != want {
			t.Errorf("401: got %q, want %q", got, want)
		}
	})
}

func TestValidationBeforeCommandWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		// Both commands share the stock wording; only the Enterprise variant
		// picks its verb.
		want := "fix validation errors in agent-card.json before publishing"
		for _, commandName := range []string{"blocks publish", "blocks register"} {
			if got := validationBeforeCommandError("agent-card.json", commandName).Error(); got != want {
				t.Errorf("%s: got %q, want %q", commandName, got, want)
			}
		}
	})
	t.Run("enterprise_register_uses_its_own_verb", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		got := validationBeforeCommandError("agent-card.json", "blocks register").Error()
		want := "Fix validation errors in agent-card.json before registering.\n  Run 'blocks check' for details."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		got = validationBeforeCommandError("agent-card.json", "blocks publish").Error()
		want = "Fix validation errors in agent-card.json before publishing.\n  Run 'blocks check' for details."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestExistingPaidAgentWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "Agent translator is already configured as a Paid agent. Please delete via the Blocks portal before publishing it as a Free agent."
		if got := existingPaidAgentMessage("translator", ""); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		want = "This agent is already configured as a Paid agent. Please delete via the Blocks portal before publishing it as a Free agent."
		if got := existingPaidAgentMessage("  ", ""); got != want {
			t.Errorf("blank name: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_with_dashboard_url", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Agent translator is already configured as a Paid agent. Delete it via the dashboard (https://umbrella-dashboard.blocks.example) before re-registering."
		if got := existingPaidAgentMessage("translator", resolveAppBaseURL()); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_without_dashboard_url", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Agent translator is already configured as a Paid agent. Delete it via the dashboard before re-registering."
		if got := existingPaidAgentMessage("translator", ""); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestRunCommandWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "could not read /proj/agent-card.json\nCreate an agent-card.json or run: blocks init <name>"
		if got := agentCardNotFoundError("/proj/agent-card.json").Error(); got != want {
			t.Errorf("card: got %q, want %q", got, want)
		}
		want = "no Python venv found. Run 'make setup' or activate a virtualenv with the Blocks SDK"
		if got := pythonVenvNotFoundError().Error(); got != want {
			t.Errorf("venv: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "agent-card.json not found. Run 'blocks init' to create a new project, or see https://blocks.ai/docs/connect-your-agent for creating an agent-card manually."
		if got := agentCardNotFoundError("/proj/agent-card.json").Error(); got != want {
			t.Errorf("card: got %q, want %q", got, want)
		}
		want = "No Python virtual environment found. Run 'pip install -e .' in a venv, or check that .venv exists in your project directory."
		if got := pythonVenvNotFoundError().Error(); got != want {
			t.Errorf("venv: got %q, want %q", got, want)
		}
	})
}

func TestInviteRequestFailedWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "request failed (HTTP 403): no grant to revoke"
		if got := inviteRequestFailedError(403, "no grant to revoke").Error(); got != want {
			t.Errorf("detail: got %q, want %q", got, want)
		}
		want = "request failed: HTTP 403"
		if got := inviteRequestFailedError(403, "").Error(); got != want {
			t.Errorf("no detail: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Request to umbrella.blocks.example failed (HTTP 403): no grant to revoke"
		if got := inviteRequestFailedError(403, "no grant to revoke").Error(); got != want {
			t.Errorf("detail: got %q, want %q", got, want)
		}
		want = "Request to umbrella.blocks.example failed (HTTP 403): no details returned"
		if got := inviteRequestFailedError(403, "").Error(); got != want {
			t.Errorf("no detail: got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_no_profile_names_the_host", func(t *testing.T) {
		srvURL := inEnterpriseNoProfileContext(t)
		host := originHost(srvURL)
		want := "Request to " + host + " failed (HTTP 403): no grant to revoke"
		if got := inviteRequestFailedError(403, "no grant to revoke").Error(); got != want {
			t.Errorf("detail: got %q, want %q", got, want)
		}
		want = "Request to " + host + " failed (HTTP 403): no details returned"
		if got := inviteRequestFailedError(403, "").Error(); got != want {
			t.Errorf("no detail: got %q, want %q", got, want)
		}
	})
}

func TestDashboardURLUnresolvableWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "could not resolve dashboard URL - set BLOCKS_APP_BASE_URL or BLOCKS_DASHBOARD_URL, or ensure CDM config is reachable"
		if got := dashboardURLUnresolvableError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Could not resolve dashboard URL. Run 'blocks login <your-instance-url>' first to configure your deployment."
		if got := dashboardURLUnresolvableError().Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestRemoteConfigWarningWording(t *testing.T) {
	cause := errors.New("dial timeout")
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "Warning: failed to fetch remote config: dial timeout"
		if got := remoteConfigWarning(cause); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Warning: could not reach deployment configuration. Commands may fall back to defaults. (dial timeout)"
		if got := remoteConfigWarning(cause); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLoginOrgUnresolvableWording(t *testing.T) {
	const target = "https://umbrella.blocks.example"
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "could not determine the organization for this API key from " + target +
			" — the key was not stored; verify the key is valid and the instance URL is correct, then retry"
		if got := loginOrgUnresolvableError(target, false).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("network_no_target", func(t *testing.T) {
		inNetworkContext(t)
		want := "could not determine the organization for this API key from the target instance — the key was not stored; verify the key is valid and the instance URL is correct, then retry"
		if got := loginOrgUnresolvableError("", false).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inNetworkContext(t) // the login flow passes its own verdict; the context is irrelevant here
		want := "Could not determine the organization for this API key. Verify the key is valid for " + target + " and try again."
		if got := loginOrgUnresolvableError(target, true).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestLogoutIncompleteWording(t *testing.T) {
	cause := errors.New("permission denied")
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "logout incomplete — Blocks credentials remain on disk: permission denied"
		if got := logoutIncompleteError("umbrella.blocks.example", cause).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise_named_profile", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		err := logoutIncompleteError("umbrella.blocks.example", cause)
		got := err.Error()
		want := "Logout incomplete — credentials for umbrella.blocks.example could not be fully removed (permission denied).\n  Try 'blocks profile remove <name>' to clean up."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		// Both arms wrap the cause, so introspection behaves the same whichever
		// wording printed.
		if !errors.Is(err, cause) {
			t.Errorf("the enterprise arm must keep the cause in the chain")
		}
		assertNoValueOnACommandLine(t, got, "umbrella.blocks.example")
	})
	t.Run("enterprise_default_profile_gets_no_remove_suggestion", func(t *testing.T) {
		// The default profile cannot be removed; suggesting it would send the
		// user to a command that refuses.
		inEnterpriseProfileContext(t)
		want := "logout incomplete — Blocks credentials remain on disk: permission denied"
		if got := logoutIncompleteError(profiles.DefaultProfile, cause).Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestAgentNotFoundWording(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		want := "agent \"translator\" not found — check the spelling.\n  If it is a private agent your account can access, log in first: run 'blocks login'."
		if got := agentNotFoundOnDeploymentError("translator", "umbrella.blocks.example").Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		want := "Agent \"translator\" not found on umbrella.blocks.example. Check spelling, or verify the agent is registered on this deployment."
		if got := agentNotFoundOnDeploymentError("translator", "umbrella.blocks.example").Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestOriginHost(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://umbrella.blocks.example", "umbrella.blocks.example"},
		{"https://umbrella.blocks.example/", "umbrella.blocks.example"},
		{"  https://umbrella.blocks.example  ", "umbrella.blocks.example"},
		{"https://umbrella.blocks.example:8443", "umbrella.blocks.example:8443"},
		{"not a url", "not a url"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := originHost(tc.raw); got != tc.want {
			t.Errorf("originHost(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// The remaining tests exercise the wiring, not the wording: each command's
// mapper must reach the helper with the inputs that decide its output — the
// verb publish vs register, the credential source on a 401, the org on a 403,
// the response body on an invite failure. A helper that is unit-tested but
// never called correctly changes nothing.

func TestWhoamiEnterpriseNotLoggedInNamesTheProfile(t *testing.T) {
	inEnterpriseProfileContext(t)
	// The post-logout shape: the profile still describes the deployment but
	// holds no cached key, which is whoami's "not logged in" arm.
	p := enterpriseTestProfile()
	p.Orgs = map[string]profiles.OrgKey{}
	p.DefaultOrgID = ""
	seedProfile(t, "umbrella.blocks.example", p)

	err := runWhoami(whoamiCmd, nil)
	if err == nil {
		t.Fatal("whoami should fail with no cached key")
	}
	want := "Not logged in — the active profile is umbrella.blocks.example (https://umbrella.blocks.example).\n  Run 'blocks login' to re-authenticate."
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestSubmitPublishErrorEnterpriseWording(t *testing.T) {
	inEnterpriseProfileContext(t)
	// The profile's cached key is the credential this invocation sends, so a
	// 401 is answered by the stored-credential wording.
	got := submitPublishError(&blocksapi.APIError{StatusCode: 401}, "translator",
		registry.PromotionInput{}, submitOptions{commandName: "blocks publish"})
	want := "Authentication failed on umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate, then retry."
	if got.Error() != want {
		t.Errorf("401: got %q, want %q", got.Error(), want)
	}
	got = submitPublishError(&blocksapi.APIError{StatusCode: 403}, "translator",
		registry.PromotionInput{}, submitOptions{commandName: "blocks publish"})
	want = "Permission denied — your account does not have 'agent:manage' permission in Engineering.\n  Contact your org admin, or run 'blocks whoami' to see your permissions."
	if got.Error() != want {
		t.Errorf("403: got %q, want %q", got.Error(), want)
	}
}

func TestPreparePublishEnterpriseValidationUsesTheCommandVerb(t *testing.T) {
	inEnterpriseProfileContext(t)
	dir := t.TempDir()
	t.Chdir(dir)
	// A card that parses but fails validation, so preparePublish stops at the
	// validation summary.
	if err := os.WriteFile(filepath.Join(dir, "agent-card.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write card: %v", err)
	}

	_, err := preparePublish("blocks register", nil)
	if err == nil {
		t.Fatal("preparePublish should fail on an empty card")
	}
	if !strings.Contains(err.Error(), "before registering") {
		t.Errorf("register: got %q", err.Error())
	}
	_, err = preparePublish("blocks publish", nil)
	if err == nil {
		t.Fatal("preparePublish should fail on an empty card")
	}
	if !strings.Contains(err.Error(), "before publishing") {
		t.Errorf("publish: got %q", err.Error())
	}
}

func TestInviteHandleErrorResponseEnterpriseWording(t *testing.T) {
	inEnterpriseProfileContext(t)

	newResp := func(status int, body string) *http.Response {
		rec := httptest.NewRecorder()
		rec.Code = status
		fmt.Fprint(rec.Body, body)
		return rec.Result()
	}

	got := handleErrorResponse(newResp(403, `{"error":"no grant to revoke"}`))
	want := "Request to umbrella.blocks.example failed (HTTP 403): no grant to revoke"
	if got.Error() != want {
		t.Errorf("detail: got %q, want %q", got.Error(), want)
	}
	got = handleErrorResponse(newResp(500, ``))
	want = "Request to umbrella.blocks.example failed (HTTP 500): no details returned"
	if got.Error() != want {
		t.Errorf("no detail: got %q, want %q", got.Error(), want)
	}
}

// The drained legacy store is the standard post-migration state, and it is an
// absent login, not a store failure. It used to surface as "failed to load
// credentials: no Blocks credentials found" — a message that names no
// deployment and that no wording pass had ever designed — so the most common
// logged-out shape never reached the context-aware not-authenticated wording
// at all. These two tests pin both halves of the distinction the adapter now
// draws.
func TestDrainedLegacyStoreIsAnAbsentLoginNotAStoreFailure(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	seedProfile(t, "umbrella.blocks.example", profiles.Profile{
		BaseURL: "https://umbrella.blocks.example", Enterprise: true,
	})
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(blocksAPIKeyEnv, "")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"schema_version":3}`), 0600); err != nil {
		t.Fatalf("write drained store: %v", err)
	}
	origPath := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credPath, nil }
	t.Cleanup(func() { auth.CredentialPathFunc = origPath })

	clictx.Resolve(effectiveOverrides(publishCmd))

	_, err := resolvePublishApiKey()
	if err == nil {
		t.Fatal("publish should refuse without a credential")
	}
	want := "Not authenticated — the active profile is umbrella.blocks.example.\n  Run 'blocks login' to re-authenticate, or provide --api-key."
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

func TestUnreadableLegacyStoreIsStillAStoreFailure(t *testing.T) {
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	seedProfile(t, "umbrella.blocks.example", profiles.Profile{
		BaseURL: "https://umbrella.blocks.example", Enterprise: true,
	})
	t.Setenv(blocksBackendURLEnv, "")
	t.Setenv(blocksAPIKeyEnv, "")

	credPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credPath, []byte(`not json`), 0600); err != nil {
		t.Fatalf("write unreadable store: %v", err)
	}
	origPath := auth.CredentialPathFunc
	auth.CredentialPathFunc = func() (string, error) { return credPath, nil }
	t.Cleanup(func() { auth.CredentialPathFunc = origPath })

	clictx.Resolve(effectiveOverrides(publishCmd))

	_, err := resolvePublishApiKey()
	if err == nil {
		t.Fatal("publish should refuse when the store cannot be read")
	}
	// The store-failure arm is kept for stores that cannot be read: the caller
	// may well be logged in, and telling them to log in again invites a key the
	// store may fail to record.
	if !strings.HasPrefix(err.Error(), "failed to load credentials:") {
		t.Errorf("got %q, want the store-failure wording", err.Error())
	}
}
