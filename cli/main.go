package main

import (
	"fmt"
	"io"
	"os"

	"github.com/pubnub/blocks-sdk/cli/cmd"
	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

func main() {
	if err := cmd.Execute(); err != nil {
		reportFatal(os.Stderr, err)
		os.Exit(1)
	}
}

// reportFatal prints the error a failing command exits through, rendered so no byte of
// it can forge terminal output.
//
// It escapes here because this is the one boundary every command's error text passes
// through, and much of that text is not the CLI's own: internal/blocksapi's APIError
// quotes the backend's message and code verbatim, and publish, unregister, init,
// suggest and cardfetch all surface it through this printer. Escaping only at the call
// sites that thought of it makes safety opt-in — every error path added later is raw
// until someone remembers — whereas escaping at the boundary makes it a property of
// printing an error at all, which is what the invariant in internal/termsafe claims.
//
// Escaping twice is not a hazard: termsafe.Text is idempotent (TestTextIsIdempotent),
// because what it produces contains no character it escapes. A message already escaped
// where it was built therefore reaches the terminal in exactly that form.
func reportFatal(w io.Writer, err error) {
	fmt.Fprintf(w, "Error: %s\n", termsafe.Message(err.Error()))
}
