//go:build !windows

// The login flow tests drive stdin through a pipe and poll it, which the Windows
// console wait cannot do, and run real platform openers.

package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAuthURL  = "https://blocks.example/api/auth/oauth2/authorize"
	testAudience = "https://blocks.example"
)

// liveStdout captures os.Stdout while a flow is still running, so a test can read
// the login URL the flow printed and answer it.
type liveStdout struct {
	mu     sync.Mutex
	text   strings.Builder
	lines  chan string
	finish func()
}

func streamStdout(t *testing.T) *liveStdout {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	out := &liveStdout{lines: make(chan string, 256)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 64*1024)
		for sc.Scan() {
			out.mu.Lock()
			out.text.WriteString(sc.Text() + "\n")
			out.mu.Unlock()
			select {
			case out.lines <- sc.Text():
			default:
			}
		}
	}()
	var once sync.Once
	out.finish = func() {
		once.Do(func() {
			os.Stdout = orig
			w.Close()
			<-done
			r.Close()
		})
	}
	t.Cleanup(out.finish)
	return out
}

// all stops the capture and returns everything the flow printed.
func (o *liveStdout) all() string {
	o.finish()
	return o.String()
}

// loginURL waits for the flow to print the authorization URL and returns it parsed.
func (o *liveStdout) loginURL(t *testing.T) *url.URL {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-o.lines:
			if i := strings.Index(line, testAuthURL+"?"); i >= 0 {
				u, err := url.Parse(strings.TrimSpace(line[i:]))
				if err != nil {
					t.Fatalf("parse printed login URL: %v", err)
				}
				return u
			}
		case <-deadline:
			t.Fatalf("the flow never printed the login URL:\n%s", o.String())
		}
	}
}

func (o *liveStdout) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.text.String()
}

// pipeStdin replaces os.Stdin with a pipe the test writes to.
func pipeStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		w.Close()
		r.Close()
	})
	return w
}

func stubTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return isTerminal }
	t.Cleanup(func() { stdinIsTerminal = orig })
}

func stubLaunch(t *testing.T, fn func(string) error) {
	t.Helper()
	orig := launchBrowser
	launchBrowser = fn
	t.Cleanup(func() { launchBrowser = orig })
}

func loopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

type flowOutcome struct {
	res *BrowserFlowResult
	err error
}

func startFlow(ctx context.Context, ln net.Listener, skipReason string, canPaste bool) <-chan flowOutcome {
	ch := make(chan flowOutcome, 1)
	go func() {
		res, err := runBrowserFlow(ctx, ln, testAuthURL, "cli-client", testAudience, skipReason, canPaste, false)
		ch <- flowOutcome{res, err}
	}()
	return ch
}

func awaitFlow(t *testing.T, ch <-chan flowOutcome) flowOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(10 * time.Second):
		t.Fatal("the login flow did not finish")
		return flowOutcome{}
	}
}

// followRedirect plays the browser coming back to the CLI's callback server.
func followRedirect(t *testing.T, loginURL *url.URL, code string) {
	t.Helper()
	q := loginURL.Query()
	resp, err := http.Get(q.Get("redirect_uri") + "?code=" + code + "&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Errorf("callback request: %v", err)
		return
	}
	resp.Body.Close()
}

// The GUI path: the browser opens, the user signs in, and the callback completes the
// login. The CLI has to say that it opened a browser, not merely that it tried.
func TestBrowserLoginCompletesThroughTheCallbackWhenTheBrowserOpens(t *testing.T) {
	out := streamStdout(t)
	stubLaunch(t, func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		go followRedirect(t, u, "gui-code")
		return nil
	})

	o := awaitFlow(t, startFlow(context.Background(), loopbackListener(t), "", true))

	if o.err != nil {
		t.Fatalf("login failed: %v\n%s", o.err, out.String())
	}
	if o.res.Code != "gui-code" {
		t.Errorf("code = %q, want the one the callback delivered", o.res.Code)
	}
	if printed := out.all(); !strings.Contains(printed, "Opened the login page in your browser") {
		t.Errorf("the CLI must report that a browser was launched:\n%s", printed)
	}
}

