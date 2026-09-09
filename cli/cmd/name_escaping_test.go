package cmd

import (
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/branding"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
	"github.com/pubnub/blocks-sdk/cli/internal/registry"
)

// forgedName is a name that rewrites the terminal rather than appearing in it: erase the
// current line, return to its start, move up one, and print something else. Placed in a
// name the CLI prints, it wipes out the line above — which for these commands is the one
// stating which deployment the agent landed on, or which identity later commands act as.
const forgedName = "victim_agent\x1b[2K\r\x1b[1Aharmless_agent"

// forgedOrg is the same forgery in an organization name, which is authored by the
// deployment rather than locally.
const forgedOrg = "Engineering\x1b[2K\r\x1b[1AOperations"

// assertNoForgery fails when out carries the sequences forgedName/forgedOrg smuggle in,
// and when the escaped form that must replace them is missing. Both halves matter:
// dropping the bytes silently would also pass the first check while hiding evidence of
// tampering and making two different names render identically.
//
// It deliberately does not reject every control character: these lines are bold-printed,
// so the CLI's own ANSI codes are expected in the output it produces.
func assertNoForgery(t *testing.T, out string) {
	t.Helper()
	for _, seq := range []struct {
		raw, name string
	}{
		{"\x1b[2K", "erase-line"},
		{"\x1b[1A", "cursor-up"},
		{"\r", "carriage-return"},
	} {
		if strings.Contains(out, seq.raw) {
			t.Errorf("output carries a raw %s sequence, which rewrites the lines around it:\n%q", seq.name, out)
		}
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf("the escape must be rendered inertly and visibly, not stripped:\n%q", out)
	}
}

// The agent name reaches these lines from the agent card. `publish` and `register`
// validate it against ^[a-zA-Z0-9_]+$ before printing anything, so this is
// defence-in-depth rather than a live hole — but the escaping belongs at the sink, since
// nothing at the sink can see that a validator ran, and the guarantee "these lines cannot
// be forged" should not depend on a check three call frames away staying exactly as
// strict as it is today.
func TestPublishOutputCannotBeForgedByTheAgentName(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)
	free := registry.PromotionInput{Listing: "private", BillingMode: "free"}

	for _, sink := range []struct {
		name  string
		print func()
	}{
		{"the publish intro", func() { printPublishIntro(forgedName) }},
		{"the register intro", func() { printRegisterIntro(forgedName) }},
		{"the publish summary", func() { printPublishSummary(forgedName, free) }},
		{"the success block", func() { printPublishSuccess(forgedName, free, "", "blocks publish") }},
	} {
		t.Run(sink.name, func(t *testing.T) {
			out := captureStdout(sink.print)
			assertNoForgery(t, out)
			// The name still has to be readable, or the guard has made the line useless.
			if !strings.Contains(out, "victim_agent") {
				t.Errorf("%s must still name the agent:\n%q", sink.name, out)
			}
		})
	}
}

// The success block prints the name twice — centred in the wordmark, and in the sentence
// naming the deployment — and boldText wraps rather than sanitizes, so a name reaching it
// through boldText is interpreted exactly as if it had been printed alone.
func TestPublishSuccessLogoCannotBeForgedByTheAgentName(t *testing.T) {
	seedEnterpriseProfileForTest(t)
	branding.Set("Umbrella Corporation")
	t.Cleanup(branding.Reset)

	out := captureStdout(func() {
		printPublishSuccess(forgedName, registry.PromotionInput{Listing: "public", BillingMode: "free"}, "", "blocks publish")
	})

	assertNoForgery(t, out)
	// The line the forgery targets: erase-line plus cursor-up placed in the wordmark
	// would take the "registered on <deployment>" sentence with it.
	if !strings.Contains(out, "registered on Umbrella Corporation") && !strings.Contains(out, "published to Umbrella Corporation") {
		t.Errorf("the deployment sentence must survive the name:\n%q", out)
	}
}

// The organization name is the live case: it is authored by the deployment, cached by
// login, and never validated. `blocks whoami` is one of the two places a user goes to
// check which identity commands will act as, and every line of it can be overwritten by
// a name that says so.
func TestWhoamiCannotBeForgedByTheOrganizationName(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme.example.com", profiles.Profile{
		BaseURL:      "https://acme.example.com",
		Enterprise:   true,
		ProductName:  "Acme Corp",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: forgedOrg, ApiKey: "bk_acme"}},
	})
	resolveCLIContext(t, whoamiCmd)

	out := captureStdout(func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})

	assertNoForgery(t, out)
	// The lines a forged name would erase.
	for _, want := range []string{"Profile:  acme.example.com", "Org ID:   org-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("whoami must still report %q:\n%q", want, out)
		}
	}
}
