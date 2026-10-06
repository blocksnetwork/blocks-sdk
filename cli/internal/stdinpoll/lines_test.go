package stdinpoll

import "testing"

func press(c uint16) consoleRecord   { return consoleRecord{key: true, down: true, char: c, repeat: 1} }
func release(c uint16) consoleRecord { return consoleRecord{key: true, char: c, repeat: 1} }

// typed is how a console queues a typed or pasted string: a press and a release per
// character.
func typed(s string) []consoleRecord {
	var rs []consoleRecord
	for _, c := range s {
		rs = append(rs, press(uint16(c)), release(uint16(c)))
	}
	return rs
}

// focus, mouse and window-resize records signal the console handle without typing
// anything; on Windows the paste prompt has to sit through them without reading.
var nonKey = consoleRecord{}

// A Windows console is read record by record so that nothing ever waits on the
// user. The line editor is what turns those records back into the line a console
// read would have returned, and it must leave input typed after that line queued
// for the next prompt.
func TestConsoleLineFeed(t *testing.T) {
	t.Run("events that type nothing complete no line", func(t *testing.T) {
		var l consoleLine
		recs := []consoleRecord{nonKey, release('a'), {key: true, down: true, repeat: 1}, nonKey}
		used, _, done, echo := l.feed(recs)
		if done || used != len(recs) || echo != "" {
			t.Errorf("= (used %d, done %v, echo %q), want every record consumed, no line, no echo", used, done, echo)
		}
	})

	t.Run("a line split across reads, with input queued after it", func(t *testing.T) {
		var l consoleLine
		first := append([]consoleRecord{nonKey}, typed("http://127.0.0.1/cb?co")...)
		if used, _, done, echo := l.feed(first); done || used != len(first) || echo != "http://127.0.0.1/cb?co" {
			t.Fatalf("partial line = (used %d, done %v, echo %q)", used, done, echo)
		}
		rest := append(typed("de=x\r"), typed("next answer\r")...)
		used, line, done, echo := l.feed(rest)
		if !done || line != "http://127.0.0.1/cb?code=x" {
			t.Fatalf("= (%q, done %v), want the completed line", line, done)
		}
		if want := len(typed("de=x")) + 1; used != want {
			t.Errorf("used %d records, want %d: the release of Enter and the next answer stay queued", used, want)
		}
		if echo != "de=x\r\n" {
			t.Errorf("echo = %q", echo)
		}
		if _, line, done, _ := l.feed(rest[used:]); !done || line != "next answer" {
			t.Errorf("the next line = (%q, done %v); the editor must start empty after a line", line, done)
		}
	})

	t.Run("backspace and held keys", func(t *testing.T) {
		var l consoleLine
		recs := []consoleRecord{press('a'), {key: true, down: true, char: 'b', repeat: 3}, press('\b'), press('\r')}
		_, line, done, echo := l.feed(recs)
		if !done || line != "abb" {
			t.Errorf("= (%q, done %v), want \"abb\"", line, done)
		}
		if echo != "abbb\b \b\r\n" {
			t.Errorf("echo = %q", echo)
		}
	})

	t.Run("backspace removes a whole surrogate pair and nothing past the line start", func(t *testing.T) {
		var l consoleLine
		recs := []consoleRecord{press('\b'), press('x'), press(0xd83d), press(0xde00), press('\b'), press('\r')}
		if _, line, done, _ := l.feed(recs); !done || line != "x" {
			t.Errorf("= (%q, done %v), want \"x\"", line, done)
		}
	})
}
