package wizard

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDecodeByte(t *testing.T) {
	cases := []struct {
		name  string
		b     byte
		want  acEventKind
		wantR rune
	}{
		{"rune", 'a', acRune, 'a'},
		{"digit", '7', acRune, '7'},
		{"underscore", '_', acRune, '_'},
		{"enter-cr", '\r', acEnter, 0},
		{"enter-lf", '\n', acEnter, 0},
		{"backspace-del", 0x7f, acBackspace, 0},
		{"backspace-bs", 0x08, acBackspace, 0},
		{"ctrl-c", 0x03, acCtrlC, 0},
		{"control-byte-ignored", 0x01, acIgnore, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := decodeByte(tc.b)
			if ev.kind != tc.want {
				t.Fatalf("kind = %v, want %v", ev.kind, tc.want)
			}
			if tc.want == acRune && ev.r != tc.wantR {
				t.Errorf("rune = %q, want %q", ev.r, tc.wantR)
			}
		})
	}
}

// expectEvent reads the next event from ri.events within a generous deadline,
// failing the test on timeout (so a regression of the lone-Esc hang surfaces
// as a failure rather than a hung test).
func expectEvent(t *testing.T, ri *rawInput, want acEventKind) acEvent {
	t.Helper()
	select {
	case ev := <-ri.events:
		if ev.kind != want {
			t.Fatalf("event kind = %v, want %v", ev.kind, want)
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for event kind %v", want)
		return acEvent{}
	}
}

func TestDecodeLoop_ArrowSequence(t *testing.T) {
	ri := &rawInput{events: make(chan acEvent, 8)}
	in := make(chan byte, 8)
	go ri.decodeLoop(in)

	// A printable byte decodes immediately.
	in <- 'a'
	if ev := expectEvent(t, ri, acRune); ev.r != 'a' {
		t.Errorf("rune = %q, want 'a'", ev.r)
	}
	// An arrow sequence arrives as a burst → Up / Down.
	in <- 0x1b
	in <- '['
	in <- 'A'
	expectEvent(t, ri, acUp)
	in <- 0x1b
	in <- '['
	in <- 'B'
	expectEvent(t, ri, acDown)

	close(in)
	expectEvent(t, ri, acEOF)
}

// TestDecodeLoop_LoneEsc is the regression guard for the high-severity finding:
// a lone Esc (no following bytes, as a real raw TTY delivers it) must resolve to
// an Esc event via the timeout — not block waiting for a second keypress.
func TestDecodeLoop_LoneEsc(t *testing.T) {
	ri := &rawInput{events: make(chan acEvent, 8)}
	in := make(chan byte, 8)
	go ri.decodeLoop(in)

	in <- 0x1b // ESC and nothing else
	start := time.Now()
	expectEvent(t, ri, acEsc)
	if elapsed := time.Since(start); elapsed < escTimeout {
		t.Errorf("Esc fired after %v, expected at least escTimeout (%v)", elapsed, escTimeout)
	}

	// ESC followed by a non-'[' byte → Esc, then the trailing key decodes.
	in <- 0x1b
	in <- 'x'
	expectEvent(t, ri, acEsc)
	if ev := expectEvent(t, ri, acRune); ev.r != 'x' {
		t.Errorf("trailing rune = %q, want 'x'", ev.r)
	}
}

func TestACModel_InsertBackspaceQuery(t *testing.T) {
	m := newACModel()
	for _, r := range "trans" {
		m.insert(r)
	}
	if m.query() != "trans" {
		t.Fatalf("query = %q, want trans", m.query())
	}
	m.backspace()
	if m.query() != "tran" {
		t.Fatalf("after backspace query = %q, want tran", m.query())
	}
	// Backspace on empty is a no-op.
	m2 := newACModel()
	m2.backspace()
	if m2.query() != "" {
		t.Errorf("empty backspace changed buffer to %q", m2.query())
	}
}

func TestACModel_Navigation(t *testing.T) {
	m := newACModel()
	m.suggestions = []Suggestion{{Value: "a"}, {Value: "b"}, {Value: "c"}}
	if m.highlight != -1 {
		t.Fatalf("initial highlight = %d, want -1", m.highlight)
	}
	m.moveDown() // -1 -> 0
	m.moveDown() // 0 -> 1
	if m.highlight != 1 {
		t.Fatalf("highlight = %d, want 1", m.highlight)
	}
	m.moveDown() // 1 -> 2
	m.moveDown() // clamp at 2
	if m.highlight != 2 {
		t.Fatalf("highlight = %d, want 2 (clamped)", m.highlight)
	}
	m.moveUp() // 2 -> 1
	m.moveUp() // 1 -> 0
	m.moveUp() // 0 -> -1 (back to input)
	m.moveUp() // clamp at -1
	if m.highlight != -1 {
		t.Fatalf("highlight = %d, want -1 (clamped)", m.highlight)
	}
	// Typing resets highlight to the input buffer.
	m.suggestions = []Suggestion{{Value: "a"}}
	m.moveDown()
	m.insert('x')
	if m.highlight != -1 {
		t.Errorf("highlight after insert = %d, want -1", m.highlight)
	}
}

func TestACModel_SetResultsStaleDrop(t *testing.T) {
	m := newACModel()
	for _, r := range "tran" {
		m.insert(r)
	}
	// A response for an earlier query is dropped.
	m.setResults("tra", []Suggestion{{Value: "stale"}})
	if len(m.suggestions) != 0 {
		t.Errorf("stale results were applied: %+v", m.suggestions)
	}
	// A response for the current query is applied.
	m.setResults("tran", []Suggestion{{Value: "translator"}})
	if len(m.suggestions) != 1 || m.suggestions[0].Value != "translator" {
		t.Errorf("current results not applied: %+v", m.suggestions)
	}
}

func TestACModel_SetResultsClampsHighlight(t *testing.T) {
	m := newACModel()
	for _, r := range "x" {
		m.insert(r)
	}
	m.suggestions = []Suggestion{{Value: "a"}, {Value: "b"}, {Value: "c"}}
	m.highlight = 2
	// New, shorter result set must clamp the highlight in-range.
	m.setResults("x", []Suggestion{{Value: "a"}})
	if m.highlight != 0 {
		t.Errorf("highlight = %d, want 0 after clamp", m.highlight)
	}
	// Empty result set drives highlight back to the input buffer.
	m.setResults("x", nil)
	if m.highlight != -1 {
		t.Errorf("highlight = %d, want -1 for empty results", m.highlight)
	}
}

func TestACModel_SetResultsCapsList(t *testing.T) {
	m := newACModel()
	m.insert('q')
	big := make([]Suggestion, maxVisibleSuggestions+5)
	for i := range big {
		big[i] = Suggestion{Value: "a"}
	}
	m.setResults("q", big)
	if len(m.suggestions) != maxVisibleSuggestions {
		t.Errorf("suggestions len = %d, want %d", len(m.suggestions), maxVisibleSuggestions)
	}
}

func TestACModel_Selected(t *testing.T) {
	m := newACModel()
	for _, r := range "  spaced  " {
		m.insert(r)
	}
	// No highlight → free-text, trimmed.
	if v, fromSugg := m.selected(); v != "spaced" || fromSugg {
		t.Errorf("selected = (%q, %v), want (spaced, false)", v, fromSugg)
	}
	// Highlighted suggestion wins.
	m.suggestions = []Suggestion{{Value: "translator", Label: "Acme"}}
	m.highlight = 0
	if v, fromSugg := m.selected(); v != "translator" || !fromSugg {
		t.Errorf("selected = (%q, %v), want (translator, true)", v, fromSugg)
	}
}

// fdNoTTY is a descriptor term.GetSize always fails on, so a fixture's
// injected width survives refreshWidth.
const fdNoTTY = -1

// A transient size-read failure mid-prompt must not reset a live terminal's
// width. 37 is deliberately not defaultTermWidth, so substituting
// terminalWidth here fails.
func TestRefreshWidthKeepsInjectedWidthWhenSizeReadFails(t *testing.T) {
	ri := &rawInput{fd: fdNoTTY, width: 37}
	ri.refreshWidth()
	if ri.width != 37 {
		t.Errorf("width = %d, want 37 (a failed size read must not overwrite it)", ri.width)
	}
}

// withPipeStdout restores os.Stdout via t.Cleanup as well as inline, so a
// panic in f cannot strand the package's stdout for later tests.
func withPipeStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = old
		w.Close()
	})
	f()
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// cursorUp is the row count the renderer claims, parsed from its trailing \x1b[NA.
func cursorUp(t *testing.T, out string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(out[strings.LastIndex(out, "\x1b["):], "\x1b[%dA", &n); err != nil {
		t.Fatalf("no cursor-up suffix in %q", out)
	}
	return n
}

