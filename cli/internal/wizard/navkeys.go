package wizard

import "unicode"

// NavKey is a paging intent read from the terminal.
type NavKey int

const (
	NavNone NavKey = iota
	NavNext
	NavPrev
	NavQuit
	NavHelp
)

// navKeyFor ignores Up/Down: terminals send them for the scroll wheel, which must scroll, not page.
func navKeyFor(ev acEvent) NavKey {
	switch ev.kind {
	case acRight:
		return NavNext
	case acLeft:
		return NavPrev
	case acEsc, acCtrlC, acEOF:
		return NavQuit
	case acRune:
		switch unicode.ToLower(ev.r) {
		case 'q':
			return NavQuit
		case '?':
			return NavHelp
		}
	}
	return NavNone
}

// NavKeys streams paging keys in raw mode until stop; ok is false without a terminal, and output needs "\r\n" meanwhile.
func NavKeys() (keys <-chan NavKey, stop func(), ok bool) {
	ri, ok := newRawInput()
	if !ok {
		return nil, nil, false
	}
	out := make(chan NavKey)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case ev := <-ri.events:
				if k := navKeyFor(ev); k != NavNone {
					select {
					case out <- k:
					case <-done:
						return
					}
				}
			case <-done:
				return
			}
		}
	}()
	return out, func() { close(done); ri.close() }, true
}
