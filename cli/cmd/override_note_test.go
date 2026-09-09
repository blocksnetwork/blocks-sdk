package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// The reproduction this note exists for: the active profile is stock Blocks
// Network, and a leftover project .env still pins an enterprise deployment. Every
// profile-shaped signal said Network while every request went to the pinned host.
func TestProfileListReportsAnActiveBackendOverride(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://acme.blocks.ai\n")
	resolveCLIContext(t, profileListCmd)

	out := captureStdout(func() {
		if err := profileListCmd.RunE(profileListCmd, nil); err != nil {
			t.Fatalf("profile list: %v", err)
		}
	})

	want := "* " + profiles.DefaultProfile + "  Blocks Network (default)\n" +
		"  Note: BLOCKS_BACKEND_URL in ./.env overrides this → https://acme.blocks.ai\n"
	if out != want {
		t.Errorf("profile list output:\n%q\nwant:\n%q", out, want)
	}
}

// The regression guard on the most-read output in the CLI: with no override, not
// one byte changes.
func TestProfileListPrintsNoNoteWithoutAnOverride(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	// An empty project directory: nothing pins a backend, and the .env-provenance
	// record is cleared with it.
	t.Chdir(t.TempDir())
	loadProjectEnv(t, blocksBackendURLEnv)
	resolveCLIContext(t, profileListCmd)

	out := captureStdout(func() {
		if err := profileListCmd.RunE(profileListCmd, nil); err != nil {
			t.Fatalf("profile list: %v", err)
		}
	})

	want := "* acme  https://blocks.acme.com\n" +
		"  " + profiles.DefaultProfile + "  Blocks Network (default)\n"
	if out != want {
		t.Errorf("profile list output:\n%q\nwant:\n%q", out, want)
	}
}

// An override pointing at the deployment the active profile already records — what
// `blocks login --write-env` leaves in its own project — redirects nothing, so
// there is nothing to report.
func TestProfileListPrintsNoNoteWhenTheOverrideMatchesTheProfile(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://blocks.acme.com/\n")
	resolveCLIContext(t, profileListCmd)

	out := captureStdout(func() {
		if err := profileListCmd.RunE(profileListCmd, nil); err != nil {
			t.Fatalf("profile list: %v", err)
		}
	})

	if strings.Contains(out, "Note:") {
		t.Errorf("an override naming the profile's own deployment must not be reported:\n%s", out)
	}
}

// whoami is the other place a user checks who and where they are, and it reports
// the stored profile. It must say when that is not where requests go.
func TestWhoamiReportsAnActiveBackendOverride(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.com",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://acme.blocks.ai\n")
	resolveCLIContext(t, whoamiCmd)

	out := captureStdout(func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami: %v", err)
		}
	})

	if !strings.Contains(out, "Profile:  acme") {
		t.Errorf("whoami should still report the stored profile:\n%s", out)
	}
	want := "  Note: BLOCKS_BACKEND_URL in ./.env overrides this → https://acme.blocks.ai\n"
	if !strings.Contains(out, want) {
		t.Errorf("whoami output:\n%s\nmissing:\n%s", out, want)
	}
}

// The same blind spot exists for a script, so --json names the override too. The
// field appears only while one is in force.
func TestWhoamiJSONNamesTheOverriddenBackend(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL:      "https://blocks.acme.com",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_acme"}},
	})
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://acme.blocks.ai\n")
	resolveCLIContext(t, whoamiCmd)

	if err := whoamiCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	t.Cleanup(func() { whoamiCmd.Flags().Set("json", "false") })

	out := captureStdout(func() {
		if err := runWhoami(whoamiCmd, nil); err != nil {
			t.Fatalf("whoami --json: %v", err)
		}
	})

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("whoami --json is not valid JSON (%v):\n%s", err, out)
	}
	if got["backend_url_override"] != "https://acme.blocks.ai" {
		t.Errorf("backend_url_override = %v, want https://acme.blocks.ai", got["backend_url_override"])
	}
}

