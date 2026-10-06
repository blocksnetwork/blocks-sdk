package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/stdinpoll"
)

//go:embed callback_success.html
var callbackSuccessHTML string

//go:embed callback_error.html
var callbackErrorHTML string

//go:embed callback_styles.css
var callbackStylesCSS string

//go:embed callback_pulse.js
var callbackPulseJS string

type callbackPageData struct {
	Styles template.CSS
	Script template.JS
	Error  string
}

var (
	callbackSuccessRendered string
	callbackErrorTemplate   *template.Template
)

func init() {
	shared := callbackPageData{
		Styles: template.CSS(callbackStylesCSS),
		Script: template.JS(callbackPulseJS),
	}

	successTmpl := template.Must(template.New("success").Parse(callbackSuccessHTML))
	var buf bytes.Buffer
	if err := successTmpl.Execute(&buf, shared); err != nil {
		panic(fmt.Errorf("render callback_success.html: %w", err))
	}
	callbackSuccessRendered = buf.String()

	callbackErrorTemplate = template.Must(template.New("error").Parse(callbackErrorHTML))
}

func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func codeChallengeS256(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type BrowserFlowResult struct {
	Code         string
	CodeVerifier string
	RedirectURI  string
	Audience     string
}

// LoginOptions shapes how a browser login reaches the person logging in.
type LoginOptions struct {
	// NoBrowser never launches a browser: the URL and the manual steps are
	// printed instead (--no-browser).
	NoBrowser bool
	// NoInput forbids every prompt: the pasted redirect URL and the
	// organization picker (--no-input).
	NoInput bool
	// Org names the organization to create the key in, by id or name (--org).
	Org string
}

// loginTimeout bounds how long a login waits for the browser to come back.
const loginTimeout = 5 * time.Minute

// launchBrowser is the opener the login flow uses; a var so tests do not spawn a
// real browser.
var launchBrowser = OpenBrowser

func RunBrowserFlow(ctx context.Context, authURL string, clientID string, audience string, opts LoginOptions) (*BrowserFlowResult, error) {
	const callbackPort = 8787
	// Decided before anything is bound or printed: a login that can neither open a
	// browser nor read a pasted URL can only end in a timeout, so it fails now.
	canPaste := !opts.NoInput && stdinIsTerminal()
	skipReason := browserSkipReason(opts.NoBrowser, runtime.GOOS, os.Getenv)
	if skipReason != "" && !canPaste {
		return nil, noBrowserLoginError(skipReason, opts.NoInput)
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		return nil, fmt.Errorf("failed to start local server on port %d (is another login in progress?): %w", callbackPort, err)
	}
	return runBrowserFlow(ctx, listener, authURL, clientID, audience, skipReason, canPaste, opts.NoInput)
}

// runBrowserFlow is RunBrowserFlow past the port binding. The redirect URI is
// derived from the listener, so it always names the address the callback server
// actually holds.
func runBrowserFlow(ctx context.Context, listener net.Listener, authURL, clientID, audience, skipReason string, canPaste, noInput bool) (*BrowserFlowResult, error) {
	redirectURI := fmt.Sprintf("http://%s/callback", listener.Addr().String())

	verifier, err := generateCodeVerifier()
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}
	challenge := codeChallengeS256(verifier)

	state, err := generateState()
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email offline_access"},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {audience},
	}
	fullURL := authURL + "?" + params.Encode()

	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code, page, cbErr := parseCallback(r.URL.Query(), state)
		if cbErr != nil {
			fmt.Fprint(w, errorPage(page))
			errCh <- cbErr
			return
		}
		fmt.Fprint(w, callbackSuccessRendered)
		resultCh <- code
	})

	server := &http.Server{Handler: mux}
	go func() {
		if serveErr := server.Serve(listener); serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	reason := skipReason
	if reason == "" {
		if openErr := launchBrowser(fullURL); openErr != nil {
			reason = openErr.Error()
		}
	}

	var pasted <-chan string
	if reason == "" {
		fmt.Println("  Opened the login page in your browser — finish signing in there.")
		fmt.Printf("  If no browser window appeared, open this URL yourself:\n\n  %s\n\n", fullURL)
		fmt.Println("  Waiting for the browser to finish (Ctrl+C to cancel)...")
	} else {
		if !canPaste {
			return nil, noBrowserLoginError(reason, noInput)
		}
		printManualLoginSteps(reason, fullURL)
		// The paste reader is stopped and waited for before returning, so a callback
		// that wins the race leaves nothing reading stdin to swallow the answer to
		// the next prompt.
		waitCtx, stopWaiting := context.WithCancel(ctx)
		lines := stdinpoll.Lines(waitCtx)
		defer func() {
			stopWaiting()
			for range lines {
			}
		}()
		pasted = lines
	}

	timeout := time.After(loginTimeout)
	for {
		select {
		case code := <-resultCh:
			return &BrowserFlowResult{Code: code, CodeVerifier: verifier, RedirectURI: redirectURI, Audience: audience}, nil
		case err := <-errCh:
			return nil, err
		case line, ok := <-pasted:
			if !ok {
				pasted = nil
				continue
			}
			code, pasteErr := parsePastedRedirect(line, state)
			if pasteErr == nil {
				return &BrowserFlowResult{Code: code, CodeVerifier: verifier, RedirectURI: redirectURI, Audience: audience}, nil
			}
			var authErr *authorizationError
			if errors.As(pasteErr, &authErr) {
				return nil, pasteErr
			}
			fmt.Printf("  %v\n  Paste the full URL from the browser's address bar: ", pasteErr)
		case <-ctx.Done():
			return nil, fmt.Errorf("authorization cancelled")
		case <-timeout:
			return nil, fmt.Errorf("authorization timed out — the login was not completed within %s", loginTimeout)
		}
	}
}

