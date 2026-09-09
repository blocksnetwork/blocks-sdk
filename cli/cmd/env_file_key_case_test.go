package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// The provenance record is the whole of the evidence both pin declines act on, so the
// name it is filed under has to be the name every consumer asks for. Consumers ask
// with the canonical constant (blocksBackendURLEnv, cdm.URLEnv); a project file writes
// whatever spelling it likes; and on Windows those are the same variable, because the
// process environment is case-insensitive there. A record keyed by the file's spelling
// therefore answered "not from a file" for a value a file had just supplied, which is
// the one answer that makes a decline gate return early.

// TestALowercaseSpellingCarriesProvenanceForTheCanonicalName drives the real loader
// over a real file, because the defect is entirely in what the loader files the record
// under — populating the map by hand would assert the fix against itself.
func TestALowercaseSpellingCarriesProvenanceForTheCanonicalName(t *testing.T) {
	lower := strings.ToLower(blocksBackendURLEnv)

	note := projectEnvWithoutImports(t, lower+"=https://backend.example.test\n", lower, blocksBackendURLEnv)

	if src := envFileSource(blocksBackendURLEnv); src != "./.env" {
		t.Errorf("envFileSource(%s) = %q, want the file that supplied %s recorded under the canonical name",
			blocksBackendURLEnv, src, lower)
	}
	// The variable is one the CLI accepts from a project file, and an odd spelling of it
	// is a mistake rather than something to explain, so nothing should be written here.
	if note != "" {
		t.Errorf("note = %q, want silence: the line was imported, only its provenance is at issue", note)
	}
}

// TestALowercaseBackendPinIsStillDeclined runs the real gate over the record the real
// loader produced from a real file.
//
// This is the **Windows** case, and it is now modelled honestly. Two things a Unix test
// cannot reproduce are supplied explicitly: the loader's os.Setenv of the lowercase
// spelling is what makes os.Getenv(BLOCKS_BACKEND_URL) return the value there, so the
// canonical name is set after the load to stand in for that lookup; and the environment
// folds case there, so envNamesAreCaseInsensitive is flipped to say so. Without the
// second, this test asserted Windows behaviour while running Unix semantics, and the
// pin was declined for the wrong reason — provenance recorded under a canonical name the
// file had not actually supplied. Everything the gate reads beyond those two is real.
//
// The Unix counterpart, where the two spellings are genuinely different variables and a
// shell export must survive, is TestAShellExportSurvivesALowercaseFileSpellingOnUnix.
func TestALowercaseBackendPinIsStillDeclined(t *testing.T) {
	restoreCLIState(t)
	origFold := envNamesAreCaseInsensitive
	envNamesAreCaseInsensitive = true
	t.Cleanup(func() { envNamesAreCaseInsensitive = origFold })
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	hostile := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	trusted := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	seedProfile(t, "acme", profiles.Profile{
		BaseURL: trusted.url,
		Orgs:    map[string]profiles.OrgKey{},
	})

	lower := strings.ToLower(blocksBackendURLEnv)
	t.Chdir(writeProjectEnv(t, t.TempDir(), lower+"="+hostile.url+"\n"))
	loadProjectEnv(t, lower, blocksBackendURLEnv, blocksAPIKeyEnv)
	if got := os.Getenv(lower); got != hostile.url {
		t.Fatalf("premise: the .env must supply %s=%q, got %q", lower, hostile.url, got)
	}
	t.Setenv(blocksBackendURLEnv, hostile.url)

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_live_secret", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if len(hostile.requests) != 0 {
		t.Errorf("the deployment the project .env named received %v", hostile.requests)
	}
	for _, sent := range hostile.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}
	if got, ok := os.LookupEnv(blocksBackendURLEnv); ok {
		t.Errorf("%s = %q, want a pin a project file supplied in any spelling declined", blocksBackendURLEnv, got)
	}
	if len(trusted.requests) == 0 {
		t.Error("the command reached no deployment at all; declining a pin must fall back to the profile's own")
	}
	if !strings.Contains(out, hostile.url) {
		t.Errorf("the decline must name the value it dropped:\n%s", out)
	}
}

// The allowlist, the note list and the provenance record must agree about what a
// variable is called, whichever spelling arrives: they are three answers about one
// line, and a line the CLI does not accept must not reach its process under any
// spelling — nor go unexplained when it is one a developer might have set on purpose.
func TestTheProjectEnvRulesAgreeOnSpelling(t *testing.T) {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "Https_Proxy", "XDG_CONFIG_HOME", "xdg_config_home"} {
		t.Run(key, func(t *testing.T) {
			if cliAcceptsFromProjectEnv(key) {
				t.Fatalf("cliAcceptsFromProjectEnv(%q) = true, want the allowlist to be spelling-agnostic", key)
			}
			if !worthExplainingWithheld(key) {
				t.Fatalf("worthExplainingWithheld(%q) = false, want the note list to be spelling-agnostic", key)
			}
			note := projectEnvWithoutImports(t, key+"=from-the-project-file\n",
				key, strings.ToUpper(key), strings.ToLower(key))
			for _, spelling := range []string{key, strings.ToUpper(key), strings.ToLower(key)} {
				if got := os.Getenv(spelling); got != "" {
					t.Errorf("%s = %q, want a withheld line to reach this process under no spelling", spelling, got)
				}
			}
			if !strings.Contains(note, key) {
				t.Errorf("note %q does not name the variable it withheld", note)
			}
			// Provenance is recorded under the canonical name even though the value never
			// entered this process, because the delegated agent still receives it and the
			// record is what a withdrawal deletes.
			if src := envFileSource(key); src != "./.env" {
				t.Errorf("envFileSource(%q) = %q, want the file that supplied it recorded", key, src)
			}
		})
	}
}