// Switching profiles while an override is in force does not change where commands
// go, which is exactly when someone would assume it had.
func TestProfileUseReportsAnActiveBackendOverride(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://acme.blocks.ai\n")
	resolveCLIContext(t, profileUseCmd)

	out := captureStdout(func() {
		if err := profileUseCmd.RunE(profileUseCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile use acme: %v", err)
		}
	})

	want := "Active profile: acme\n" +
		"  Note: BLOCKS_BACKEND_URL in ./.env overrides this → https://acme.blocks.ai\n"
	if out != want {
		t.Errorf("profile use output:\n%q\nwant:\n%q", out, want)
	}
}

// Switching TO the profile the override names must not report an override: the
// note has to describe the profile just selected, not the one selected before.
func TestProfileUseIsSilentWhenTheSelectedProfileIsTheTarget(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	// blocks-network is active, so before the switch the pin does displace it.
	if _, _, err := profiles.SetActive(profiles.DefaultProfile); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://blocks.acme.com\n")
	resolveCLIContext(t, profileUseCmd)

	out := captureStdout(func() {
		if err := profileUseCmd.RunE(profileUseCmd, []string{"acme"}); err != nil {
			t.Fatalf("profile use acme: %v", err)
		}
	})

	if out != "Active profile: acme\n" {
		t.Errorf("profile use output:\n%q\nwant only the active-profile line", out)
	}
}

// The supported headless mechanism must keep working end to end: an ambient
// backend URL and API key alone resolve a target and a credential with no profile
// for that deployment, and the note describes exactly that target.
func TestAmbientBackendAndKeyStillDriveHeadlessOperation(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	pinnedProjectDir(t, "BLOCKS_BACKEND_URL=https://headless.blocks.example\nBLOCKS_API_KEY=bk_headless\n")
	resolveCLIContext(t, profileListCmd)

	if got := clictx.BackendURL(); got != "https://headless.blocks.example" {
		t.Errorf("BackendURL() = %q, want the ambient backend URL", got)
	}
	cred := clictx.EffectiveCredential()
	if cred.Err != nil || cred.Key != "bk_headless" || cred.Source != clictx.SourceEnv {
		t.Errorf("credential = %+v, want the ambient key from the environment", cred)
	}
	if got, want := backendOverrideNote(), "BLOCKS_BACKEND_URL in ./.env overrides this → https://headless.blocks.example"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

// The note exists to contradict the profile line printed directly above it, so a
// backend URL carrying an erase-line sequence — anything at all can be exported into
// an environment variable — must not be able to wipe that line out or hide where
// requests are really going. The rendered bytes are the assertion: the escape has to
// be gone from the output, and visible as text.
func TestOverrideNoteEscapesAnInjectedBackendURL(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	seedProfile(t, "acme", profiles.Profile{BaseURL: "https://blocks.acme.com", Orgs: map[string]profiles.OrgKey{}})
	t.Chdir(t.TempDir())
	loadProjectEnv(t, blocksBackendURLEnv)
	t.Setenv(blocksBackendURLEnv, "https://acme.example.com\x1b[2K\rhttps://evil.example.test")
	resolveCLIContext(t, profileListCmd)

	line := backendOverrideNoteLine("  ")
	if strings.ContainsAny(line, "\x1b\r") {
		t.Fatalf("the note carried a raw control sequence to the terminal: %q", line)
	}
	if want := `https://acme.example.com\x1b[2K\x0dhttps://evil.example.test`; !strings.Contains(line, want) {
		t.Errorf("note = %q, want it to show the escaped URL (%s) so the tampering is visible", line, want)
	}
	// The variable name is the CLI's own text and is left as it is.
	if !strings.HasPrefix(line, "  Note: "+blocksBackendURLEnv+" ") {
		t.Errorf("note = %q, want it to still read as the ordinary override note", line)
	}
}

// Provenance is only claimed when it is known: a value exported in the shell was
// never seen in a .env, so the note names no source rather than inventing one.
func TestOverrideNoteOmitsTheSourceForAShellExportedValue(t *testing.T) {
	restoreCLIState(t)
	withTempProfiles(t)
	t.Chdir(t.TempDir())
	loadProjectEnv(t, blocksBackendURLEnv)
	t.Setenv(blocksBackendURLEnv, "https://exported.blocks.example")
	resolveCLIContext(t, profileListCmd)

	got := backendOverrideNote()
	if want := "BLOCKS_BACKEND_URL overrides this → https://exported.blocks.example"; got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}
