//go:build windows

package stdinpoll

import (
	"context"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procPeekConsoleInputW = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInputW = kernel32.NewProc("ReadConsoleInputW")
)

// inputRecord mirrors INPUT_RECORD with its KEY_EVENT_RECORD arm, the only one read.
// The union is 16 bytes in every arm, so the layout holds for any record type.
type inputRecord struct {
	eventType       uint16
	_               uint16
	keyDown         int32
	repeatCount     uint16
	virtualKeyCode  uint16
	virtualScanCode uint16
	unicodeChar     uint16
	controlKeyState uint32
}

const keyEvent = 0x0001

// Lines: see lines.go. A console handle is signalled for focus, mouse and
// window-resize records as well as keys, and a line-mode ReadFile then waits for
// Enter, so waiting on the handle and reading after it can park the read — and a
// read parked when the login ends would hold up its cleanup or swallow the next
// prompt's answer. A console is therefore read record by record: records are taken
// only once they are queued, so no call here ever waits on the user. Anything that
// is not a console falls back to the handle wait.
func Lines(ctx context.Context) <-chan string {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return pollLines(ctx, StdinReadableWithin)
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		var line consoleLine
		for ctx.Err() == nil {
			event, err := windows.WaitForSingleObject(h, uint32(linePollInterval.Milliseconds()))
			if err != nil {
				return
			}
			// Re-checked after the wait: input that arrives once the wait has ended
			// belongs to whatever reads stdin next.
			if event != windows.WAIT_OBJECT_0 || ctx.Err() != nil {
				continue
			}
			records, err := peekConsoleInput(h)
			if err != nil {
				return
			}
			if len(records) == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			used, text, done, echo := line.feed(records)
			// The records consumed are exactly the ones peeked: the queue is FIFO and
			// nothing else reads it while this runs.
			if err := consumeConsoleInput(h, used); err != nil {
				return
			}
			if echo != "" {
				os.Stdout.WriteString(echo)
			}
			if done {
				select {
				case ch <- text:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

// peekConsoleInput returns the queued console input records without consuming them.
func peekConsoleInput(h windows.Handle) ([]consoleRecord, error) {
	var n uint32
	if err := windows.GetNumberOfConsoleInputEvents(h, &n); err != nil || n == 0 {
		return nil, err
	}
	buf := make([]inputRecord, n)
	var got uint32
	if r, _, err := procPeekConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&got))); r == 0 {
		return nil, err
	}
	records := make([]consoleRecord, got)
	for i, r := range buf[:got] {
		records[i] = consoleRecord{
			key:    r.eventType == keyEvent,
			down:   r.keyDown != 0,
			char:   r.unicodeChar,
			repeat: r.repeatCount,
		}
	}
	return records, nil
}

// consumeConsoleInput removes the first n queued records. They were just peeked, so
// ReadConsoleInputW returns at once instead of waiting for input.
func consumeConsoleInput(h windows.Handle, n int) error {
	if n == 0 {
		return nil
	}
	buf := make([]inputRecord, n)
	var got uint32
	if r, _, err := procReadConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&got))); r == 0 {
		return err
	}
	return nil
}