// Every expected count in the renderer tests below is hard-coded: deriving one
// from physicalLines or visibleLen would move the expectation with a
// regression in them.

func TestRenderAutocomplete_WrappedRowsCountedPhysically(t *testing.T) {
	ri := &rawInput{fd: fdNoTTY, width: 80}

	// 149 cells + the 2-space indent = 151: wraps to 2 rows at 80.
	longErr := `agent "transcription-pipeline-v2" not found — check the spelling (if it is a private agent your account can access, cancel with Esc and log in first)`

	m := newACModel()
	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, longErr)
	})

	if n := cursorUp(t, out); n != 4 {
		t.Errorf("cursor-up = %d rows, want 4 (prompt, input, error wrapped to 2)", n)
	}
}

func TestRenderAutocomplete_BoundarySuggestionNotOvercounted(t *testing.T) {
	// 6-cell prefix + 74 = exactly 80: shortening the value stops testing the boundary.
	const width = 80
	value := strings.Repeat("a", 74)
	m := newACModel()
	m.suggestions = []Suggestion{{Value: value}}
	m.highlight = 0
	ri := &rawInput{fd: fdNoTTY, width: width}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	if n := cursorUp(t, out); n != 3 {
		t.Errorf("cursor-up = %d rows, want 3 (boundary suggestion must be one row)", n)
	}
}

