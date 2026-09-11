package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// Every failing command exits through this printer, and much of what it prints is text
// the CLI did not author: internal/blocksapi's APIError quotes the backend's message
// and code verbatim, and the OAuth and registry paths quote response bodies. So the
// printer itself has to render inertly — a control sequence that reaches the terminal
// here can erase or redraw the lines around it, and a bidi override can make the error
// read as something other than what it says.
func TestReportFatalPrintsAnErrorInertly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "an erase-line sequence",
			message: "HTTP 403: forbidden\x1b[2K\rall good, published",
			want:    `HTTP 403: forbidden\x1b[2K\x0dall good, published`,
		},
		{
			name:    "a right-to-left override",
			message: "HTTP 403: \u202edetsilbup",
			want:    `HTTP 403: \u202edetsilbup`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			reportFatal(&out, errors.New(tc.message))

			got := out.String()
			if !strings.Contains(got, tc.want) {
				t.Errorf("printed %q, want it to contain the escaped form %q", got, tc.want)
			}
			assertInert(t, got)
		})
	}
}

// Several messages are already escaped where they are built, so the boundary must not
// escape them a second time — a doubly escaped message is unreadable, which is its own
// way of hiding what went wrong. termsafe.Text produces nothing it would escape again,
// and this pins that the boundary relies on exactly that.
func TestReportFatalDoesNotEscapeAnAlreadyEscapedMessage(t *testing.T) {
	atSource := termsafe.Text("HTTP 500: Acme\x1b[2K\rEvil Corp")

	var out bytes.Buffer
	reportFatal(&out, errors.New(atSource))

	if want := "Error: " + atSource + "\n"; out.String() != want {
		t.Errorf("printed %q, want %q — an escaped message must pass through unchanged", out.String(), want)
	}
}

// assertInert fails when the printed text still holds a byte a terminal would act on
// rather than display.
func assertInert(t *testing.T, printed string) {
	t.Helper()
	for _, r := range strings.TrimSuffix(printed, "\n") {
		// Newline and tab are the two control characters the boundary keeps, because a
		// message is not a name: around ten CLI errors put their remedy on a second
		// line, and cobra indents its suggestion list with tabs — escaping either
		// rendered them as literal \x0a / \x09. Both are whitespace a terminal displays
		// rather than acts on, so what remains inert is the part that matters — nothing
		// printed can erase, reposition or reorder anything.
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			t.Errorf("printed text still holds control character %U: %q", r, printed)
		}
		if (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("printed text still holds bidi formatting character %U: %q", r, printed)
		}
	}
}

// Multi-line errors are the common shape, not the exception: the profile-not-found hint,
// the --no-input refusals and the unreachable-instance advice all put their remedy on a
// second line. Nothing pinned that, so escaping the break went unnoticed — it rendered
// `...not found\x0aRun 'blocks profile list'...`, one flattened line, least readable
// exactly where the user needs to act.
func TestReportFatalKeepsAMultiLineRemedyOnItsOwnLine(t *testing.T) {
	var out bytes.Buffer
	reportFatal(&out, errors.New("profile \"prod-typo\" not found\nRun 'blocks profile list' to see which profiles exist"))

	got := out.String()
	if strings.Contains(got, `\x0a`) {
		t.Errorf("the line break was escaped: %q", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("printed %d lines, want 2: %q", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "Error: profile ") {
		t.Errorf("first line = %q, want the failure", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Run 'blocks profile list'") {
		t.Errorf("second line = %q, want the remedy on its own line", lines[1])
	}
	// The rest of the invariant is unchanged: nothing that erases or repositions survives.
	assertInert(t, got)
}

// And a hostile value inside a multi-line message still cannot hide anything: it may add
// a line, but the sequences that erase or reposition are escaped as before.
func TestReportFatalStillDisarmsAnEscapeInsideAMultiLineMessage(t *testing.T) {
	var out bytes.Buffer
	reportFatal(&out, errors.New("first line\n\x1b[2K\x1b[1Asecond line"))

	got := out.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("an escape sequence survived: %q", got)
	}
	assertInert(t, got)
}

// Cobra's unknown-command output indents its suggestion list with tabs, and the whole
// cobra error passes through this boundary — so the tab was rendered as a literal
// `\x09` on every mistyped command. Nothing pinned that either. The tab is
// CLI-authored formatting, and it is kept.
func TestReportFatalKeepsCobraSuggestionIndentation(t *testing.T) {
	var out bytes.Buffer
	reportFatal(&out, errors.New("unknown command \"int\" for \"blocks\"\n\nDid you mean this?\n\tinit"))

	got := out.String()
	if strings.Contains(got, `\x09`) {
		t.Errorf("the suggestion's tab was escaped: %q", got)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 4 || lines[3] != "\tinit" {
		t.Errorf("the suggestion must stay tab-indented on its own line, got %q", lines)
	}
	assertInert(t, got)
}
