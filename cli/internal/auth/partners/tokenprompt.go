package partners

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"golang.org/x/term"
)

// openBrowser is the browser opener the token prompt offers; a var so tests
// can replace it instead of spawning a real browser.
var openBrowser = auth.OpenBrowser

// promptTokenOpenBrowser reads a partner API token, offering to open the
// token-creation page in the default browser first.
//
// intro is everything up to the paste line (URL, required scopes); pasteLine
// is the paste prompt itself. Non-interactive callers get byte-for-byte the
// single paste prompt the flows always printed — an injected reader is a
// test or the --no-input refusal, and a session that is not on a terminal
// must not find a browser spawned beside it.
//
// Interactive callers get the affordance instead: "Press Enter to open <url>
// (or paste the token now)". Enter opens the browser best-effort — the URL
// is already on screen, so an open failure is reported inline and the paste
// prompt follows — and a token pasted at the first prompt is accepted
// directly, saving the round-trip for the user who had one ready.
//
// openURL is the same code-owned token page intro names, never
// caller-supplied text, so the opener cannot be steered at another origin.
func promptTokenOpenBrowser(intro, pasteLine, openURL string, r io.Reader, interactive bool, opener func(string) error) (string, error) {
	if !interactive {
		return promptToken(intro+pasteLine, r)
	}

	fmt.Print(intro)
	// The affordance says "it" — intro just named the URL, and repeating it
	// here is noise on the line the user reads while deciding. The opener's
	// failure message repeats the URL for the case that matters (needing to
	// reach for it by hand).
	fmt.Print("Press Enter to open it in your browser (or paste the token now): ")
	line, err := readPromptLine(r)
	if err != nil {
		return "", err
	}
	if line != "" {
		return line, nil
	}
	if err := opener(openURL); err != nil {
		fmt.Printf("  (could not open a browser: %v — open %s manually)\n", err, openURL)
	}
	return promptToken(pasteLine, r)
}

// interactiveStdin reports whether the token prompt may offer its
// Enter-to-open affordance: only when the flow is reading the real stdin
// (Reader nil — an injected reader is a test or the --no-input refusal) and
// that stdin is a terminal. A piped or /dev/null session gets the plain
// paste prompt and no browser spawn.
func interactiveStdin(reader io.Reader) bool {
	if reader != nil {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// readPromptLine reads one trimmed line from r. Unlike promptToken it
// accepts an empty line — Enter is a meaningful answer here ("open the
// browser"), not a missing token.
//
// It reads unbuffered, one byte at a time, because it shares r with the
// paste prompt that follows: a bufio.Scanner (what promptToken uses) reads
// ahead in chunks, so a scanner here would buffer the token the user pasted
// ahead of the prompt and promptToken's own scanner would never see it.
// One line of interactive input costs nothing to read a byte at a time.
func readPromptLine(r io.Reader) (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimSpace(sb.String()), nil
			}
			if buf[0] != '\r' {
				sb.WriteByte(buf[0])
			}
			continue
		}
		if err == io.EOF {
			// A final line without a newline still counts; bare EOF is an
			// empty answer, which the caller treats as Enter.
			return strings.TrimSpace(sb.String()), nil
		}
		if err != nil {
			return "", err
		}
		// n == 0 with no error: a reader that keeps doing this would spin the
		// loop forever, so treat it as no progress rather than trust it to
		// eventually yield.
		return "", io.ErrNoProgress
	}
}