// browserSkipReason says why no browser should be launched for this login, or ""
// when launching one is worth trying. A browser opened where nobody is looking is
// worse than none: the CLI would report success and then wait out the timeout.
func browserSkipReason(noBrowser bool, goos string, getenv func(string) string) string {
	switch {
	case noBrowser:
		return "--no-browser was passed"
	case getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "":
		return "this is an SSH session, so a browser opened here would not be on your screen"
	}
	switch goos {
	case "linux", "freebsd", "openbsd", "netbsd":
		// WSL opens the Windows browser without any X or Wayland display.
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" && getenv("WSL_DISTRO_NAME") == "" {
			return "no graphical display is available (DISPLAY is not set)"
		}
	}
	return ""
}

// noBrowserLoginError is the fail-fast answer for a login that has no browser and
// nobody at a terminal to paste the redirect URL back.
func noBrowserLoginError(reason string, noInput bool) error {
	why := "there is no terminal to paste the login redirect URL into"
	if noInput {
		why = "--no-input forbids prompting for the login redirect URL"
	}
	return fmt.Errorf("cannot complete a browser login: %s, and %s.\n"+
		"  Run 'blocks login --no-browser' in an interactive terminal to log in from another device, or\n"+
		"  authenticate without a browser using an API key from the dashboard:\n"+
		"    export BLOCKS_API_KEY=<key>                     # every command uses it; no login needed\n"+
		"    blocks login --api-key-stdin --no-write-env     # or store it in a profile (key on stdin)",
		reason, why)
}

func printManualLoginSteps(reason, fullURL string) {
	fmt.Printf("  Not opening a browser: %s.\n\n", reason)
	fmt.Println("  To log in manually:")
	fmt.Println("    1. Open this URL in a browser on any device:")
	fmt.Printf("\n       %s\n\n", fullURL)
	fmt.Println("    2. Sign in. The browser is then sent to a 127.0.0.1 address; on another")
	fmt.Println("       device that page fails to load, which is expected.")
	fmt.Println("    3. Copy the full URL from the browser's address bar and paste it here.")
	fmt.Println()
	fmt.Print("  Paste the URL: ")
}

// authorizationError is a refusal the authorization server reported in the
// redirect. Retrying the paste cannot fix it, unlike a mistyped URL.
type authorizationError struct{ code, description string }

