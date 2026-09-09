package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
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
