package cmd

import (
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

// A declined value is attacker-chosen text, and the two protections it needs are
// orthogonal. termsafe.Text makes it safe to *display* — no byte of it reaches the
// terminal as a control character — and says nothing about whether it is safe to
// *run*: `;`, `&&`, backticks and $(...) are ordinary printable characters that pass
// through it unchanged. A remediation line the user is invited to copy is therefore
// the one place a declined value may not appear, because a line presented as a
// command is a line that gets pasted into a shell.
//
// These cases drive the real gates over a real project .env, because the note's text
// is produced from the value the loader recorded and the target the gate resolved;
// calling the formatter with hand-made arguments would test a different function than
// the one that runs.

// injectedBackendPin is what a hostile repository's .env would carry if the point were
// not to redirect a request but to get a command executed on the developer's machine
// when they follow the CLI's own advice.
const injectedBackendPin = "https://backend.example.test/x;$(id) && rm -rf / #`id`"

// injectedCDMPin is the same idea aimed at the other gate.
const injectedCDMPin = "https://collector.example.test/api/v1/cdm;$(id)"

// shellMetacharacters are the sequences that turn a pasted line into more than one
// command. None of them can appear on a line the CLI formats as a command.
var shellMetacharacters = []string{";", "&&", "||", "`", "$(", "|", ">"}

// runnableMarkers is what makes a line read as something to run. It is a package-level
// var, not a literal inside commandLines, because two things ask the question: the
// output scans below and the source scan in command_shaped_output_test.go, and a second
// list of markers would be a second definition of "reads as something to run".
//
// The set is deliberately generous — a line only has to *look* runnable for a user to
// paste it. Both quotings of a named command are here: a remedy worded with backticks
// reads exactly as runnably as one worded with straight quotes, and the profile-removal
// retry line used the backtick spelling, which is how it stayed out of this set through
// two earlier rounds of the same defect.
var runnableMarkers = []string{"run:", "run '", "run `", "$ ", "blocks login", "blocks publish", "blocks register", "export "}

// commandLines returns the lines of out that read as something to run: they either
// introduce a command or name one. A note that starts wording a remedy as a command in
// future is caught by these cases rather than by a report.
func commandLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		for _, marker := range runnableMarkers {
			if strings.Contains(line, marker) {
				lines = append(lines, line)
				break
			}
		}
	}
	return lines
}

// assertNoPastableInjection fails when any line that reads as a command carries the
// declined value or a shell metacharacter, and when no line reads as a command at all
// — the remedy has to still be there, or the scan would pass by saying nothing.
func assertNoPastableInjection(t *testing.T, out, value string) {
	t.Helper()
	lines := commandLines(out)
	if len(lines) == 0 {
		t.Fatalf("no line of the note names a command, so there is no remedy to check:\n%s", out)
	}
	for _, line := range lines {
		if strings.Contains(line, value) {
			t.Errorf("a line formatted as a command carries the declined value:\n%s", line)
		}
		for _, meta := range shellMetacharacters {
			if strings.Contains(line, meta) {
				t.Errorf("a line formatted as a command carries the shell metacharacter %q:\n%s", meta, line)
			}
		}
	}
}

// The declined backend pin: the remedy must name 'blocks login' and the variable
// without echoing the value, while the value itself is still reported — a decline the
// user cannot see the cause of is not actionable.
func TestTheBackendPinDeclineOffersNoPastableCommandBuiltFromTheValue(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	// No profile describes anything, so the pin is foreign and the note is emitted.
	seedProfile(t, profiles.DefaultProfile, profiles.Profile{Orgs: map[string]profiles.OrgKey{}})
	t.Chdir(writeProjectEnv(t, t.TempDir(), blocksBackendURLEnv+"="+injectedBackendPin+"\n"))
	loadProjectEnv(t, blocksBackendURLEnv, blocksAPIKeyEnv)

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}

	assertNoPastableInjection(t, out, injectedBackendPin)
	// Still displayed, and still sanitized: the two protections are independent, and
	// dropping the value from the report would trade one defect for another.
	if !strings.Contains(out, injectedBackendPin) {
		t.Errorf("the decline must still report the value it dropped:\n%s", out)
	}
	if !strings.Contains(out, "blocks login") {
		t.Errorf("the remedy must still name the command that establishes the deployment:\n%s", out)
	}
}

// The declined CDM pin, held to the same rule: it is a second gate wording a second
// remedy, and a rule applied to one note and not the other is not a rule.
func TestTheCDMPinDeclineOffersNoPastableCommandBuiltFromTheValue(t *testing.T) {
	restoreCLIState(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	seedAcmeProfile(t)
	pinnedCDMURL(t, cdm.URLEnv+"="+injectedCDMPin+"\n")

	out, err := runRootCapturing(t, "version")
	if err != nil {
		t.Fatalf("version: %v\n%s", err, out)
	}

	assertNoPastableInjection(t, out, injectedCDMPin)
	if !strings.Contains(out, injectedCDMPin) {
		t.Errorf("the decline must still report the value it dropped:\n%s", out)
	}
}

// The refusal note has no value in it at all, by construction — the variables it names
// are the file's text but their values are never echoed. This holds that property,
// because the tempting fix for "the user cannot tell which line to change" is to print
// the assignment back, and the assignment is exactly the pasteable line.
func TestTheRefusedVariableNoteOffersNoPastableAssignment(t *testing.T) {
	note := projectEnvWithoutImports(t,
		"HTTPS_PROXY=http://intercept.example.test:8080;$(id)\n",
		"HTTPS_PROXY", "https_proxy")

	if note == "" {
		t.Fatal("premise: refusing a variable must be explained")
	}
	assertNoPastableInjection(t, note, "http://intercept.example.test:8080;$(id)")
	if strings.Contains(note, "$(id)") {
		t.Errorf("the refusal note echoes the refused value:\n%s", note)
	}
}
