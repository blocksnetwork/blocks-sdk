package wizard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/term"
)

// ErrCanceled is returned by the raw-mode prompts when the user presses Esc.
var ErrCanceled = errors.New("canceled")

// debounceDelay is how long the autocomplete waits after the last keystroke
// before querying the backend, so a fast typist issues one query per pause
// rather than one per character.
const debounceDelay = 180 * time.Millisecond

// maxVisibleSuggestions caps how many suggestion rows are rendered.
const maxVisibleSuggestions = 8

// Suggestion is one autocomplete candidate. Value is what gets accepted
// (the bare agent name); Label is the human-readable annotation shown beside it.
type Suggestion struct {
	Value string
	Label string
}

// SuggestFunc returns candidate suggestions for a partial query. It is called
// off the render loop in its own goroutine; a nil/empty result or an error
// simply means "no suggestions" (the widget degrades to free-text entry).
type SuggestFunc func(ctx context.Context, query string) ([]Suggestion, error)

// --- Key decoding (pure, testable) -----------------------------------------

type acEventKind int

const (
	acIgnore acEventKind = iota
	acRune
	acBackspace
	acUp
	acDown
	acEnter
	acEsc
	acCtrlC
	acEOF
)

type acEvent struct {
	kind acEventKind
	r    rune
}

// decodeByte maps a single non-ESC raw byte to its key event. ESC (0x1b) is
// handled by decodeLoop, which needs a timeout to tell a lone Esc from the
// start of an arrow-key escape sequence.
func decodeByte(b byte) acEvent {
	switch b {
	case '\r', '\n':
		return acEvent{kind: acEnter}
	case 0x7f, 0x08: // DEL / Backspace
		return acEvent{kind: acBackspace}
	case 0x03: // Ctrl+C
		return acEvent{kind: acCtrlC}
	default:
		if b >= 0x20 && b < 0x7f {
			return acEvent{kind: acRune, r: rune(b)}
		}
		return acEvent{kind: acIgnore}
	}
}

// escTimeout is how long decodeLoop waits after an ESC byte for the rest of an
// arrow-key sequence ("ESC [ A/B", delivered as a burst by real terminals)
// before treating the ESC as a standalone Esc keypress. Without this, a lone
// Esc on a raw TTY would block until the user pressed another key, because a
// blocking read cannot tell "Esc" from "start of escape sequence".
const escTimeout = 50 * time.Millisecond

// decodeLoop assembles raw bytes from in into key events on ri.events,
// resolving the ESC ambiguity with escTimeout. It exits when in is closed.
func (ri *rawInput) decodeLoop(in <-chan byte) {
	for {
		b, ok := <-in
		if !ok {
			ri.events <- acEvent{kind: acEOF}
			return
		}
		if b != 0x1b {
			ri.events <- decodeByte(b)
			continue
		}
		// ESC: an arrow sequence continues immediately; a lone Esc does not.
		select {
		case b2, ok := <-in:
			if !ok {
				ri.events <- acEvent{kind: acEsc}
				ri.events <- acEvent{kind: acEOF}
				return
			}
			if b2 != '[' {
				ri.events <- acEvent{kind: acEsc}
				ri.events <- decodeByte(b2)
				continue
			}
			b3, ok := <-in
			if !ok {
				ri.events <- acEvent{kind: acEOF}
				return
			}
			switch b3 {
			case 'A':
				ri.events <- acEvent{kind: acUp}
			case 'B':
				ri.events <- acEvent{kind: acDown}
			default:
				ri.events <- acEvent{kind: acIgnore}
			}
		case <-time.After(escTimeout):
			ri.events <- acEvent{kind: acEsc}
		}
	}
}

// --- Editable state (pure, testable) ----------------------------------------

// acModel holds the editable autocomplete state: the typed buffer, the latest
// suggestions, and which row (if any) is highlighted. highlight == -1 means the
// caret is in the text buffer; >= 0 indexes into suggestions.
type acModel struct {
	input       []rune
	suggestions []Suggestion
	highlight   int
}

func newACModel() *acModel { return &acModel{highlight: -1} }

func (m *acModel) query() string { return string(m.input) }

func (m *acModel) insert(r rune) {
	m.input = append(m.input, r)
	m.highlight = -1
}

func (m *acModel) backspace() {
	if len(m.input) > 0 {
		m.input = m.input[:len(m.input)-1]
	}
	m.highlight = -1
}