// The Unix case, and the defect the platform check closes. `blocks_backend_url` and
// `BLOCKS_BACKEND_URL` are two different variables here, so a file assigning the
// lowercase one supplied nothing the CLI reads — yet provenance is filed under the
// canonical name so Windows consumers can find it, and the decline read that as "the
// file supplied this".
//
// The consequence was not a harmless extra decline. dropProjectEnvValue unsets the
// canonical variable, so the shell's own export — the supported headless mechanism,
// meant to be authoritative — was discarded, the command silently retargeted to
// whatever the next tier named, and the note blamed the project file for a value it
// never supplied.
func TestAShellExportSurvivesALowercaseFileSpellingOnUnix(t *testing.T) {
	restoreCLIState(t)
	origFold := envNamesAreCaseInsensitive
	envNamesAreCaseInsensitive = false // Unix semantics, explicitly
	t.Cleanup(func() { envNamesAreCaseInsensitive = origFold })

	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	// The exported deployment is deliberately one no profile describes: that is the only
	// state in which the decline gate does any work, so a profile vouching for it would
	// short-circuit the gate and the test would pass against the defect.
	exported := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	unrelated := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	seedProfile(t, "acme", profiles.Profile{BaseURL: unrelated.url, Orgs: map[string]profiles.OrgKey{}})

	// The file names a different deployment under a spelling this platform does not read.
	lower := strings.ToLower(blocksBackendURLEnv)
	other := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	t.Chdir(writeProjectEnv(t, t.TempDir(), lower+"="+other.url+"\n"))
	loadProjectEnv(t, lower, blocksBackendURLEnv, blocksAPIKeyEnv)

	// The shell exports the canonical variable, naming a deployment a profile describes.
	t.Setenv(blocksBackendURLEnv, exported.url)

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_live_secret", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if got := os.Getenv(blocksBackendURLEnv); got != exported.url {
		t.Errorf("%s = %q, want the shell's own export preserved", blocksBackendURLEnv, got)
	}
	if len(exported.requests) == 0 {
		t.Error("the command must reach the deployment the shell export named")
	}
	if len(other.requests) != 0 {
		t.Errorf("the deployment only the lowercase file spelling named received %v", other.requests)
	}
}

// The bypass the per-spelling record closes. A project file can assign both spellings:
//
//	BLOCKS_BACKEND_URL=https://collector.example
//	blocks_backend_url=x
//
// On Unix those are two distinct process variables, but they fold to one canonical
// provenance key. While only the last spelling was recorded, the trailing lowercase line
// replaced the record for the still-live uppercase value, so both decline gates read a
// hostile file-supplied pin as shell-exported and honoured it — carrying a flag- or
// stdin-supplied key to whatever the file named. The order matters, so the hostile line
// comes first exactly as an attacker would write it.
func TestATrailingLowercaseSpellingCannotHideAHostileUppercasePin(t *testing.T) {
	restoreCLIState(t)
	origFold := envNamesAreCaseInsensitive
	envNamesAreCaseInsensitive = false // Unix: the two really are different variables
	t.Cleanup(func() { envNamesAreCaseInsensitive = origFold })

	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	resetUnregisterFlags(t)

	hostile := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	trusted := newRecordingDeployment(t, `{"agentName":"my_agent","status":"deleted"}`)
	seedProfile(t, "acme", profiles.Profile{BaseURL: trusted.url, Orgs: map[string]profiles.OrgKey{}})

	lower := strings.ToLower(blocksBackendURLEnv)
	t.Chdir(writeProjectEnv(t, t.TempDir(),
		blocksBackendURLEnv+"="+hostile.url+"\n"+lower+"=x\n"))
	loadProjectEnv(t, lower, blocksBackendURLEnv, blocksAPIKeyEnv)

	if got := os.Getenv(blocksBackendURLEnv); got != hostile.url {
		t.Fatalf("premise: the file must have supplied %s=%q, got %q", blocksBackendURLEnv, hostile.url, got)
	}

	out, err := runRootCapturing(t, "unregister", "my_agent", "--api-key", "bk_live_secret", "--yes")
	if err != nil {
		t.Fatalf("unregister: %v\n%s", err, out)
	}

	if len(hostile.requests) != 0 {
		t.Errorf("the deployment the project file named received %v", hostile.requests)
	}
	for _, sent := range hostile.authorizations {
		if sent != "" {
			t.Errorf("a credential was transmitted to a host the user never named: %q", sent)
		}
	}
	if len(trusted.requests) == 0 {
		t.Error("declining the pin must fall back to the profile's own deployment")
	}
}
