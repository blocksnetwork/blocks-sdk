// Package termsafe renders externally-influenced text so it cannot forge terminal
// output.
//
// The invariant: every string the CLI prints that it did not author itself —
// organization and product names the backend supplies, profile names, deployment
// hosts, agent names, server error messages — passes through Text first, so no
// caller-controlled byte can reach the terminal as a control character.
//
// It exists because control characters are not inert on a terminal. A carriage
// return, a cursor-up, or an erase-line sequence embedded in an organization name
// can scroll away or overwrite the very lines a destructive command prints for the
// operator to check: the context banner naming the deployment and organization, and
// the "This cannot be undone" confirmation. Those lines are the only defence against
// removing the wrong agent from the wrong deployment, so text that can rewrite them
// defeats the check rather than merely looking odd.
//
// Explicit bidirectional formatting characters are covered for the same reason by a
// different mechanism: they reorder the glyphs around them without being control
// characters at all, so a name carrying one can make the rest of the line — including
// text the CLI wrote itself — read as something other than what it says. A check the
// operator reads is defeated as thoroughly by reordering it as by overwriting it.
//
// Escaping, not stripping: a name that arrives with an escape sequence in it is
// evidence of tampering, and silently deleting the bytes hides that while also
// making two different names render identically. The escaped form is inert, visible,
// and obviously not what a legitimate name looks like.
package termsafe

import (
	"strings"
	"unicode"
)

// Text returns s with every C0 control character, DEL, and C1 control character
// replaced by a printable \xNN escape, and every explicit bidirectional formatting
// character by a printable \uNNNN escape. Everything else is returned unchanged,
// including non-ASCII printable text: organization names are Unicode, and mangling
// them would trade one wrong-looking banner for another.
//
// Tab and newline are escaped along with the rest. They carry no meaning inside a
// name, and both can misalign a table or split a single line into two that read as
// separate CLI output.
func Text(s string) string {
	if !strings.ContainsFunc(s, needsEscape) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case isControl(r):
			writeEscape(&b, `\x`, r, 2)
		case isBidiFormatting(r):
			writeEscape(&b, `\u`, r, 4)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// needsEscape reports whether r is one of the characters Text renders inertly.
func needsEscape(r rune) bool { return isControl(r) || isBidiFormatting(r) }

// isControl reports whether r is a C0 control character, DEL, or a C1 control
// character — the ranges a terminal interprets rather than displays. unicode.IsControl
// is exactly those three ranges (U+0000–U+001F, U+007F, U+0080–U+009F), so the
// definition is not spelled out a second time here.
func isControl(r rune) bool { return unicode.IsControl(r) }

// isBidiFormatting reports whether r is one of Unicode's explicit bidirectional
// formatting characters: the embeddings and overrides U+202A–U+202E (LRE, RLE, PDF,
// LRO, RLO) and the isolates U+2066–U+2069 (LRI, RLI, FSI, PDI).
//
// Exactly those nine, and no other Cf character. They are the ones that open a scope
// which reorders a run of the text after them, so a single character inside a name can
// reverse the words of the line it appears in — a name reading as another agent's, a
// "cannot be undone" confirmation reading as its opposite. The set includes the two
// terminators (PDF, PDI) because a name that only closes a scope it never opened can
// reorder the CLI's own words that follow it just as effectively.
//
// Deliberately not escaped: the direction marks LRM, RLM and ALM (U+200E, U+200F,
// U+061C), which set the direction of a neighbouring neutral character — a bracket or
// a full stop can move, but no run of text is reordered; the zero-width joiners and
// non-joiners, which shape scripts that need them; and the invisibles like ZWSP and
// SHY, which hide a boundary rather than lie about an order. Escaping any of those
// would break legitimate names in scripts that need them for a much weaker gain. And
// note what makes right-to-left text itself safe: Arabic and Hebrew letters carry
// their direction as a property of the character, so an ordinary RTL name never needs
// one of these nine and renders untouched.
func isBidiFormatting(r rune) bool {
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

// writeEscape appends prefix followed by r's code point in width lowercase hex digits.
// Two widths rather than one: a control character has always rendered as \xNN and
// changing that would rewrite output the CLI already prints, while a bidi character does
// not fit in two digits and reads as the \uNNNN it is written as everywhere else.
func writeEscape(b *strings.Builder, prefix string, r rune, width int) {
	const hex = "0123456789abcdef"
	b.WriteString(prefix)
	for shift := (width - 1) * 4; shift >= 0; shift -= 4 {
		b.WriteByte(hex[(r>>shift)&0xf])
	}
}

// Message is Text for a whole message rather than a single value: it keeps line
// breaks and indentation tabs, and escapes everything else.
//
// Text escapes newline and tab along with the rest, which is right for a name and
// wrong for a message. Around ten CLI errors put their remedy on a second line — the
// profile-not-found hint, the --no-input refusals, the unreachable-instance advice —
// and routing those through Text rendered them as one flattened line carrying a
// literal `\x0a`, least readable exactly where the user most needs to read it.
// Cobra's unknown-command output indents its suggestion list with tabs, and the same
// boundary rendered those as `\x09` on every mistyped command — the same defect one
// character class over.
//
// Only newline and tab are kept. Both are whitespace a terminal displays rather than
// acts on: text that reaches a message without having been escaped where it was
// interpolated can add a line or shift a column, but it cannot erase, overwrite or
// reorder anything. Carriage return, cursor movement and erase sequences are still
// escaped, so the class Text exists to stop is unaffected. The residue — a
// backend-authored tab can misalign a table — is real and much weaker than
// unreadable CLI formatting on every error, which is the alternative.
func Message(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		segments := strings.Split(line, "\t")
		for j, seg := range segments {
			segments[j] = Text(seg)
		}
		lines[i] = strings.Join(segments, "\t")
	}
	return strings.Join(lines, "\n")
}