func (e *authorizationError) Error() string {
	return fmt.Sprintf("authorization error: %s — %s", e.code, e.description)
}

// parseCallback extracts the authorization code from a redirect's query. On
// failure it also returns the text for the browser's error page.
//
// State is checked before anything else, the error included: a refusal is bound to
// its login exactly as a code is (the authorization server echoes state on both,
// RFC 6749 §4.1.2.1), so another login's error redirect cannot end this one.
func parseCallback(q url.Values, state string) (code, page string, err error) {
	if q.Get("state") != state {
		return "", "State mismatch — please try again.", fmt.Errorf("state mismatch — possible CSRF attack")
	}
	if errMsg := q.Get("error"); errMsg != "" {
		errDesc := q.Get("error_description")
		return "", errMsg + ": " + errDesc, &authorizationError{code: errMsg, description: errDesc}
	}
	code = q.Get("code")
	if code == "" {
		return "", "No authorization code received.", fmt.Errorf("no authorization code in callback")
	}
	return code, "", nil
}

// parsePastedRedirect reads the authorization code out of a redirect URL the user
// pasted. It applies the same state check as the callback server, so a pasted URL
// from some other login is refused rather than exchanged.
func parsePastedRedirect(raw, state string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if raw == "" || err != nil {
		return "", fmt.Errorf("That is not a URL.")
	}
	q := u.Query()
	if q.Get("code") == "" && q.Get("error") == "" {
		return "", fmt.Errorf("That URL carries no login result — copy it after the browser leaves the sign-in page.")
	}
	code, _, err := parseCallback(q, state)
	if err != nil {
		var authErr *authorizationError
		if errors.As(err, &authErr) {
			return "", err
		}
		return "", fmt.Errorf("That URL belongs to a different login attempt — paste the one from this login.")
	}
	return code, nil
}

func errorPage(detail string) string {
	var buf bytes.Buffer
	data := callbackPageData{
		Styles: template.CSS(callbackStylesCSS),
		Script: template.JS(callbackPulseJS),
		Error:  detail,
	}
	if err := callbackErrorTemplate.Execute(&buf, data); err != nil {
		return fmt.Sprintf("<html><body><pre>%s</pre></body></html>", template.HTMLEscapeString(detail))
	}
	return buf.String()
}

// browserCommand maps a GOOS string to the command and arguments used to open
// a URL in the platform's default browser. Pure function; no side effects.
func browserCommand(goos, url string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{url}, nil
	case "linux", "freebsd", "openbsd":
		return "xdg-open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	default:
		return "", nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// missingOpenerError formats a friendly error for the case where the
// platform's browser opener is not on PATH. Pure function for testability.
func missingOpenerError(goos, name string) error {
	hint := ""
	switch goos {
	case "freebsd":
		hint = " (install with `pkg install xdg-utils`)"
	case "openbsd":
		hint = " (install with `pkg_add xdg-utils`)"
	case "linux":
		hint = " (install xdg-utils via your package manager)"
	}
	return fmt.Errorf("browser opener %q not found on PATH%s", name, hint)
}

// openerGrace is how long OpenBrowser waits for the opener to report failure.
// Platform openers hand the URL to a browser and exit at once; one still running
// after this long has handed off to a browser that stays in the foreground.
const openerGrace = 3 * time.Second

// OpenBrowser opens the given URL in the platform's default browser. It reports
// an opener that is missing, fails to start, or exits with an error, so callers
// can tell a browser that opened from one that did not.
func OpenBrowser(url string) error {
	name, args, err := browserCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	if _, lookupErr := exec.LookPath(name); lookupErr != nil {
		return missingOpenerError(runtime.GOOS, name)
	}
	return runOpener(exec.Command(name, args...), openerGrace)
}

func runOpener(cmd *exec.Cmd, grace time.Duration) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s: %w", filepath.Base(cmd.Path), err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s could not open a browser (%v)", filepath.Base(cmd.Path), err)
		}
		return nil
	case <-time.After(grace):
		return nil
	}
}
