package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

// A profile name, a deployment URL and an agent name are all attacker-influenceable: a
// profile is named after the host of whatever deployment a login reached, that host can
// come from a project .env or from a deployment's own discovery payload, and an agent
// name arrives from the registry. termsafe.Text makes such a value safe to *display* and
// says nothing about whether it is safe to *run* — `;`, `&&`, backticks and $(...) pass
// through it as the ordinary printable characters they are. So none of them may be
// interpolated into a line the CLI formats as a command, because a line presented as
// something to copy is a line that gets pasted into a shell. The two protections are
// orthogonal and both are needed.
//
// The scan reuses commandLines, this package's one definition of "reads as something to
// run", so a next-steps block that starts wording a step as a command in future is
// covered by these cases rather than by a report.

// injectedProfileName is what a profile named after a hostile host slug carries, and
// injectedProfileBaseURL the deployment it describes. Both are values the next-steps
// block has reason to know and no business pasting into a command.
const injectedProfileName = "acme.example.test;$(id)"
const injectedProfileBaseURL = "https://acme.example.test/x;$(id)"

// injectionFragments are the sequences that turn a pasted line into more than one
// command and that no line the CLI formats as a command has any legitimate reason to
// carry. `&&` is deliberately absent: a scaffold's own install step is a real
// two-command line, so requiring its absence would forbid a correct instruction rather
// than an interpolated value.
var injectionFragments = []string{";", "$(", "`"}

// assertNoInjectedValueInCommandLines fails when a line that reads as a command carries
// any of values, or any injection fragment. The fragments are checked as well as the
// whole values because a formatter that interpolates only part of a value — its host,
// say — is still handing over a command.
func assertNoInjectedValueInCommandLines(t *testing.T, out string, values ...string) {
	t.Helper()
	lines := commandLines(out)
	if len(lines) == 0 {
		t.Fatalf("no line of the output names a command, so there is nothing to check:\n%s", out)
	}
	needles := append(append([]string{}, injectionFragments...), values...)
	for _, line := range lines {
		for _, needle := range needles {
			if strings.Contains(line, needle) {
				t.Errorf("a line formatted as a command carries %q:\n%s", needle, line)
			}
		}
	}
}

// The agent next-steps block knows the active profile and, on an enterprise deployment,
// offered its login step with that profile name interpolated raw — a name that reaches
// the store from a host slug. The step has to survive; only the interpolation goes.
func TestTheNextStepsBlockOffersNoPastableCommandBuiltFromTheProfile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	// No org key, so the login step is printed at all, and enterprise, so the
	// instance-qualified form is the branch under test.
	seedProfile(t, injectedProfileName, profiles.Profile{
		BaseURL:    injectedProfileBaseURL,
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	resolveCLIContext(t, rootCmd)
	if !clictx.Enterprise() {
		t.Fatal("premise: the enterprise branch must be the one reached")
	}

	out := captureStdout(func() {
		printNextSteps(wizard.Config{Name: "test_agent", Language: "python", Mode: "provider"})
	})

	assertNoInjectedValueInCommandLines(t, out, injectedProfileName, injectedProfileBaseURL)
	if !strings.Contains(out, "blocks login") {
		t.Errorf("the login step must still be offered:\n%s", out)
	}
}

// The webapp block is held to the same rule, and has no profile-derived value to
// interpolate in the first place — which is what this case pins, since the tempting way
// to make it instance-aware is the one the agent block just lost.
func TestTheWebappNextStepsBlockOffersNoPastableCommandBuiltFromTheProfile(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	seedProfile(t, injectedProfileName, profiles.Profile{
		BaseURL:    injectedProfileBaseURL,
		Enterprise: true,
		Orgs:       map[string]profiles.OrgKey{},
	})
	resolveCLIContext(t, rootCmd)

	out := captureStdout(func() {
		printWebappNextSteps(wizard.Config{Name: "page", Mode: "webapp", Agents: []string{"translator"}}, "page")
	})

	assertNoInjectedValueInCommandLines(t, out, injectedProfileName, injectedProfileBaseURL)
}

// The same rule in an error. The agent name comes from the registry — the suggestion
// list the webapp wizard populates from the backend — and the line that reported it also
// told the user to run a command, so the two are separated: the name is still reported,
// on a line of its own, and the command carries nothing but itself.
func TestTheAgentNotFoundErrorSeparatesTheNameFromTheCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(srv.Close)
	t.Chdir(t.TempDir())

	const hostileAgent = "translator;$(id)"
	cfg := wizard.Config{
		Name:           "page",
		Mode:           "webapp",
		Agents:         []string{hostileAgent},
		BlocksBaseURL:  srv.URL,
		BackendBaseURL: srv.URL,
	}

	err := scaffoldWebappProject(context.Background(), cfg, blocksapi.NewClient(srv.URL, "bk_test"))
	if err == nil {
		t.Fatal("premise: an agent the registry does not have must be reported")
	}
	assertNoInjectedValueInCommandLines(t, err.Error(), hostileAgent)
	if !strings.Contains(err.Error(), hostileAgent) {
		t.Errorf("the error must still name the agent it could not find: %v", err)
	}
}

