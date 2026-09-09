package termsafe

import (
	"slices"
	"strings"
	"testing"
)

// bidiOverrides are the nine explicit bidirectional formatting characters, listed here
// rather than asked of the package: a test that checks its subject with its subject's own
// predicate agrees with it by construction, including when the predicate is wrong.
var bidiOverrides = []rune{0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069}

// reordersItsLine reports whether r can change the order the rest of a printed line
// reads in, or overwrite it.
func reordersItsLine(r rune) bool { return slices.Contains(bidiOverrides, r) || isControl(r) }

// The sequences an attacker-supplied name would carry: erase the line, move the
// cursor up, return to column zero, then rewrite. None may survive as raw bytes.
func TestTextNeutralizesTerminalControlSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"carriage return rewrites the line", "Acme\rHarmless Corp"},
		{"erase line", "Acme\x1b[2KHarmless Corp"},
		{"cursor up", "Acme\x1b[1A\x1b[2KHarmless Corp"},
		{"OSC window title", "Acme\x1b]0;pwned\x07"},
		{"backspace erases characters", "Acme\b\b\b\bOther"},
		{"C1 control introducer", "AcmemHarmless"},
		{"DEL", "Acme\x7f\x7fOther"},
		{"newline forges a second line", "Acme\nThis cannot be undone. (y/N): y"},
		{"tab breaks table alignment", "Acme\tOther"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.in)
			for _, r := range got {
				if isControl(r) {
					t.Fatalf("Text(%q) = %q still carries the control character %U", tc.in, got, r)
				}
			}
			if !strings.HasPrefix(got, "Acme") {
				t.Errorf("Text(%q) = %q, want the printable text preserved", tc.in, got)
			}
			if !strings.Contains(got, `\x`) {
				t.Errorf("Text(%q) = %q, want the removed sequence shown as an escape so tampering stays visible", tc.in, got)
			}
		})
	}
}

// Reordering a line is as good as overwriting it. Each of these is a name an operator
// would read off the safety banner or the destructive confirmation, carrying one
// explicit direction override — enough to make the words after it, the CLI's own words
// included, render in an order nobody wrote. The characters are written as \u escapes
// here on purpose: a test file that carried them literally would be reordered itself.
func TestTextNeutralizesBidiReordering(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"right-to-left override reverses what follows", "Acme\u202egro suogolinam"},
		{"right-to-left isolate reverses what follows", "Acme\u2067gro suogolinam"},
		{"left-to-right override", "Acme\u202dOther"},
		{"left-to-right embedding", "Acme\u202aOther"},
		{"embedding without its terminator", "Acme\u202bOther"},
		{"a terminator alone reorders the CLI's own words after it", "Acme\u202c"},
		{"first-strong isolate", "Acme\u2068Other"},
		{"left-to-right isolate", "Acme\u2066Other"},
		{"pop directional isolate alone", "Acme\u2069"},
		{"override hidden among control characters", "Acme\r\u202eOther"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.in)
			for _, r := range got {
				if reordersItsLine(r) {
					t.Fatalf("Text(%q) = %q still carries %U, which reorders the line it is printed on", tc.in, got, r)
				}
			}
			if !strings.HasPrefix(got, "Acme") {
				t.Errorf("Text(%q) = %q, want the printable text preserved", tc.in, got)
			}
			if !strings.Contains(got, `\u`) {
				t.Errorf("Text(%q) = %q, want the character shown as an escape so tampering stays visible", tc.in, got)
			}
		})
	}
}

// The escape has to name the character it replaced, or two names that differ only by
// which override they hide render identically and the escaping stops being evidence.
// Every one of the nine is listed, so the set cannot shrink unnoticed.
func TestTextEscapesNameTheBidiCodePoint(t *testing.T) {
	for in, want := range map[string]string{
		"\u202a": `\u202a`,
		"\u202b": `\u202b`,
		"\u202c": `\u202c`,
		"\u202d": `\u202d`,
		"\u202e": `\u202e`,
		"\u2066": `\u2066`,
		"\u2067": `\u2067`,
		"\u2068": `\u2068`,
		"\u2069": `\u2069`,
	} {
		if got := Text("Acme" + in); got != "Acme"+want {
			t.Errorf("Text(%q) = %q, want %q", in, got, "Acme"+want)
		}
	}
}

