//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package wizard

import (
	"os"
	"testing"
	"time"
)

// The Esc disambiguation polls stdin for a pending arrow-sequence
// continuation instead of blocking on a second read. Both halves of that
// contract are pinned here with a pipe: an empty pipe must report not
// readable after the window (this is the lone-Esc case, where blocking
// would park the picker until the next keypress), and a pipe with a byte
// waiting must report readable immediately (this is the arrow case, where
// the burst is already buffered).
func TestFDReadableWithin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	const wait = 20 * time.Millisecond

	if fdReadableWithin(int(r.Fd()), wait) {
		t.Error("empty pipe reported readable; a lone Esc would misparse as an arrow sequence")
	}

	if _, err := w.Write([]byte{'['}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if fdReadableWithin(int(r.Fd()), wait) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pipe with a byte waiting never reported readable; arrow keys would cancel the picker as Esc")
		}
		time.Sleep(wait)
	}
}
