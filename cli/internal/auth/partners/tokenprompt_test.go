package partners

import (
	"errors"
	"strings"
	"testing"
)

// recordingOpener records the URL it was asked to open, so tests can assert
// the affordance fired (and at which origin) without spawning a browser.
type recordingOpener struct {
	urls []string
	err  error
}

func (o *recordingOpener) open(url string) error {
	o.urls = append(o.urls, url)
	return o.err
}

// The non-interactive path is the single paste prompt the flows always
// printed: one line in, token out, and no browser involved.
func TestPromptTokenOpenBrowser_NonInteractiveSinglePrompt(t *testing.T) {
	opener := &recordingOpener{}
	token, err := promptTokenOpenBrowser(
		"Create a token at https://example.com/tokens.\n",
		"Paste the token: ",
		"https://example.com/tokens",
		strings.NewReader("tok\n"),
		false,
		opener.open,
	)
	if err != nil {
		t.Fatalf("promptTokenOpenBrowser: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q, want tok", token)
	}
	if len(opener.urls) != 0 {
		t.Errorf("non-interactive path opened a browser: %v", opener.urls)
	}
}

// Enter at the affordance prompt opens the token page, then the paste
// prompt reads the token.
func TestPromptTokenOpenBrowser_EnterOpensThenPastes(t *testing.T) {
	opener := &recordingOpener{}
	token, err := promptTokenOpenBrowser(
		"Create a token at https://example.com/tokens.\n",
		"Paste the token: ",
		"https://example.com/tokens",
		strings.NewReader("\ntok\n"),
		true,
		opener.open,
	)
	if err != nil {
		t.Fatalf("promptTokenOpenBrowser: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q, want tok", token)
	}
	if len(opener.urls) != 1 || opener.urls[0] != "https://example.com/tokens" {
		t.Errorf("opener urls = %v, want [https://example.com/tokens]", opener.urls)
	}
}

// A token pasted at the affordance prompt is accepted directly — no
// browser, no second prompt.
func TestPromptTokenOpenBrowser_PasteAtFirstPromptSkipsBrowser(t *testing.T) {
	opener := &recordingOpener{}
	token, err := promptTokenOpenBrowser(
		"Create a token at https://example.com/tokens.\n",
		"Paste the token: ",
		"https://example.com/tokens",
		strings.NewReader("tok\n"),
		true,
		opener.open,
	)
	if err != nil {
		t.Fatalf("promptTokenOpenBrowser: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q, want tok", token)
	}
	if len(opener.urls) != 0 {
		t.Errorf("opener fired for a directly-pasted token: %v", opener.urls)
	}
}

// An opener failure is reported inline and the paste prompt still runs —
// the URL is already on screen, so the flow is completable by hand.
func TestPromptTokenOpenBrowser_OpenerFailureStillPrompts(t *testing.T) {
	opener := &recordingOpener{err: errors.New("no opener")}
	token, err := promptTokenOpenBrowser(
		"Create a token at https://example.com/tokens.\n",
		"Paste the token: ",
		"https://example.com/tokens",
		strings.NewReader("\ntok\n"),
		true,
		opener.open,
	)
	if err != nil {
		t.Fatalf("promptTokenOpenBrowser: %v", err)
	}
	if token != "tok" {
		t.Errorf("token = %q, want tok", token)
	}
}

// Enter then EOF surfaces promptToken's "no input received" rather than
// accepting an empty token.
func TestPromptTokenOpenBrowser_EnterThenEOFRejectsEmpty(t *testing.T) {
	opener := &recordingOpener{}
	_, err := promptTokenOpenBrowser(
		"Create a token at https://example.com/tokens.\n",
		"Paste the token: ",
		"https://example.com/tokens",
		strings.NewReader("\n"),
		true,
		opener.open,
	)
	if err == nil {
		t.Fatal("promptTokenOpenBrowser = nil error, want no-input error")
	}
	if len(opener.urls) != 1 {
		t.Errorf("opener urls = %v, want one open attempt", opener.urls)
	}
}

// The interactive flag is what the flows derive from interactiveStdin: an
// injected reader (a test, or the --no-input refusal) never gets the
// affordance, whatever the host terminal is.
func TestInteractiveStdinReaderInjectedIsNeverInteractive(t *testing.T) {
	if interactiveStdin(strings.NewReader("x")) {
		t.Error("interactiveStdin(injected reader) = true, want false")
	}
}
