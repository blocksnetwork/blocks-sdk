package stdinpoll

import (
	"bytes"
	"context"
	"os"
	"time"
	"unicode/utf16"
)

// linePollInterval bounds how long a line reader waits for input before checking
// whether it should stop, and so how long stopping it can take.
const linePollInterval = 200 * time.Millisecond

// Lines delivers lines typed at the terminal until ctx ends, then closes the
// channel. It never parks in a read: the channel closes within linePollInterval
// of ctx ending, and once it has closed nothing is left reading stdin to swallow
// the answer to the next prompt. The browser login uses it to take a pasted
// redirect URL while racing the callback server.
//
// Each platform supplies it: Unix polls the descriptor, whose canonical-mode
// terminal reports readable only once a whole line is waiting; a Windows console
// is read record by record instead (see lines_windows.go).

// pollLines is Lines for a stdin whose readiness check guarantees that the read
// following it returns without waiting.
func pollLines(ctx context.Context, readable func(time.Duration) bool) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		var pending []byte
		buf := make([]byte, 4096)
		for ctx.Err() == nil {
			// Re-checked after the poll: input that arrives once the wait has ended
			// belongs to whatever reads stdin next.
			if !readable(linePollInterval) || ctx.Err() != nil {
				continue
			}
			n, err := os.Stdin.Read(buf)
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := string(pending[:i])
				pending = pending[i+1:]
				select {
				case ch <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ch
}

// consoleRecord is the part of a Windows console input record the line editor
// reads. Focus, mouse, menu and window-resize records are not key events, and a
// key release types nothing.
type consoleRecord struct {
	key    bool   // a KEY_EVENT record
	down   bool   // a key press rather than its release
	char   uint16 // the UTF-16 unit the key types; 0 for none (Shift, arrows, …)
	repeat uint16 // how many times the press repeated while held
}

// consoleLine is the line being typed at a Windows console. Reading input records
// directly bypasses the console's own line editing, so the echo and Backspace it
// would have provided are done here.
type consoleLine struct{ units []uint16 }

// feed applies records in order and stops after the one that completes the line.
// used is how many records it took: all of them unless a line completed, so the
// caller consumes exactly those and input typed after the line stays queued for
// whatever reads stdin next. echo is what the console would have shown.
func (l *consoleLine) feed(records []consoleRecord) (used int, line string, done bool, echo string) {
	var out []uint16
	for i, r := range records {
		if !r.key || !r.down || r.char == 0 {
			continue
		}
		for n := max(r.repeat, 1); n > 0; n-- {
			switch c := r.char; {
			case c == '\r' || c == '\n':
				line = string(utf16.Decode(l.units))
				l.units = l.units[:0]
				return i + 1, line, true, string(utf16.Decode(append(out, '\r', '\n')))
			case c == '\b':
				if len(l.units) == 0 {
					continue
				}
				drop := 1
				if k := len(l.units); k >= 2 && isLowSurrogate(l.units[k-1]) && isHighSurrogate(l.units[k-2]) {
					drop = 2
				}
				l.units = l.units[:len(l.units)-drop]
				out = append(out, '\b', ' ', '\b')
			case c < 0x20 || c == 0x7f:
				// Other control characters type nothing on a line.
			default:
				l.units = append(l.units, c)
				out = append(out, c)
			}
		}
	}
	return len(records), "", false, string(utf16.Decode(out))
}

func isHighSurrogate(u uint16) bool { return u >= 0xd800 && u < 0xdc00 }
func isLowSurrogate(u uint16) bool  { return u >= 0xdc00 && u < 0xe000 }