func (m *acModel) moveDown() {
	if m.highlight < len(m.suggestions)-1 {
		m.highlight++
	}
}

func (m *acModel) moveUp() {
	if m.highlight > -1 {
		m.highlight--
	}
}

// setResults applies suggestions for query q, dropping them if the user has
// since edited the buffer (stale response). The highlight is clamped so it
// never dangles past the new list.
func (m *acModel) setResults(q string, s []Suggestion) {
	if q != m.query() {
		return
	}
	if len(s) > maxVisibleSuggestions {
		s = s[:maxVisibleSuggestions]
	}
	m.suggestions = s
	if m.highlight >= len(s) {
		m.highlight = len(s) - 1
	}
}

// selected returns the value Enter would accept: a highlighted suggestion's
// Value, or the trimmed free-text buffer otherwise.
func (m *acModel) selected() (value string, fromSuggestion bool) {
	if m.highlight >= 0 && m.highlight < len(m.suggestions) {
		return m.suggestions[m.highlight].Value, true
	}
	return strings.TrimSpace(string(m.input)), false
}

// --- Raw-mode driver --------------------------------------------------------

// rawInput owns the terminal raw-mode state and a single goroutine reading
// keypresses for the lifetime of an interactive agent-collection loop. Using
// one reader (rather than one per prompt) ensures only a single goroutine ever
// blocks on os.Stdin, so prompts never race each other for input bytes.
type rawInput struct {
	fd       int
	oldState *term.State
	events   chan acEvent
}

// newRawInput puts the terminal into raw mode and starts the key reader.
// Returns ok == false when stdin is not a terminal (callers fall back to
// line-based prompts).
func newRawInput() (*rawInput, bool) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, false
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false
	}
	ri := &rawInput{fd: fd, oldState: st, events: make(chan acEvent, 8)}
	rawBytes := make(chan byte, 16)
	go ri.byteLoop(rawBytes)
	go ri.decodeLoop(rawBytes)
	return ri, true
}

// byteLoop reads raw bytes from stdin and forwards them to out, closing out on
// read error so decodeLoop can emit acEOF and stop.
func (ri *rawInput) byteLoop(out chan<- byte) {
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			out <- buf[0]
		}
		if err != nil {
			close(out)
			return
		}
	}
}

// close restores the terminal. The reader goroutine remains parked on
// os.Stdin.Read until the next byte or process exit; the webapp wizard performs
// no further stdin reads after closing, so this is harmless.
func (ri *rawInput) close() {
	fmt.Fprint(os.Stdout, "\x1b[?25h") // ensure cursor visible
	term.Restore(ri.fd, ri.oldState)
}

func (ri *rawInput) restoreAndExit() {
	fmt.Fprint(os.Stdout, "\r\n\x1b[?25h")
	term.Restore(ri.fd, ri.oldState)
	os.Exit(1)
}

type queryResult struct {
	query string
	sugg  []Suggestion
}

// autocomplete runs the type-ahead prompt for a single value. It debounces
// keystrokes, queries suggest asynchronously, and renders a live dropdown.
// Enter accepts the highlighted suggestion or the typed text (validated via
// validate). Esc cancels (ErrCanceled); Ctrl+C exits the process.
func (ri *rawInput) autocomplete(ctx context.Context, prompt string, suggest SuggestFunc, validate func(string) error) (string, error) {
	m := newACModel()
	resultCh := make(chan queryResult, 1)
	// querySeq tags each fired query so an in-flight query whose result arrives
	// after a newer query was issued can discard itself ("latest wins").
	var querySeq atomic.Uint64
	debounce := time.NewTimer(time.Hour)
	debounce.Stop()
	errMsg := ""

	fireQuery := func() {
		q := m.query()
		if suggest == nil || len(q) < 2 {
			return
		}
		seq := querySeq.Add(1)
		go func() {
			s, err := suggest(ctx, q)
			if err != nil {
				return // silent degrade — free-text entry still works
			}
			if querySeq.Load() != seq {
				return // superseded by a newer query
			}
			// Keep only the latest completed result in the 1-slot buffer:
			// drain any older queued result, then enqueue this one, so a stale
			// queued result can never crowd out the fresh current-query result.
			select {
			case <-resultCh:
			default:
			}
			select {
			case resultCh <- queryResult{query: q, sugg: s}:
			default:
			}
		}()
	}
	schedule := func() {
		errMsg = ""
		debounce.Reset(debounceDelay)
	}

	ri.renderAutocomplete(prompt, m, errMsg)

	for {
		select {
		case ev := <-ri.events:
			switch ev.kind {
			case acRune:
				m.insert(ev.r)
				schedule()
			case acBackspace:
				m.backspace()
				schedule()
			case acUp:
				m.moveUp()
			case acDown:
				m.moveDown()
			case acEnter:
				value, _ := m.selected()
				if value == "" {
					errMsg = "Enter an agent name (type to search)."
					ri.renderAutocomplete(prompt, m, errMsg)
					continue
				}
				if validate != nil {
					if err := validate(value); err != nil {
						errMsg = err.Error()
						ri.renderAutocomplete(prompt, m, errMsg)
						continue
					}
				}
				ri.finishLine(prompt, value)
				return value, nil
			case acEsc:
				return "", ErrCanceled
			case acCtrlC:
				ri.restoreAndExit()
			case acEOF:
				return "", io.EOF
			}
			ri.renderAutocomplete(prompt, m, errMsg)
		case <-debounce.C:
			fireQuery()
		case res := <-resultCh:
			m.setResults(res.query, res.sugg)
			ri.renderAutocomplete(prompt, m, errMsg)
		}
	}
}

