package cmd

import (
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// TestResolveDashboardURLPrefersAppBaseOverride confirms an explicit dashboard
// override (BLOCKS_APP_BASE_URL) wins over the deployment fallback.
func TestResolveDashboardURLPrefersAppBaseOverride(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "https://dashboard.acme.com")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://api.acme.com")

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://dashboard.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want dashboard override", got)
	}
}

// TestResolveDashboardURLFallsBackToBackendEnv covers the case where, without a
// dashboard override, the dashboard opens on the deployment origin
// (BLOCKS_BACKEND_URL) rather than stock CDM / app.blocks.ai.
func TestResolveDashboardURLFallsBackToBackendEnv(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "https://blocks.acme.com")
	resolveCLIContext(t, rootCmd)

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://blocks.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want backend-env fallback", got)
	}
}

// TestResolveDashboardURLFallsBackToActiveProfile covers the case where the active
// profile's BaseURL drives the dashboard origin when no override and no
// BLOCKS_BACKEND_URL are set.
func TestResolveDashboardURLFallsBackToActiveProfile(t *testing.T) {
	restore := isolateProfiles(t)
	defer restore()
	_ = profiles.Upsert(profiles.DefaultProfile, profiles.Profile{
		BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{},
	}, true)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_BACKEND_URL", "")
	resolveCLIContext(t, rootCmd)

	got, err := resolveDashboardURL()
	if err != nil {
		t.Fatalf("resolveDashboardURL: %v", err)
	}
	if got != "https://blocks.acme.com" {
		t.Errorf("resolveDashboardURL = %q, want active-profile fallback", got)
	}
}

// `blocks dashboard` does not fetch the URL it resolves — it asks the operating system
// to dispatch it to whichever handler is registered for its scheme. So the value is not
// merely a request target: `file:` reads a local path, a UNC path reaches an SMB server,
// and a registered custom protocol starts an application. Two of the tiers that can
// supply it (BLOCKS_APP_BASE_URL, BLOCKS_DASHBOARD_URL) are imported from a project
// .env, which arrives with a cloned repository, so these cases drive the real command
// over a real .env and assert the refusal happens before the opener is reached.
//
// The opener is a recorder rather than the real one for the obvious reason: a test that
// proved this by opening a `file:` URL would be the defect.

// recordingOpener replaces the browser opener with one that records every URL it is
// handed, so a case can assert not just what was opened but that nothing was.
func recordingOpener(t *testing.T) *[]string {
	t.Helper()
	orig := openBrowserFunc
	var opened []string
	openBrowserFunc = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openBrowserFunc = orig })
	return &opened
}

// dashboardEnvPin puts the test in a project directory whose .env carries the given
// assignments and loads it exactly as the root command does at startup — the route a
// checked-in .env takes into this process.
func dashboardEnvPin(t *testing.T, assignments string) {
	t.Helper()
	t.Chdir(writeProjectEnv(t, t.TempDir(), assignments))
	loadProjectEnv(t, blocksAppBaseURLEnv, blocksDashboardURLEnv, blocksBackendURLEnv, blocksAPIKeyEnv)
	if envFileSource(blocksDashboardURLEnv) == "" && envFileSource(blocksAppBaseURLEnv) == "" {
		t.Fatal("premise: the dashboard override must have reached this process from the .env")
	}
}

// A project .env naming anything but a web address the CLI is willing to open.
func TestAProjectEnvDashboardURLNeverReachesTheURLHandler(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"a local file", "file:///etc/passwd"},
		{"a script URL", "javascript:alert(document.domain)"},
		{"a UNC path", `\\attacker.example.test\share`},
		{"a custom protocol", "acme-updater://install?pkg=evil"},
		{"cleartext http to a remote host", "http://dashboard.acme.example.com"},
		{"userinfo hiding the authority", "https://dashboard.acme.example.com@collector.example.test"},
		{"no host at all", "https:///agents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreCLIState(t)
			defer isolateProfiles(t)()
			isolateAmbientState(t)
			seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
			dashboardEnvPin(t, blocksDashboardURLEnv+"="+tc.url+"\n")
			opened := recordingOpener(t)

			out, err := runRootCapturing(t, "dashboard")
			if err == nil {
				t.Errorf("dashboard accepted %q:\n%s", tc.url, out)
			}
			if len(*opened) != 0 {
				t.Fatalf("the URL handler was asked to open %q", (*opened)[0])
			}
		})
	}
}

