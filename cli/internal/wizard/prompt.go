package wizard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// noInputMode tracks whether --no-input is set. When true, InteractiveSelect
// must fail with an actionable error instead of prompting.
var noInputMode bool

// SetNoInputMode is called from cmd/root.go to propagate the --no-input flag
// state to the wizard package.
func SetNoInputMode(enabled bool) {
	noInputMode = enabled
}

// readRequiredLine prompts until the answer passes validate. Unlike readLine
// there is no default to fall back on, so a blank answer re-prompts and EOF is
// an error rather than a silent accept — a value the caller cannot proceed
// without must never be invented, and a loop that keeps re-prompting a closed
// stdin would spin. flagHint names what supplies the value without a terminal,
// so both the --no-input refusal and the EOF error say how to answer.
//
// It lives beside the --no-input gate so every prompt that reads a line is
// gated by construction; a call site that reads stdin directly is the hole this
// primitive exists to close.
func readRequiredLine(r *bufio.Reader, label, flagHint, helpText string, validate func(string) error) (string, error) {
	if noInputMode {
		return "", fmt.Errorf("cannot ask %q with --no-input — pass %s", label, flagHint)
	}
	for {
		fmt.Printf("%s (? for help): ", label)
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("%s is required — pass %s", label, flagHint)
			}
			return "", err
		}
		answer := strings.TrimSpace(line)
		switch {
		case answer == "?":
			fmt.Println(helpText)
			fmt.Println()
		case answer == "":
			fmt.Printf("  %s is required.\n", label)
		default:
			if err := validate(answer); err != nil {
				fmt.Printf("  Invalid: %v\n", err)
				continue
			}
			return answer, nil
		}
	}
}

// readKey reads a single keypress from stdin (must be in raw mode).
func readKey() (string, error) {
	buf := make([]byte, 1)
	if _, err := os.Stdin.Read(buf); err != nil {
		return "", err
	}

	switch buf[0] {
	case '\r', '\n':
		return "enter", nil
	case ' ':
		return "space", nil
	case '?':
		return "?", nil
	case 3: // Ctrl+C
		return "ctrlc", nil
	case 0x1b: // Escape — a lone Esc, or the start of an escape sequence
		// A blocking second read cannot tell the two apart until more input
		// arrives, which parks the picker on a lone Esc until the user's
		// next keypress — and that key is consumed as the disambiguator.
		// Poll briefly instead: an arrow burst is already buffered and polls
		// ready at once, while a lone Esc resolves within the same window
		// the autocomplete decoder uses (escTimeout) for this ambiguity.
		if !stdinReadableWithin(escTimeout) {
			return "esc", nil
		}
		// Input followed the ESC, so this is a sequence, never a lone Esc.
		// Unrecognized ones — left/right arrows, Home/End, Delete,
		// Alt-modified keys — return the ignored key rather than "esc":
		// Esc now cancels pickers, and reporting a left arrow as Esc would
		// cancel on a navigation keypress that predates and never meant
		// cancellation.
		if _, err := os.Stdin.Read(buf); err != nil || buf[0] != '[' {
			return "", nil
		}
		if _, err := os.Stdin.Read(buf); err != nil {
			return "", nil
		}
		switch buf[0] {
		case 'A':
			return "up", nil
		case 'B':
			return "down", nil
		}
		return "", nil
	}

	return string(buf[0]), nil
}

// ansiEscapes matches CSI sequences (ESC [ params final-byte), including
// private-parameter forms like \x1b[?25l, the only escape forms the
// renderers emit. Row counting needs visible widths, not byte counts.
var ansiEscapes = regexp.MustCompile("\x1b\\[[0-9;:?]*[a-zA-Z]")

// visibleLen returns the terminal-cell width of s with ANSI escapes removed —
// what physicalLines needs, since the renderers embed color codes and
// non-ASCII glyphs. Cell width, not byte or rune count: an East Asian Wide
// character like 組 occupies two cells per Unicode TR11 (UAX #11), a
// combining mark occupies zero, and a byte count would inflate every
// multi-byte glyph. Counting wrong in either direction corrupts the
// cursor-up math — under-counting leaves residue below, over-counting erases
// the line above the block.
func visibleLen(s string) int {
	return runewidth.StringWidth(ansiEscapes.ReplaceAllString(s, ""))
}

