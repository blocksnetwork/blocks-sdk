package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"
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

func RunBrowserFlow(ctx context.Context, authURL string, clientID string, audience string) (*BrowserFlowResult, error) {
	const callbackPort = 8787
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
	if err != nil {
		return nil, fmt.Errorf("failed to start local server on port %d (is another login in progress?): %w", callbackPort, err)
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", callbackPort)

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

	resultCh := make(chan *BrowserFlowResult, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		if errMsg := q.Get("error"); errMsg != "" {
			errDesc := q.Get("error_description")
			fmt.Fprint(w, errorPage(errMsg+": "+errDesc))
			errCh <- fmt.Errorf("authorization error: %s — %s", errMsg, errDesc)
			return
		}

		if q.Get("state") != state {
			fmt.Fprint(w, errorPage("State mismatch — please try again."))
			errCh <- fmt.Errorf("state mismatch — possible CSRF attack")
			return
		}

		code := q.Get("code")
		if code == "" {
			fmt.Fprint(w, errorPage("No authorization code received."))
			errCh <- fmt.Errorf("no authorization code in callback")
			return
		}

		fmt.Fprint(w, callbackSuccessRendered)
		resultCh <- &BrowserFlowResult{Code: code, RedirectURI: redirectURI}
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

	fmt.Printf("  %s\n", fullURL)

	if err := OpenBrowser(fullURL); err != nil {
		fmt.Println("  Could not open browser automatically. Please open the URL above.")
	}

	select {
	case result := <-resultCh:
		return &BrowserFlowResult{
			Code:         result.Code,
			CodeVerifier: verifier,
			RedirectURI:  result.RedirectURI,
			Audience:     audience,
		}, nil
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, fmt.Errorf("authorization cancelled")
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authorization timed out — no callback received within 5 minutes")
	}
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

// OpenBrowser opens the given URL in the platform's default browser.
func OpenBrowser(url string) error {
	name, args, err := browserCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	if _, lookupErr := exec.LookPath(name); lookupErr != nil {
		return missingOpenerError(runtime.GOOS, name)
	}
	return exec.Command(name, args...).Start()
}
