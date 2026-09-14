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

// withPipeStdout swaps os.Stdout for a pipe for the duration of the test and
// returns everything written once f has run. Restoration is deferred so a
// panic or t.Fatal inside f cannot leave the package's stdout pointed at a
// dead pipe for later tests.
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
	os.Stdout = old // restore on the normal path too, so nothing between here and Cleanup writes into a closed fd
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// cursorUp parses the trailing \x1b[NA from a rendered block — the number of
// physical rows the renderer believes it drew.
func cursorUp(t *testing.T, out string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(out[strings.LastIndex(out, "\x1b["):], "\x1b[%dA", &n); err != nil {
		t.Fatalf("no cursor-up suffix in %q", out)
	}
	return n
}

// The cursor-up count at the end of renderAutocomplete must equal the number
// of physical rows drawn — including rows where a long error, prompt, or
// suggestion wraps at the terminal width. Counting a wrapped line as one row
// lands the cursor short, so the \x1b[J clear starts too low and each redraw
// walks the prompt down the screen leaving residue.
//
// The expected count is hard-coded, not derived from physicalLines/visibleLen:
// deriving it from the helpers under test would let a regression in those
// helpers move the expectation with the implementation and stay green.
func TestRenderAutocomplete_WrappedRowsCountedPhysically(t *testing.T) {
	ri := &rawInput{width: 80}

	// The not-found messages the wizard emits are ~120-135 columns before the
	// two-space indent, so at 80 columns they wrap to two physical rows. The
	// em dash is 3 bytes but one column — this fixture also pins that the
	// counting is by column, not byte.
	longErr := `agent "transcription-pipeline-v2" not found — check the spelling (if it is a private agent your account can access, cancel with Esc and log in first)`

	m := newACModel()
	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, longErr)
	})

	// prompt+hint row, input row, error wrapping to 2 rows → 4 total.
	if n := cursorUp(t, out); n != 4 {
		t.Errorf("cursor-up = %d rows, want 4 (prompt, input, error wrapped to 2)", n)
	}
}

// A suggestion row whose visible length sits exactly at the wrap boundary
// must count as ONE row — counting its trailing \r\n or counting bytes
// instead of columns both inflate it to two and push the cursor-up above the
// block, erasing the line the previous wizard step printed.
func TestRenderAutocomplete_BoundarySuggestionNotOvercounted(t *testing.T) {
	// Width 80 keeps the prompt+hint row (~61 cols) unwrapped, isolating the
	// suggestion row. Highlighted-row prefix: 4 spaces + "› " = 6 visible
	// columns, so a 74-column value lands the row exactly at 80.
	const width = 80
	value := strings.Repeat("a", 74)
	m := newACModel()
	m.suggestions = []Suggestion{{Value: value}}
	m.highlight = 0
	ri := &rawInput{width: width}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	// prompt+hint, input, one suggestion row → 3 total. The pre-fix bugs
	// (CRLF/byte inflation) counted 4+ here.
	if n := cursorUp(t, out); n != 3 {
		t.Errorf("cursor-up = %d rows, want 3 (boundary suggestion must be one row)", n)
	}
}

// Multi-byte glyphs in a suggestion label must be measured in display CELLS,
// not bytes or runes: an East Asian Wide glyph like 組 occupies two cells
// (Unicode TR11 / UAX #11). The label below is 11 runes of 組 = 22 cells, so
// the row's true width is 6 + 60 + 3 + 22 = 91 cells — one physical row at
// width 91, two at width 80. Rune-counting would read 80 cells and fit it on
// one row, under-counting; byte-counting would read ~113.
func TestRenderAutocomplete_UnicodeLabelCountedByCell(t *testing.T) {
	label := strings.Repeat("組", 11)
	m := newACModel()
	m.suggestions = []Suggestion{{Value: strings.Repeat("v", 60), Label: label}}
	ri := &rawInput{width: 80}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	// prompt+hint, input, suggestion wrapped to 2 rows → 4 total.
	if n := cursorUp(t, out); n != 4 {
		t.Errorf("cursor-up = %d rows, want 4 (wide CJK label counted by cell)", n)
	}
}

// A long wrapped suggestion must still count all its physical rows — the
// undercount direction (the original bug) walks the prompt down the screen.
func TestRenderAutocomplete_WrappedSuggestionCounted(t *testing.T) {
	// 6-col prefix + 74-char value = 80... deliberately past the boundary:
	// value 78 cols → row = 84 visible → 2 physical rows at width 80.
	m := newACModel()
	m.suggestions = []Suggestion{{Value: strings.Repeat("w", 78)}}
	m.highlight = 0
	ri := &rawInput{width: 80}

	out := withPipeStdout(t, func() {
		ri.renderAutocomplete("Agent name", m, "")
	})

	// prompt+hint, input, suggestion wrapped to 2 → 4. Exact, not a lower
	// bound: an overcount (CRLF/byte inflation) fails here just as the
	// undercount does.
	if n := cursorUp(t, out); n != 1+1+2 {
		t.Errorf("cursor-up = %d rows, want %d (prompt, input, suggestion wrapped to 2)", n, 1+1+2)
	}
}

// visibleLen itself, pinned directly: ANSI stripping (including the private
// \x1b[?25l form) and one-cell-per-rune counting, independent of the renderer
// math that consumes it.
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
