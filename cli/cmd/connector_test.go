package cmd

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/connectors"
	"github.com/spf13/pflag"
)

const (
	connectorsAPI = "/api/v1/pubnub-connectors"
	createRoute   = "POST " + connectorsAPI + "/exposures"
	linkRoute     = "POST " + connectorsAPI + "/keysets"
)

// connectorFake is a deployment's Connectors routes. Every request is logged; a route nobody
// registered answers 404 so an unexpected call is visible in the log and fails the test that made it.
type connectorFake struct {
	t      *testing.T
	mu     sync.Mutex
	log    []string
	bodies map[string]string
	routes map[string]func(w http.ResponseWriter, body string)
	// onBrowser runs when the CLI opens the browser, standing in for what the person does there.
	onBrowser func()
}

func newConnectorFake(t *testing.T) *connectorFake {
	f := &connectorFake{t: t, bodies: map[string]string{}, routes: map[string]func(http.ResponseWriter, string){}}
	f.reply("GET /api/v1/orgs", 200, `{"orgs":[{"id":"org-1","name":"Engineering","pubnubConnectorsEnabled":true}]}`)
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON(connection("conn-1", "Acme PubNub", "active", false)))
	f.reply("GET "+connectorsAPI+"/account/conn-1/keysets", 200, pickerJSON)
	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON(keysetView("lk-1", "Prod", "sub-1", "access_manager", "admin_api", "active", true)))
	f.reply("GET "+connectorsAPI+"/exposures", 200, `{"exposures":[]}`)
	f.reply(linkRoute, 201, mustJSON(keysetView("lk-new", "Live / Prod", "sub-1", "access_manager", "admin_api", "active", true)))
	f.routes[createRoute] = func(w http.ResponseWriter, body string) {
		var req map[string]any
		_ = json.Unmarshal([]byte(body), &req)
		x := exposureView("x-new", req["agentName"].(string), req["kind"].(string), false)
		x["channelTemplate"] = req["channelTemplate"]
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"exposure": x})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		f.mu.Lock()
		f.log = append(f.log, key)
		f.bodies[key] = string(b)
		h := f.routes[key]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if h == nil {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"Not found","code":"NOT_FOUND"}`))
			return
		}
		h(w, string(b))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("BLOCKS_BACKEND_URL", srv.URL)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	seedEnterpriseProfileForTest(t, srv.URL)
	return f
}

func (f *connectorFake) reply(route string, status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = func(w http.ResponseWriter, _ string) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func (f *connectorFake) replyError(route string, status int, code, message string) {
	f.reply(route, status, mustJSON(map[string]any{"error": message, "code": code}))
}

func (f *connectorFake) requested(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.log {
		if strings.Contains(l, fragment) {
			return true
		}
	}
	return false
}

// wantUntouched fails the test if any request so far contains one of the fragments.
func (f *connectorFake) wantUntouched(t *testing.T, fragments ...string) {
	t.Helper()
	for _, fr := range fragments {
		if f.requested(fr) {
			t.Fatalf("%s was requested: %v", fr, f.log)
		}
	}
}

func (f *connectorFake) wantCreatedOn(t *testing.T, keysetID string) {
	t.Helper()
	if got := f.body(createRoute)["linkedKeysetId"]; got != keysetID {
		t.Fatalf("created on keyset %v, want %s: %v", got, keysetID, f.log)
	}
}

func (f *connectorFake) body(route string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal([]byte(f.bodies[route]), &m); err != nil {
		f.t.Fatalf("no JSON body was sent to %s (%q)", route, f.bodies[route])
	}
	return m
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v) // fixtures are plain maps and slices, which always encode
	return string(b)
}

func connection(id, name, status string, mine bool) map[string]any {
	return map[string]any{
		"id": id, "name": name, "pubnubAccountId": "acct", "status": status, "isMine": mine,
		"connectedBy": map[string]any{"userId": "u", "name": "Ana Lee", "email": "ana@example.com"},
		"createdAt":   "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
	}
}

func connectionsJSON(conns ...map[string]any) string {
	return mustJSON(map[string]any{"connections": conns})
}

const pickerJSON = `{"apps":[{"id":"a1","name":"Live","keysets":[
 {"id":"k-1","name":"Prod","subscribeKey":"sub-1","linkedKeysetId":null},
 {"id":"k-2","name":"Staging","subscribeKey":"sub-2","linkedKeysetId":"lk-2"}]}]}`

func keysetView(id, label, sub, mode, source, status string, rich bool) map[string]any {
	return map[string]any{
		"id": id, "label": label, "subscribeKey": sub, "origin": nil, "mode": mode, "source": source, "status": status,
		"features":  map[string]any{"accessManager": mode == "access_manager", "persistence": rich, "presence": rich, "wildcard": true, "revokeV3": true, "appContext": false},
		"createdBy": nil, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z",
		"disabledAt": nil, "pubnubKeysetId": nil, "pubnubAccountId": nil, "accountConnectionId": nil,
	}
}

func keysetsJSON(views ...map[string]any) string { return mustJSON(map[string]any{"keysets": views}) }

func exposureView(id, name, kind string, revoked bool) map[string]any {
	x := map[string]any{
		"id": id, "orgId": "org-1", "agentId": "ag", "agentName": name, "displayName": "Display " + name,
		"description": nil, "linkedKeysetId": "lk-1", "kind": kind, "channelTemplate": "live.{show}",
		"templateParam": "show", "rawAccess": false, "rawSelector": nil, "config": map[string]any{},
		"consentedBy": nil, "consentedAt": nil, "createdBy": "u", "createdAt": "2026-01-01T00:00:00Z",
		"revokedBy": nil, "revokedAt": nil, "allValues": map[string]any{"eligible": true, "pattern": "live.*"},
	}
	if revoked {
		x["revokedAt"] = "2026-02-01T00:00:00Z"
	}
	return x
}

func exposuresJSON(xs ...map[string]any) string { return mustJSON(map[string]any{"exposures": xs}) }

func rawExposure(channels, patterns []string) map[string]any {
	x := exposureView("x1", "live", "subscribe", false)
	x["channelTemplate"], x["rawAccess"] = nil, true
	x["rawSelector"] = map[string]any{"channels": channels, "patterns": patterns}
	return x
}

// browserFake is an org with no account connection and no linked keyset, so create has to open the browser.
func browserFake(t *testing.T) *connectorFake {
	f := newConnectorFake(t)
	f.reply("GET "+connectorsAPI+"/account", 200, connectionsJSON())
	f.reply("GET "+connectorsAPI+"/keysets", 200, keysetsJSON())
	return f
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type connectorRun struct {
	stdout   string
	err      error
	browsed  []string
	errorMsg string
}

// runConnector executes `blocks connector ...` against the fake. A non-empty stdin means a person is
// at a terminal and answers with those lines; "" means no terminal.
func runConnector(t *testing.T, f *connectorFake, stdin string, args ...string) connectorRun {
	t.Helper()
	resetConnectorFlags()
	t.Cleanup(resetConnectorFlags)

	origInteractive, origScanner, origBrowser := isInteractive, stdinScanner, openBrowserFunc
	origInterval, origTimeout := connectors.DefaultWaitInterval, connectors.DefaultWaitTimeout
	t.Cleanup(func() {
		isInteractive, stdinScanner, openBrowserFunc = origInteractive, origScanner, origBrowser
		connectors.DefaultWaitInterval, connectors.DefaultWaitTimeout = origInterval, origTimeout
	})
	isInteractive = func() bool { return stdin != "" }
	stdinScanner = bufio.NewScanner(strings.NewReader(stdin))
	connectors.DefaultWaitInterval, connectors.DefaultWaitTimeout = time.Millisecond, 100*time.Millisecond

	var res connectorRun
	openBrowserFunc = func(u string) error {
		res.browsed = append(res.browsed, u)
		if f.onBrowser != nil {
			f.onBrowser()
		}
		return nil
	}
	res.stdout = captureStdout(func() {
		rootCmd.SetArgs(append([]string{"connector"}, args...))
		res.err = rootCmd.Execute()
	})
	if res.err != nil {
		res.errorMsg = res.err.Error()
	}
	return res
}

func mustRun(t *testing.T, f *connectorFake, stdin string, args ...string) connectorRun {
	t.Helper()
	res := runConnector(t, f, stdin, args...)
	if res.err != nil {
		t.Fatalf("connector %v failed: %v\n%s", args, res.err, res.stdout)
	}
	return res
}

// wantErr fails the test unless the run failed with every fragment in its error.
func (r connectorRun) wantErr(t *testing.T, fragments ...string) {
	t.Helper()
	for _, fr := range append(fragments, "") { // "" still requires an error when no fragment is given
		if r.err == nil || !strings.Contains(r.errorMsg, fr) {
			t.Fatalf("error = %v, want one containing %q\n%s", r.err, fr, r.stdout)
		}
	}
}

func (r connectorRun) wantOut(t *testing.T, fragments ...string) {
	t.Helper()
	for _, fr := range fragments {
		if !strings.Contains(r.stdout, fr) {
			t.Fatalf("output lacks %q:\n%s", fr, r.stdout)
		}
	}
}

func resetConnectorFlags() {
	connectorCreateOpts = connectorCreateFlags{}
	connectorEditOpts = connectorShape{}
	connectorListAll, connectorListJSON, connectorRevokeYes = false, false, false
	connectorAccountListAll, connectorAccountListJSON, connectorAccountYes = false, false, false
	connectorKeysetAccount, connectorKeysetLabel = "", ""
	connectorKeysetJSON, connectorKeysetAll, connectorKeysetDisableOK = false, false, false
	for _, c := range append(append(connectorCmd.Commands(), connectorAccountCmd.Commands()...), connectorKeysetCmd.Commands()...) {
		c.Flags().VisitAll(func(fl *pflag.Flag) { fl.Changed = false })
	}
}