// A 4-cell prefix + 76 leaves the input row at exactly 80, so the caret is the
// one cell that decides whether it wraps.
func TestRenderAutocomplete_CaretCellCountedOnlyWhenDrawn(t *testing.T) {
	cases := []struct {
		name      string
		highlight int
		want      int
	}{
		{"suppressed by a highlighted suggestion", 0, 3},
		{"drawn while the caret is in the buffer", -1, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newACModel()
			m.input = []rune(strings.Repeat("a", 76))
			m.suggestions = []Suggestion{{Value: "agent"}}
			m.highlight = tc.highlight
			ri := &rawInput{fd: fdNoTTY, width: 80}

			out := withPipeStdout(t, func() {
				ri.renderAutocomplete("Agent name", m, "")
			})

			if n := cursorUp(t, out); n != tc.want {
				t.Errorf("cursor-up = %d rows, want %d", n, tc.want)
			}
		})
	}
}

func TestRenderAutocomplete_UnicodeLabelCountedByCell(t *testing.T) {
	// 11 wide glyphs are 22 cells, not 11: 6+60+3+22 = 91 wraps at 80, 80 would not.
	label := strings.Repeat("組", 11)
	m := newACModel()
	m.suggestions = []Suggestion{{Value: strings.Repeat("v", 60), Label: label}}
	ri := &rawInput{fd: fdNoTTY, width: 80}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	if n := cursorUp(t, out); n != 4 {
		t.Errorf("cursor-up = %d rows, want 4 (wide CJK label counted by cell)", n)
	}
}

func TestRenderAutocomplete_WrappedSuggestionCounted(t *testing.T) {
	// 6-cell prefix + 75 = 81: one cell past the boundary, so the row wraps.
	// Paired with the 80-cell case above, this is the off-by-one either side.
	m := newACModel()
	m.suggestions = []Suggestion{{Value: strings.Repeat("w", 75)}}
	m.highlight = 0
	ri := &rawInput{fd: fdNoTTY, width: 80}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	if n := cursorUp(t, out); n != 1+1+2 {
		t.Errorf("cursor-up = %d rows, want %d (prompt, input, suggestion wrapped to 2)", n, 1+1+2)
	}
}

func TestVisibleLen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"plain ascii", "hello", 5},
		{"csi color stripped", "\x1b[31mhello\x1b[0m", 5},
		{"private-mode stripped", "\x1b[?25l", 0},
		{"em dash one cell", "a—b", 3},
		{"cjk wide two cells per rune", strings.Repeat("組", 10), 20},
		{"combining mark zero cells", "e\u0301", 1}, // e + U+0301 combining acute (decomposed), 1 cell
		{"mixed arrows hint", "(↑↓ pick)", 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := visibleLen(tc.in); got != tc.want {
				t.Errorf("visibleLen(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