// physicalLines returns the number of terminal rows a string of the given
// visible character count occupies on a terminal of the given width.
func physicalLines(visibleLen, termWidth int) int {
	if visibleLen <= 0 || termWidth <= 0 {
		return 1
	}
	return (visibleLen + termWidth - 1) / termWidth
}

// defaultTermWidth is the fallback column count when the terminal size cannot
// be read (non-TTY, or a kernel that answers without one). Both renderers
// share it so their wrap math cannot drift apart.
const defaultTermWidth = 80

// terminalWidth reads the column count for fd, falling back to
// defaultTermWidth when it cannot.
func terminalWidth(fd int) int {
	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return defaultTermWidth
	}
	return width
}

// InteractiveSelect shows a single-select list navigable with arrow keys.
// Returns the index of the selected option. helpText is printed when the user
// presses ?. Esc restores the terminal and returns ErrCanceled — the same
// cancel the autocomplete prompt has always offered, so every picker can be
// backed out of without killing the process.
func InteractiveSelect(prompt string, options []string, defaultIdx int, helpText string) (int, error) {
	if noInputMode {
		return 0, fmt.Errorf("cannot ask %q with --no-input", prompt)
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return defaultIdx, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return defaultIdx, nil
	}

	cursor := defaultIdx

	// Get terminal width so we can account for line wrapping.
	width := terminalWidth(fd)

	// Calculate total physical rows used by the rendered block,
	// accounting for lines that wrap at the terminal width. Widths are
	// display cells (visibleLen strips the color codes and counts East Asian
	// Wide glyphs as two cells), not bytes — the arrows in the hint are
	// multi-byte.
	hint := "(\xe2\x86\x91\xe2\x86\x93 navigate, enter select, esc cancel, ? help)"
	totalRows := physicalLines(visibleLen(prompt)+1+visibleLen(hint), width) // +1 for the space
	for _, opt := range options {
		totalRows += physicalLines(4+visibleLen(opt), width) // "  > " or "    " = 4 chars
	}

	// Hide cursor during rendering.
	fmt.Fprintf(os.Stdout, "\x1b[?25l")

	render := func() {
		// Clear from cursor to end of screen to remove wrapped-line residue.
		fmt.Fprintf(os.Stdout, "\r\x1b[J")
		fmt.Fprintf(os.Stdout, "\x1b[1m%s\x1b[0m \x1b[2m%s\x1b[0m\r\n", prompt, hint)
		for i, opt := range options {
			if i == cursor {
				fmt.Fprintf(os.Stdout, "  \x1b[36m> %s\x1b[0m\r\n", opt)
			} else {
				fmt.Fprintf(os.Stdout, "    %s\r\n", opt)
			}
		}
		// Move cursor back to the top of the rendered block.
		fmt.Fprintf(os.Stdout, "\x1b[%dA", totalRows)
	}

	cleanup := func(selected string) {
		fmt.Fprintf(os.Stdout, "\r\x1b[J\x1b[32m+\x1b[0m \x1b[1m%s\x1b[0m: \x1b[36m%s\x1b[0m\r\n", prompt, selected)
		fmt.Fprintf(os.Stdout, "\x1b[?25h")
		term.Restore(fd, oldState)
	}

	render()

	for {
		key, err := readKey()
		if err != nil {
			fmt.Fprintf(os.Stdout, "\x1b[?25h")
			term.Restore(fd, oldState)
			return cursor, err
		}

		switch key {
		case "up":
			if cursor > 0 {
				cursor--
			}
		case "down":
			if cursor < len(options)-1 {
				cursor++
			}
		case "?":
			// Print help below the menu and re-render.
			// In raw mode \n doesn't return to column 0, so replace with \r\n.
			fmt.Fprintf(os.Stdout, "\r\x1b[J")
			fmt.Fprintf(os.Stdout, "%s\r\n\r\n", strings.ReplaceAll(helpText, "\n", "\r\n"))
		case "enter":
			cleanup(options[cursor])
			return cursor, nil
		case "esc":
			fmt.Fprintf(os.Stdout, "\r\n\x1b[J\x1b[?25h")
			term.Restore(fd, oldState)
			return 0, ErrCanceled
		case "ctrlc":
			fmt.Fprintf(os.Stdout, "\r\n\x1b[J\x1b[?25h")
			term.Restore(fd, oldState)
			os.Exit(1)
		}

		render()
	}
}
