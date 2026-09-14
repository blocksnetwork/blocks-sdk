//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package wizard

import (
	"os"
	"testing"
	"time"
)

// withPipeStdin swaps os.Stdin for a pipe for the duration of the test.
// readKey and its availability poll both read the os.Stdin global, and
// poll operates on pipe fds, so these tests drive the real decode path —
// including the timed Esc disambiguation — without a terminal.
//
// Unix-only: the Windows helper waits on the process's console handle, which
// a swapped-in pipe does not become; the Windows path needs a manual smoke
// run instead.
func withPipeStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
		w.Close()
	})
	return w
}

// A lone Esc must resolve within the disambiguation window. The pre-fix
// behavior parked readKey on a blocking second read until the user's next
// keypress — and consumed that keypress — which a regression would bring
// back as a silent hang here, so the result arrives via a goroutine with a
// deadline rather than a direct call.
func TestReadKeyLoneEscResolvesWithinWindow(t *testing.T) {
	w := withPipeStdin(t)

	if _, err := w.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		key string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		key, err := readKey()
		ch <- result{key, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("readKey: %v", r.err)
		}
		if r.key != "esc" {
			t.Errorf("key = %q, want esc", r.key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lone Esc did not resolve within the disambiguation window — the blocking-second-read regression is back")
	}
}

func TestReadKeyArrowSequences(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"up burst", []byte{0x1b, '[', 'A'}, "up"},
		{"down burst", []byte{0x1b, '[', 'B'}, "down"},
		// Unrecognized sequences return the ignored key (""): Esc cancels
		// pickers now, so a left/right arrow decoding as Esc would cancel
		// on a navigation keypress. These were no-ops before Esc meant
		// anything, and must stay no-ops.
		{"left arrow is ignored, not esc", []byte{0x1b, '[', 'C'}, ""},
		{"right arrow is ignored, not esc", []byte{0x1b, '[', 'D'}, ""},
		{"home is ignored, not esc", []byte{0x1b, '[', 'H'}, ""},
		{"alt-modified key is ignored, not esc", []byte{0x1b, 'x'}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := withPipeStdin(t)
			if _, err := w.Write(tc.in); err != nil {
				t.Fatal(err)
			}
			key, err := readKey()
			if err != nil {
				t.Fatalf("readKey: %v", err)
			}
			if key != tc.want {
				t.Errorf("key = %q, want %q", key, tc.want)
			}
		})
	}
}

// tmux and slow terminals can split an arrow sequence across reads. The
// continuation arriving while readKey is mid-poll is the case the window
// exists for: it must be seen as an arrow, not as a lone Esc plus stray
// keys. readKey therefore starts before the continuation is written —
// pre-buffering the whole sequence would only re-test the burst path.
func TestReadKeySplitArrowStillParses(t *testing.T) {
	w := withPipeStdin(t)

	type result struct {
		key string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		key, err := readKey()
		ch <- result{key, err}
	}()

	if _, err := w.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := w.Write([]byte{'[', 'A'}); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("readKey: %v", r.err)
		}
		if r.key != "up" {
			t.Errorf("key = %q, want up", r.key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("split arrow sequence did not resolve — the poll lost the continuation")
	}
}
