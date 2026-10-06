package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// loggedInProfile is a completed login holding a key, for a deployment named by URL
// so that resolving it needs no CDM lookup.
func loggedInProfile() profiles.Profile {
	return profiles.Profile{
		BaseURL:      "https://blocks.acme.example.com",
		DefaultOrgID: "org-1",
		Orgs:         map[string]profiles.OrgKey{"org-1": {OrgName: "Acme", ApiKey: "bk_profile"}},
	}
}

// enterProject makes a project directory with the given .env the working directory
// and runs the startup .env load on it.
func enterProject(t *testing.T, env string) {
	t.Helper()
	t.Chdir(writeProjectEnv(t, t.TempDir(), env))
	loadProjectEnv(t, blocksAPIKeyEnv)
}

func storePath(t *testing.T) string {
	t.Helper()
	p, err := profiles.ContextsPathFunc()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// scaffoldedEnv is the .env 'blocks init' writes: an empty BLOCKS_API_KEY placeholder.
const scaffoldedEnv = "# Blocks API key.\n" + blocksAPIKeyEnv + "=\n"

// A user logged in to the CLI who runs a freshly scaffolded agent, whose .env carries
// an empty placeholder, gets the profile's key and is told so, and where to put a
// project key instead. Any key supplied for the invocation is the one in use, so the
// note about the profile's key stays silent.
func TestNoteStoredCredential(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		exported string
		flagKey  string
		wantNote bool
	}{
		{name: "empty scaffold placeholder", env: scaffoldedEnv, wantNote: true},
		{name: "key in .env", env: blocksAPIKeyEnv + "=bk_project\n"},
		{name: "exported BLOCKS_API_KEY", env: scaffoldedEnv, exported: "bk_shell"},
		{name: "--api-key", env: scaffoldedEnv, flagKey: "bk_flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreCLIState(t)
			isolateCredentials(t)
			defer isolateProfiles(t)()
			isolateAmbientState(t)
			resetRegisterFlags()
			t.Cleanup(resetRegisterFlags)
			seedProfile(t, profiles.DefaultProfile, loggedInProfile())
			t.Setenv(cdm.URLEnv, "")
			enterProject(t, tc.env)
			if tc.exported != "" {
				t.Setenv(blocksAPIKeyEnv, tc.exported)
			}
			if tc.flagKey != "" {
				if err := registerCmd.Flags().Set("api-key", tc.flagKey); err != nil {
					t.Fatal(err)
				}
			}
			resolveCLIContext(t, registerCmd)

			var note bytes.Buffer
			noteStoredCredential(&note)
			if !tc.wantNote {
				if note.Len() != 0 {
					t.Errorf("no note is owed when the invocation supplies the key:\n%s", note.String())
				}
				return
			}
			for _, want := range []string{
				fmt.Sprintf("CLI profile %q (%s)", profiles.DefaultProfile, storePath(t)),
				"add BLOCKS_API_KEY=<key> to .env",
			} {
				if !strings.Contains(note.String(), want) {
					t.Errorf("note is missing %q:\n%s", want, note.String())
				}
			}
			if got, _ := childEnvValue(buildChildEnv(), blocksAPIKeyEnv); got != "bk_profile" {
				t.Errorf("the agent received %s=%q, want the logged-in profile's key", blocksAPIKeyEnv, got)
			}
		})
	}
}

// The note is only worth anything if the commands print it; 'blocks run' is the one a
// freshly scaffolded agent meets first. It prints before checking for agent-card.json,
// so a project without one is enough.
func TestBlocksRunNotesTheLoggedInProfilesKey(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedProfile(t, profiles.DefaultProfile, loggedInProfile())
	enterProject(t, scaffoldedEnv)
	resolveCLIContext(t, runCmd)

	out := captureStdoutStderr(func() {
		if err := runCmd.RunE(runCmd, nil); err == nil {
			t.Error("want run to stop on the missing agent-card.json")
		}
	})

	if !strings.Contains(out, "Using the API key from CLI profile") {
		t.Errorf("blocks run must say it uses the profile's key:\n%s", out)
	}
}
