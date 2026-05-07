package wizard

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

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
	case 3: // Ctrl+C
		return "ctrlc", nil
	case 0x1b: // Escape — start of arrow key sequence
		if _, err := os.Stdin.Read(buf); err != nil {
			return "esc", nil
		}
		if buf[0] != '[' {
			return "esc", nil
		}
		if _, err := os.Stdin.Read(buf); err != nil {
			return "esc", nil
		}
		switch buf[0] {
		case 'A':
			return "up", nil
		case 'B':
			return "down", nil
		}
		return "esc", nil
	}

	return string(buf[0]), nil
}

// physicalLines returns the number of terminal rows a string of the given
// visible character count occupies on a terminal of the given width.
func physicalLines(visibleLen, termWidth int) int {
	if visibleLen <= 0 || termWidth <= 0 {
		return 1
	}
	return (visibleLen + termWidth - 1) / termWidth
}

// InteractiveSelect shows a single-select list navigable with arrow keys.
// Returns the index of the selected option.
func InteractiveSelect(prompt string, options []string, defaultIdx int) (int, error) {
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
	width, _, _ := term.GetSize(fd)
	if width <= 0 {
		width = 80
	}

	// Calculate total physical rows used by the rendered block,
	// accounting for lines that wrap at the terminal width.
	hint := "(arrows navigate, enter select)"
	totalRows := physicalLines(len(prompt)+1+len(hint), width) // +1 for the space
	for _, opt := range options {
		totalRows += physicalLines(4+len(opt), width) // "  > " or "    " = 4 chars
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
		case "enter":
			cleanup(options[cursor])
			return cursor, nil
		case "ctrlc":
			fmt.Fprintf(os.Stdout, "\r\n\x1b[J\x1b[?25h")
			term.Restore(fd, oldState)
			os.Exit(1)
		}

		render()
	}
}

