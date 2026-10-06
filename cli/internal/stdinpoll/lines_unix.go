//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package stdinpoll

import "context"

// Lines: see lines.go. A terminal in canonical mode polls readable only once a
// whole line is waiting, and a pipe's read returns whatever is there, so the read
// after a successful poll never waits.
func Lines(ctx context.Context) <-chan string {
	return pollLines(ctx, StdinReadableWithin)
}