// The webapp next-steps block is context-aware like the agent block: the
// login line disappears when this invocation already holds a credential for
// the deployment it reaches, an Enterprise deployment gets the placeholder
// form, and an unauthenticated caller (who can only have scaffolded public
// agents) is told login is optional rather than mandatory.
func TestPrintWebappNextStepsBranchMatrix(t *testing.T) {
	cfg := wizard.Config{Name: "page", Mode: "webapp", Agents: []string{"translator"}}

	t.Run("network unauthenticated offers optional login and the enterprise hint", func(t *testing.T) {
		inNetworkContext(t)
		out := captureStdout(func() { printWebappNextSteps(cfg, "page") })
		for _, want := range []string{
			"blocks login --write-env",
			"for Blocks Enterprise: blocks login <your-instance> --write-env",
			"optional for public agents",
			"blocks dev",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("network/unauthed output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "blocks deploy --list") {
			t.Errorf("network deploy hint mentions custom targets:\n%s", out)
		}
	})

	t.Run("network authenticated skips the login line", func(t *testing.T) {
		inNetworkContext(t)
		seedProfile(t, "blocks-network", profiles.Profile{
			BaseURL: "",
			Orgs:    map[string]profiles.OrgKey{"org": {ApiKey: "bk_net", OrgName: "Org"}},
		})
		clictx.Resolve(nil)
		if !authenticatedForTarget() {
			t.Fatal("premise: a seeded org key must authenticate the invocation")
		}
		out := captureStdout(func() { printWebappNextSteps(cfg, "page") })
		if strings.Contains(out, "blocks login") {
			t.Errorf("authed output still tells the user to log in:\n%s", out)
		}
		if !strings.Contains(out, "blocks dev") {
			t.Errorf("authed output missing the dev step:\n%s", out)
		}
	})

	t.Run("enterprise unauthenticated uses the placeholder and names custom targets", func(t *testing.T) {
		restoreCLIState(t)
		t.Cleanup(isolateProfiles(t))
		t.Setenv(cdm.URLEnv, "")
		seedProfile(t, "umbrella.blocks.example", profiles.Profile{
			BaseURL:    "https://umbrella.blocks.example",
			Enterprise: true,
			Orgs:       map[string]profiles.OrgKey{},
		})
		clictx.Resolve(nil)
		out := captureStdout(func() { printWebappNextSteps(cfg, "page") })
		for _, want := range []string{
			"blocks login <your-instance> --write-env",
			"optional for public agents",
			"blocks deploy --list",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("enterprise/unauthed output missing %q:\n%s", want, out)
			}
		}
		assertNoInjectedValueInCommandLines(t, out, "umbrella.blocks.example", "https://umbrella.blocks.example")
	})

	t.Run("enterprise authenticated skips the login line", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		if !authenticatedForTarget() {
			t.Fatal("premise: the seeded enterprise org key must authenticate the invocation")
		}
		out := captureStdout(func() { printWebappNextSteps(cfg, "page") })
		if strings.Contains(out, "blocks login") {
			t.Errorf("authed enterprise output still tells the user to log in:\n%s", out)
		}
	})
}

// The resolved-URLs fallback leads with a runnable remedy instead of naming
// only the environment variable a script would set — and the remedy is
// deployment-aware like the next-steps block: Enterprise names the instance
// login, Network leads with the flag/profile remedies and offers the
// Enterprise login as a hint, never as a placeholder to paste.
func TestPrintWebappResolvedURLsFallbackLeadsWithLogin(t *testing.T) {
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		out := captureStdout(func() {
			printWebappResolvedURLs("https://example.com", "https://example.com", false)
		})
		if !strings.Contains(out, "run 'blocks login <your-instance>' first") {
			t.Errorf("enterprise fallback does not lead with the login remedy:\n%s", out)
		}
	})

	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		out := captureStdout(func() {
			printWebappResolvedURLs("https://example.com", "https://example.com", false)
		})
		for _, want := range []string{"pass --backend-url", "'blocks profile use'", "For Blocks Enterprise, run 'blocks login"} {
			if !strings.Contains(out, want) {
				t.Errorf("network fallback missing %q:\n%s", want, out)
			}
		}
		// The Enterprise login is a hint, never the leading remedy a Network
		// user could mistake for a command to paste verbatim.
		if strings.HasPrefix(strings.TrimSpace(out[strings.Index(out, "target another backend, "):]), "target another backend, run 'blocks login") {
			t.Errorf("network fallback leads with the Enterprise placeholder:\n%s", out)
		}
	})

	explicit := captureStdout(func() {
		printWebappResolvedURLs("https://example.com", "https://example.com", true)
	})
	if strings.Contains(explicit, "No backend was specified") {
		t.Errorf("explicit resolution printed the fallback:\n%s", explicit)
	}
}

// The wizard help texts name the active product and, on Enterprise, describe
// the agent choice in deployment vocabulary instead of marketplace terms.
func TestWebappWizardHelpTextsAreDeploymentAware(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		inNetworkContext(t)
		if got := wizard.HelpWebappAgentsText(); !strings.Contains(got, "Blocks Network agent(s)") {
			t.Errorf("agents help does not name the product:\n%s", got)
		}
		if got := wizard.HelpProjectKindText(); !strings.Contains(got, "consumer that calls agents") {
			t.Errorf("project-kind help lost the network wording:\n%s", got)
		}
	})
	t.Run("enterprise", func(t *testing.T) {
		inEnterpriseProfileContext(t)
		// branding is set at command startup from the active profile; the
		// resolve helper replays only the context, so the profile's product
		// name is applied here the way PersistentPreRun would.
		branding.Set("Umbrella Blocks")
		t.Cleanup(branding.Reset)
		if got := wizard.HelpProjectKindText(); !strings.Contains(got, "client that calls other") {
			t.Errorf("project-kind help kept marketplace wording on Enterprise:\n%s", got)
		}
		if got := wizard.HelpProjectKindText(); !strings.Contains(got, "Umbrella Blocks embed-auth widget") {
			t.Errorf("project-kind help does not name the product:\n%s", got)
		}
	})
}
