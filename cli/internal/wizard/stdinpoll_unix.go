//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package wizard

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// stdinReadableWithin reports whether os.Stdin has input waiting within
// wait, without consuming anything. It is the Esc disambiguation's way to
// tell a lone Esc from the start of an arrow sequence without blocking on a
// second read — which would park the picker until the user's next keypress
// and consume that key as the disambiguator.
func stdinReadableWithin(wait time.Duration) bool {
	return fdReadableWithin(int(os.Stdin.Fd()), wait)
}

func fdReadableWithin(fd int, wait time.Duration) bool {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, int(wait.Milliseconds()))
	// Revents, not the ready count: POLLHUP/POLLERR signal without input
	// pending, and following them with the blocking read is the parking this
	// helper exists to prevent.
	return err == nil && fds[0].Revents&unix.POLLIN != 0
}