// The SSH path: no browser is launched, the manual steps and the full URL are
// printed, and the address the remote browser landed on, pasted back, completes the
// login. A URL from some other login attempt is refused and the prompt repeats.
func TestBrowserLoginContinuesFromAPastedRedirectInAnSSHSession(t *testing.T) {
	stdin := pipeStdin(t)
	out := streamStdout(t)
	stubLaunch(t, func(string) error {
		t.Error("an SSH session must not launch a browser on the remote machine")
		return nil
	})
	ssh := map[string]string{"SSH_CONNECTION": "10.0.0.2 51000 10.0.0.1 22", "DISPLAY": ":0"}
	reason := browserSkipReason(false, "linux", func(k string) string { return ssh[k] })

	flow := startFlow(context.Background(), loopbackListener(t), reason, true)
	loginURL := out.loginURL(t)
	redirect := loginURL.Query().Get("redirect_uri")
	fmt.Fprintf(stdin, "%s?code=stale&state=from-another-login\n", redirect)
	fmt.Fprintf(stdin, "%s?code=pasted-code&state=%s\n", redirect, url.QueryEscape(loginURL.Query().Get("state")))
	o := awaitFlow(t, flow)

	if o.err != nil {
		t.Fatalf("login failed: %v\n%s", o.err, out.String())
	}
	if o.res.Code != "pasted-code" || o.res.RedirectURI != redirect {
		t.Errorf("result = %+v, want the pasted code and the redirect URI the URL named", o.res)
	}
	printed := out.all()
	for _, want := range []string{"SSH session", "To log in manually", "different login attempt"} {
		if !strings.Contains(printed, want) {
			t.Errorf("output is missing %q:\n%s", want, printed)
		}
	}
}

// When the manual steps are showing but the browser comes back to this machine after
// all, the callback wins — and the paste prompt must not leave a read parked on stdin,
// or it would swallow the answer to the next prompt (`Write credentials to .env?`).
func TestACallbackDuringTheManualStepsLeavesStdinForTheNextPrompt(t *testing.T) {
	stdin := pipeStdin(t)
	out := streamStdout(t)
	stubLaunch(t, func(string) error { return errors.New("xdg-open could not open a browser") })

	flow := startFlow(context.Background(), loopbackListener(t), "", true)
	followRedirect(t, out.loginURL(t), "callback-code")
	o := awaitFlow(t, flow)
	if o.err != nil || o.res.Code != "callback-code" {
		t.Fatalf("outcome = %+v, %v; want the callback's code", o.res, o.err)
	}

	fmt.Fprint(stdin, "y\n")
	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "y\n" {
			t.Errorf("next prompt read %q, want the answer typed after login", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer to the next prompt was swallowed by the finished login")
	}
}

// Without anyone able to paste — closed or piped stdin, CI, --no-input — a login that
// cannot open a browser could only end in the timeout, so it fails at once and names
// the headless paths instead. When the failure is predictable it comes before the
// callback port is bound, so the RunBrowserFlow cases hold that port themselves: a
// check that ran after binding would fail on the port instead.
func TestABrowserLoginNobodyCanCompleteFailsAtOnce(t *testing.T) {
	cases := []struct {
		name       string
		terminal   bool
		opts       LoginOptions
		launchFail bool
	}{
		{name: "--no-browser with closed stdin", terminal: false, opts: LoginOptions{NoBrowser: true}},
		{name: "--no-browser under --no-input", terminal: true, opts: LoginOptions{NoBrowser: true, NoInput: true}},
		{name: "browser fails to open off a terminal", launchFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamStdout(t)
			stubLaunch(t, func(string) error { return errors.New("no opener") })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			var err error
			if tc.launchFail {
				_, err = runBrowserFlow(ctx, loopbackListener(t), testAuthURL, "cli-client", testAudience, "", false, false)
			} else {
				stubTerminal(t, tc.terminal)
				held, listenErr := net.Listen("tcp", "127.0.0.1:8787")
				if listenErr != nil {
					t.Skipf("the login callback port is in use: %v", listenErr)
				}
				defer held.Close()
				_, err = RunBrowserFlow(ctx, testAuthURL, "cli-client", testAudience, tc.opts)
			}

			if err == nil {
				t.Fatal("want an immediate error")
			}
			for _, want := range []string{"cannot complete a browser login", "BLOCKS_API_KEY", "--api-key-stdin"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error is missing %q: %v", want, err)
				}
			}
		})
	}
}

