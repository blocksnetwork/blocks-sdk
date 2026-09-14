//go:build windows

package wizard

import (
	"syscall"
	"time"
)

// stdinReadableWithin reports whether os.Stdin has input waiting within
// wait, without consuming anything. It is the Esc disambiguation's way to
// tell a lone Esc from the start of an arrow sequence without blocking on a
// second read — which would park the picker until the user's next keypress
// and consume that key as the disambiguator.
//
// On Windows the console (or pipe) handle is waited on with
// WaitForSingleObject, which is signalled when input is available and
// consumes nothing. Known limitation, accepted with the Windows risk this
// helper ships under: the console input buffer holds non-key records too
// (mouse, focus, window-resize), the handle signals for those as well, and
// a ReadFile on a console returns only character input — so a non-key
// event during the wait window can satisfy the poll and leave the follow-up
// read blocking until a real keypress arrives. That is the pre-fix parking
// resurfacing, Windows-only, in a 50ms window; not merely a no-op. The
// precise remedy, if this is ever reported in the wild, is PeekConsoleInput
// scanning the pending records for an actual KEY_EVENT instead of waiting
// on the handle.
func stdinReadableWithin(wait time.Duration) bool {
	h, err := syscall.GetStdHandle(syscall.STD_INPUT_HANDLE)
	if err != nil {
		return false
	}
	const waitObject0 = 0
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").
		NewProc("WaitForSingleObject").
		Call(uintptr(h), uintptr(wait.Milliseconds()))
	return r == waitObject0
}