// confirm renders a y/n prompt reading from the shared key stream.
func (ri *rawInput) confirm(prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(os.Stdout, "%s [%s]: ", prompt, hint)
	for {
		ev := <-ri.events
		switch ev.kind {
		case acEnter:
			fmt.Fprint(os.Stdout, "\r\n")
			return def, nil
		case acRune:
			switch unicode.ToLower(ev.r) {
			case 'y':
				fmt.Fprint(os.Stdout, "y\r\n")
				return true, nil
			case 'n':
				fmt.Fprint(os.Stdout, "n\r\n")
				return false, nil
			}
		case acCtrlC:
			ri.restoreAndExit()
		case acEsc:
			return false, ErrCanceled
		case acEOF:
			return def, io.EOF
		}
	}
}

// renderAutocomplete draws the prompt, the typed buffer, the suggestion list,
// and an optional error line, leaving the cursor at the top of the block so
// the next render can clear and redraw in place.
func (ri *rawInput) renderAutocomplete(prompt string, m *acModel, errMsg string) {
	const hideCursor = "\x1b[?25l"
	fmt.Fprint(os.Stdout, hideCursor)
	// Clear from the top of the block to the end of the screen.
	fmt.Fprint(os.Stdout, "\r\x1b[J")

	lines := 1
	hint := "(type to search, \xe2\x86\x91\xe2\x86\x93 pick, enter accept, esc cancel)"
	fmt.Fprintf(os.Stdout, "\x1b[1m%s\x1b[0m \x1b[2m%s\x1b[0m\r\n", prompt, hint)

	// Input line.
	caret := ""
	if m.highlight == -1 {
		caret = "\x1b[7m \x1b[0m" // reverse-video block as a caret
	}
	fmt.Fprintf(os.Stdout, "  > %s%s\r\n", string(m.input), caret)
	lines++

	for i, s := range m.suggestions {
		label := s.Value
		if s.Label != "" && s.Label != s.Value {
			label = fmt.Sprintf("%s \x1b[2m— %s\x1b[0m", s.Value, s.Label)
		}
		if i == m.highlight {
			fmt.Fprintf(os.Stdout, "    \x1b[36m\xe2\x80\xba %s\x1b[0m\r\n", label)
		} else {
			fmt.Fprintf(os.Stdout, "      %s\r\n", label)
		}
		lines++
	}

	if errMsg != "" {
		fmt.Fprintf(os.Stdout, "  \x1b[31m%s\x1b[0m\r\n", errMsg)
		lines++
	}

	// Move the cursor back up to the top of the block.
	fmt.Fprintf(os.Stdout, "\x1b[%dA", lines)
}

// finishLine clears the live block and prints a compact accepted-value line,
// matching InteractiveSelect's confirmation style.
func (ri *rawInput) finishLine(prompt, value string) {
	fmt.Fprint(os.Stdout, "\r\x1b[J\x1b[?25h")
	fmt.Fprintf(os.Stdout, "\x1b[32m+\x1b[0m \x1b[1m%s\x1b[0m: \x1b[36m%s\x1b[0m\r\n", prompt, value)
}