// The overrides stay on the .env allowlist because a self-hosted deployment has to be
// able to name its own dashboard, so the ordinary case has to keep working: this is what
// makes the refusals above a check on the value rather than on where it came from.
func TestASelfHostedDashboardFromAProjectEnvStillOpens(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	const dashboard = "https://dashboard.acme.example.com"
	dashboardEnvPin(t, blocksDashboardURLEnv+"="+dashboard+"\n")
	opened := recordingOpener(t)

	out, err := runRootCapturing(t, "dashboard")
	if err != nil {
		t.Fatalf("dashboard: %v\n%s", err, out)
	}
	if len(*opened) != 1 || (*opened)[0] != dashboard {
		t.Errorf("opened %q, want %q", *opened, dashboard)
	}
}

// A local deployment is served over http, which is why the rule allows it for loopback
// and only there — the same three spellings `blocks login` accepts.
func TestALoopbackDashboardStillOpens(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		t.Run(host, func(t *testing.T) {
			restoreCLIState(t)
			defer isolateProfiles(t)()
			isolateAmbientState(t)
			seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
			dashboard := "http://" + host + ":3000"
			dashboardEnvPin(t, blocksDashboardURLEnv+"="+dashboard+"\n")
			opened := recordingOpener(t)

			out, err := runRootCapturing(t, "dashboard")
			if err != nil {
				t.Fatalf("dashboard: %v\n%s", err, out)
			}
			if len(*opened) != 1 || (*opened)[0] != dashboard {
				t.Errorf("opened %q, want %q", *opened, dashboard)
			}
		})
	}
}

// The agent name is concatenated into the path, and arrives either from the command line
// or from agent-card.json in a cloned repository. It may not decide anything but which
// agent's page is opened: no second path segment, no query, no traversal out of /agents.
func TestAnAgentNameCannotChangeTheDashboardPath(t *testing.T) {
	const origin = "https://dashboard.acme.example.com"
	for _, name := range []string{"a/b", "..", "../../admin", "a?next=x", "a#frag", "a%2Fb", "a b"} {
		if got, err := dashboardURL(origin, name); err == nil {
			t.Errorf("dashboardURL(%q, %q) = %q, want the name refused", origin, name, got)
		}
	}
	got, err := dashboardURL(origin+"/", "my_agent")
	if err != nil {
		t.Fatalf("dashboardURL: %v", err)
	}
	if got != origin+"/agents/my_agent" {
		t.Errorf("dashboardURL = %q, want %q", got, origin+"/agents/my_agent")
	}
}

// And through the real command, because the refusal has to happen on the way to the
// opener rather than only inside a helper a caller could bypass.
func TestAHostileAgentNameNeverReachesTheURLHandler(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	dashboardEnvPin(t, blocksDashboardURLEnv+"=https://dashboard.acme.example.com\n")
	opened := recordingOpener(t)

	out, err := runRootCapturing(t, "dashboard", "../../admin?x=1")
	if err == nil {
		t.Errorf("dashboard accepted an agent name that is a path:\n%s", out)
	}
	if len(*opened) != 0 {
		t.Fatalf("the URL handler was asked to open %q", (*opened)[0])
	}
}

// The refusal has to say which value it refused — a dashboard that will not open and
// does not say why is not something a developer can act on — while carrying nothing that
// reads as a command, for the reason the decline notes spell out.
func TestTheDashboardRefusalNamesTheValueWithoutOfferingACommand(t *testing.T) {
	const hostile = "file:///etc/passwd;$(id)"
	_, err := dashboardURL(hostile, "")
	if err == nil {
		t.Fatal("premise: the value under test must be refused")
	}
	if !strings.Contains(err.Error(), "file:///etc/passwd") {
		t.Errorf("the refusal must name the value it refused: %v", err)
	}
	for _, line := range commandLines(err.Error()) {
		t.Errorf("the refusal offers a line that reads as something to run:\n%s", line)
	}
}