// The line between neutralizing an override and refusing a script: right-to-left text is
// right-to-left because of the letters it is made of, so a legitimate Arabic or Hebrew
// name — and the marks that place punctuation inside one — must come through untouched. A
// sanitizer that mangles them is a sanitizer users work around.
func TestTextLeavesRightToLeftScriptAlone(t *testing.T) {
	for _, in := range []string{
		"\u0634\u0631\u0643\u0629 \u0623\u0643\u0645\u064a",             // Arabic: "Acme company"
		"\u05d7\u05d1\u05e8\u05ea \u05d0\u05e7\u05de\u05d9",             // Hebrew: "Acme company"
		"Acme (\u0634\u0631\u0643\u0629 \u0623\u0643\u0645\u064a) Ltd.", // mixed direction, no override needed
		"\u200eAcme\u200f",                     // LRM and RLM place neutrals; they reorder no run
		"\u0634\u0631\u0643\u0629\u061c, Ltd.", // ALM, keeping the comma beside the Arabic word
		"\u0915\u094d\u200d\u0937",             // ZWJ, which shapes a script rather than orders it
	} {
		if got := Text(in); got != in {
			t.Errorf("Text(%q) = %q, want it unchanged", in, got)
		}
	}
}

// The two lines this package exists for, composed the way the CLI composes them, with
// the organization the backend supplied carrying an override. Character-by-character
// escaping is not the point — what has to hold is that the finished line an operator
// reads has nothing in it that can reorder the words the CLI wrote around the name.
func TestTextKeepsABannerAndAConfirmationInTheOrderTheyWereWritten(t *testing.T) {
	for _, org := range []string{
		"Acme\u202egro suogolinam",       // an override, so what follows renders reversed
		"Acme\u2066harmless org",         // an isolate, the same trick with the modern characters
		"Acme\u202egro\u202c suogolinam", // opened and closed, so the reordering is scoped
	} {
		banner := "Deployment: acme.example.com  Organization: " + Text(org)
		prompt := "Remove " + Text(org) + "/my_agent? This cannot be undone. (y/N): "
		for _, line := range []string{banner, prompt} {
			for _, r := range line {
				if reordersItsLine(r) {
					t.Errorf("the line %q carries %U, which can reorder or rewrite it", line, r)
				}
			}
			if !strings.Contains(line, "acme.example.com") && !strings.Contains(line, "my_agent") {
				t.Errorf("the line %q lost the text the operator has to read", line)
			}
		}
	}
}

// A legitimate name must survive byte for byte, Unicode included: an organization
// name is free text and may be in any script, so the sanitizer must not become a
// reason for it to render wrongly.
func TestTextLeavesPrintableTextAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"Acme Corporation",
		"acme.example.com",
		"my_agent",
		"Ünïcödé Ãgency",
		"株式会社アクメ",
		"Ωμέγα — Ltd. (EU)",
		"emoji 🚀 org",
		`already\x1bescaped`,
	} {
		if got := Text(in); got != in {
			t.Errorf("Text(%q) = %q, want it unchanged", in, got)
		}
	}
}

// Sanitized output is itself printable, so a value that passes through more than one
// print site cannot be escaped twice into something unreadable.
func TestTextIsIdempotent(t *testing.T) {
	for _, in := range []string{"Acme\x1b[2K\rOther", "Acme\u202eOther\u2069"} {
		once := Text(in)
		if twice := Text(once); twice != once {
			t.Errorf("Text(Text(%q)) = %q, want %q", in, twice, once)
		}
	}
}

// Message keeps line structure where Text does not. Text escapes newline along with the
// rest, which is right for a name and wrong for a whole message: around ten CLI errors put
// their remedy on a second line, and routing those through Text rendered one flattened
// line carrying a literal \x0a.
func TestMessageKeepsLineBreaksAndEscapesTheRest(t *testing.T) {
	in := "profile \"prod-typo\" not found\nRun 'blocks profile list' to see which profiles exist"
	got := Message(in)
	if got != in {
		t.Errorf("a message of ordinary text must survive unchanged:\n got %q\nwant %q", got, in)
	}
	if strings.Contains(got, `\x0a`) {
		t.Errorf("the line break must not be escaped, got %q", got)
	}

	// Everything Text escapes is still escaped, per line. These are the sequences that
	// erase or reposition, which is the class the escaping exists to stop.
	for _, r := range []struct{ name, in, mustNotHold string }{
		{"carriage return", "one\rtwo", "\r"},
		{"erase line", "one\x1b[2Ktwo", "\x1b"},
		{"cursor up", "one\x1b[1Atwo", "\x1b"},
		{"tab", "one\ttwo", "\t"},
		{"bidi override", "one‮two", "‮"},
	} {
		out := Message(r.in)
		if strings.Contains(out, r.mustNotHold) {
			t.Errorf("%s: Message(%q) = %q, want it escaped", r.name, r.in, out)
		}
	}

	// A newline inside an otherwise hostile run is kept while the rest is escaped, so a
	// line can be added but nothing can be hidden.
	out := Message("line one\n\x1b[2Kline two")
	if !strings.Contains(out, "\n") {
		t.Errorf("the break must survive, got %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("the escape must not, got %q", out)
	}

	// Idempotent, like Text.
	if again := Message(out); again != out {
		t.Errorf("Message is not idempotent: %q became %q", out, again)
	}
}