func TestBrowserSkipReason(t *testing.T) {
	cases := []struct {
		name      string
		noBrowser bool
		goos      string
		env       map[string]string
		wantSkip  string
	}{
		{name: "desktop macOS", goos: "darwin"},
		{name: "Linux desktop", goos: "linux", env: map[string]string{"DISPLAY": ":0"}},
		{name: "Wayland", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}},
		{name: "WSL without a display", goos: "linux", env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}},
		{name: "--no-browser", noBrowser: true, goos: "darwin", wantSkip: "--no-browser"},
		{name: "SSH into a Mac", goos: "darwin", env: map[string]string{"SSH_TTY": "/dev/ttys001"}, wantSkip: "SSH session"},
		{name: "headless Linux", goos: "linux", wantSkip: "no graphical display"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := browserSkipReason(tc.noBrowser, tc.goos, func(k string) string { return tc.env[k] })
			if tc.wantSkip == "" && got != "" {
				t.Errorf("skip reason = %q, want a launch attempt", got)
			}
			if tc.wantSkip != "" && !strings.Contains(got, tc.wantSkip) {
				t.Errorf("skip reason = %q, want it to mention %q", got, tc.wantSkip)
			}
		})
	}
}

// "Opened your browser" is only true when the opener did not fail. An opener still
// running after the grace period has handed off to a foreground browser.
func TestRunOpenerReportsWhetherTheBrowserOpened(t *testing.T) {
	for _, bin := range []string{"true", "false", "sleep"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available: %v", bin, err)
		}
	}
	if err := runOpener(exec.Command("true"), time.Second); err != nil {
		t.Errorf("an opener that exits cleanly: %v", err)
	}
	if err := runOpener(exec.Command("false"), time.Second); err == nil {
		t.Error("an opener that exits with an error must be reported")
	}
	if err := runOpener(exec.Command("sleep", "5"), 100*time.Millisecond); err != nil {
		t.Errorf("an opener still running after the grace period: %v", err)
	}
	if err := runOpener(exec.Command("/nonexistent/opener"), time.Second); err == nil {
		t.Error("an opener that cannot start must be reported")
	}
}

func TestParsePastedRedirect(t *testing.T) {
	const state = "the-state"
	cases := []struct {
		name     string
		raw      string
		wantCode string
		wantAuth bool
	}{
		{name: "full redirect URL", raw: "http://127.0.0.1:8787/callback?code=abc&state=the-state", wantCode: "abc"},
		{name: "surrounding whitespace", raw: "  http://127.0.0.1:8787/callback?state=the-state&code=abc \r", wantCode: "abc"},
		{name: "another login's state", raw: "http://127.0.0.1:8787/callback?code=abc&state=other"},
		{name: "the sign-in page itself", raw: testAuthURL + "?client_id=x"},
		{name: "not a URL", raw: "%zz"},
		{name: "empty", raw: ""},
		{name: "authorization refused", raw: "http://127.0.0.1:8787/callback?error=access_denied&error_description=no&state=the-state", wantAuth: true},
		// A refusal is bound to its login by state like a code is: another login's
		// error URL must not abort this one.
		{name: "another login's refusal", raw: "http://127.0.0.1:8787/callback?error=access_denied&error_description=no&state=other"},
		{name: "a refusal without state", raw: "http://127.0.0.1:8787/callback?error=access_denied&error_description=no"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := parsePastedRedirect(tc.raw, state)
			if tc.wantCode != "" {
				if err != nil || code != tc.wantCode {
					t.Errorf("= (%q, %v), want %q", code, err, tc.wantCode)
				}
				return
			}
			if err == nil {
				t.Fatalf("= %q, want an error", code)
			}
			var authErr *authorizationError
			if errors.As(err, &authErr) != tc.wantAuth {
				t.Errorf("authorization error = %v, want %v (%v)", !tc.wantAuth, tc.wantAuth, err)
			}
		})
	}
}
