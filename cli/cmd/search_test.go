package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
	"github.com/pubnub/blocks-sdk/cli/internal/agentsearch"
	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

const searchFixture = `{"agents":[
	{"agentName":"weather","displayName":"Weather","cardSummary":"Forecasts by city","listing":"private","onlineCount":1},
	{"agentName":"weather_bot","displayName":"Storm Watcher","description":"Public forecaster","listing":"public","onlineCount":0}
]}`

func registryServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/registry/agents" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Isolates profiles and legacy credentials so apiKey "" is truly anonymous, not the developer's login.
func runSearchAgainst(t *testing.T, backendURL, apiKey string, args ...string) (string, error) {
	t.Helper()
	restoreCLIState(t)
	isolateAmbientState(t)
	isolateCredentials(t)
	t.Cleanup(isolateProfiles(t))
	t.Setenv("BLOCKS_BACKEND_URL", backendURL)
	t.Setenv("BLOCKS_API_KEY", apiKey)
	t.Cleanup(func() {
		_ = searchCmd.Flags().Set("json", "false")
		_ = searchCmd.Flags().Set("limit", fmt.Sprint(searchDefaultLimit))
		_ = searchCmd.Flags().Set("cursor", "")
		_ = searchCmd.Flags().Set("status", agentsearch.StatusOnline)
		searchCmd.Flags().Lookup("limit").Changed = false
	})
	return runRootCapturing(t, append([]string{"search"}, args...)...)
}

func TestSearchPrintsVisibilityAndAvailability(t *testing.T) {
	srv := registryServer(t, http.StatusOK, searchFixture)
	out, err := runSearchAgainst(t, srv.URL, "bk_test", "weather")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	for _, want := range []string{
		"weather  private · online", "Forecasts by city",
		"weather_bot  public · offline", "Storm Watcher", "Public forecaster",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Forecasts by city") > strings.Index(out, "Public forecaster") {
		t.Errorf("rows reordered; the registry's ranking must be kept:\n%s", out)
	}
	if strings.Contains(out, "\n  Weather\n") {
		t.Errorf("a display name that only restates the agent name is shown:\n%s", out)
	}
	if strings.Contains(out, "public agents only") {
		t.Errorf("an authenticated search claims to be public-only:\n%s", out)
	}
}

func TestSearchResultsFitTheTerminal(t *testing.T) {
	long := strings.Repeat("Converts documents between formats ", 10)
	wide := strings.Repeat("文書を変換します", 10)
	online := true
	result := agentsearch.Result{Authenticated: true, Agents: []agentsearch.Agent{
		{AgentName: strings.Repeat("doc_converter_", 8), DisplayName: long, Summary: wide, Listing: agentsearch.ListingPrivate, Online: &online},
	}}
	var out strings.Builder
	writeSearchResults(&out, searchStyle{width: 60}, "doc", result)
	for _, line := range strings.Split(out.String(), "\n") {
		if n := runewidth.StringWidth(line); n > 60 {
			t.Errorf("line is %d cells wide, want <= 60: %q", n, line)
		}
	}
}

func TestSearchOutsideATerminalPrintsTheNextPageCommand(t *testing.T) {
	withNext := strings.Replace(searchFixture, "\n]}", `],"next":"c2"}`, 1)
	cases := []struct {
		name, body, want string
		absent           bool
	}{
		{"the registry has a next page", withNext, "More results: blocks search --limit 2 --cursor c2 -- weather", false},
		{"the registry has no next page", searchFixture, "More results:", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := registryServer(t, http.StatusOK, tc.body)
			out, err := runSearchAgainst(t, srv.URL, "bk_test", "weather", "--limit", "2")
			if err != nil {
				t.Fatalf("search: %v\n%s", err, out)
			}
			if got := strings.Contains(out, tc.want); got == tc.absent {
				t.Errorf("output contains %q = %v, want %v:\n%s", tc.want, got, !tc.absent, out)
			}
		})
	}
}

func TestSearchStatusFlagFiltersTheRegistry(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStatus string
		wantHint   bool
	}{
		{"by default, like the dashboard", []string{"weather"}, "online", true},
		{"with --status all", []string{"weather", "--status", "all"}, "all", false},
		{"with --status offline", []string{"weather", "--status", "offline"}, "offline", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotStatus string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotStatus = r.URL.Query().Get("status")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(searchFixture))
			}))
			t.Cleanup(srv.Close)
			out, err := runSearchAgainst(t, srv.URL, "bk_test", tc.args...)
			if err != nil {
				t.Fatalf("search: %v\n%s", err, out)
			}
			if gotStatus != tc.wantStatus {
				t.Errorf("status sent = %q, want %q", gotStatus, tc.wantStatus)
			}
			if got := strings.Contains(out, "Add --status all"); got != tc.wantHint {
				t.Errorf("output names --status all = %v, want %v:\n%s", got, tc.wantHint, out)
			}
		})
	}
}

func TestSearchRejectsAnUnknownStatus(t *testing.T) {
	srv := registryServer(t, http.StatusOK, searchFixture)
	_, err := runSearchAgainst(t, srv.URL, "bk_test", "weather", "--status", "busy")
	if err == nil || !strings.Contains(err.Error(), "must be one of: online, offline, all") {
		t.Errorf("error = %v, want it to list the accepted statuses", err)
	}
}

func TestSearchCursorFlagFetchesThatPage(t *testing.T) {
	var gotCursor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCursor = r.URL.Query().Get("cursor")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(searchFixture))
	}))
	t.Cleanup(srv.Close)
	if _, err := runSearchAgainst(t, srv.URL, "bk_test", "weather", "--cursor", "c2"); err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotCursor != "c2" {
		t.Errorf("cursor sent = %q, want c2", gotCursor)
	}
}

// Expected values follow the Windows argv rules by hand, so each must parse back to its input.
// "" means unquotable: the caller falls back to a --cursor hint.
func TestWindowsQuoteSurvivesCmdAndPowerShell(t *testing.T) {
	cases := map[string]string{
		`weather`:  `weather`,
		`a b`:      `"a b"`,
		`C:\`:      `"C:\\"`,
		`a\b c`:    `"a\b c"`,
		`ends\\ `:  `"ends\\ "`,
		`two\\`:    `"two\\\\"`,
		``:         `""`,
		`say "hi"`: "",
		`cost $5`:  "",
		"a`b":      "",
		`50%`:      "",
	}
	for in, want := range cases {
		if got := windowsQuote(in); got != want {
			t.Errorf("windowsQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextPageCommandRepeatsTheSearch(t *testing.T) {
	cases := []struct {
		name  string
		page  nextPage
		quote func(string) string
		want  string
	}{
		{
			"POSIX quoting",
			nextPage{args: []string{"customer support", "it's"}, cursor: "abc="},
			posixQuote,
			`blocks search --cursor abc= -- 'customer support' 'it'\''s'`,
		},
		{
			"Windows quoting",
			nextPage{args: []string{"customer support", `C:\`}, cursor: "abc="},
			windowsQuote,
			`blocks search --cursor abc= -- "customer support" "C:\\"`,
		},
		{
			"a word no Windows shell can quote falls back to the cursor",
			nextPage{args: []string{`"customer support"`}, cursor: "abc="},
			windowsQuote,
			`repeat this search with --cursor abc=`,
		},
		{
			// Without --profile the next page would hit another deployment with this one's cursor.
			"keeps the deployment and page size",
			nextPage{args: []string{"weather"}, profile: "acme", limit: 5, cursor: "c2"},
			posixQuote,
			`blocks search --profile acme --limit 5 --cursor c2 -- weather`,
		},
		{
			"a query word starting with a dash stays a query word",
			nextPage{args: []string{"-draft"}, cursor: "c2"},
			posixQuote,
			`blocks search --cursor c2 -- -draft`,
		},
		{
			// Without --status the next page would switch to online agents mid-listing.
			"keeps a non-default status",
			nextPage{args: []string{"weather"}, status: agentsearch.StatusAll, cursor: "c2"},
			posixQuote,
			`blocks search --status all --cursor c2 -- weather`,
		},
		{
			"leaves the default status implied",
			nextPage{args: []string{"weather"}, status: agentsearch.StatusOnline, cursor: "c2"},
			posixQuote,
			`blocks search --cursor c2 -- weather`,
		},
		{"no cursor means no next page", nextPage{args: []string{"weather"}}, posixQuote, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.page.command(tc.quote); got != tc.want {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchAnonymousSaysPrivateAgentsNeedLogin(t *testing.T) {
	srv := registryServer(t, http.StatusOK, searchFixture)
	out, err := runSearchAgainst(t, srv.URL, "", "weather")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Showing public agents only") || !strings.Contains(out, "blocks login") {
		t.Errorf("anonymous output does not explain the login:\n%s", out)
	}
}

func TestSearchNoResultsSuggestsBroadening(t *testing.T) {
	srv := registryServer(t, http.StatusOK, `{"agents":[]}`)
	out, err := runSearchAgainst(t, srv.URL, "bk_test", "zzzz")
	if err != nil {
		t.Fatalf("no results must not be an error: %v", err)
	}
	for _, want := range []string{`No online agents match "zzzz"`, "broader", "browse the catalog", "Add --status all"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSearchJSONCarriesGeneratorFields(t *testing.T) {
	srv := registryServer(t, http.StatusOK, strings.Replace(searchFixture, "\n]}", `],"next":"c2"}`, 1))
	out, err := runSearchAgainst(t, srv.URL, "bk_test", "weather", "--json")
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	var got struct {
		Query         string `json:"query"`
		Authenticated bool   `json:"authenticated"`
		Next          string `json:"next"`
		Agents        []struct {
			AgentName   string `json:"agentName"`
			DisplayName string `json:"displayName"`
			Summary     string `json:"summary"`
			Visibility  string `json:"visibility"`
			Online      *bool  `json:"online"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if got.Query != "weather" || !got.Authenticated || got.Next != "c2" || len(got.Agents) != 2 {
		t.Fatalf("envelope = %+v", got)
	}
	first := got.Agents[0]
	if first.AgentName != "weather" || first.DisplayName != "Weather" || first.Visibility != "private" || first.Online == nil || !*first.Online || first.Summary != "Forecasts by city" {
		t.Errorf("agents[0] = %+v", first)
	}
}

func TestSearchSaysWhenOperatorsCouldNotBeParsed(t *testing.T) {
	srv := registryServer(t, http.StatusOK, strings.Replace(searchFixture, "\n]}", `],"searchParseError":true}`, 1))

	out, err := runSearchAgainst(t, srv.URL, "bk_test", `"weather`)
	if err != nil {
		t.Fatalf("search: %v\n%s", err, out)
	}
	if !strings.Contains(out, searchParseErrorNotice) {
		t.Errorf("output does not say the operators were not parsed:\n%s", out)
	}

	out, err = runSearchAgainst(t, srv.URL, "bk_test", `"weather`, "--json")
	if err != nil {
		t.Fatalf("search --json: %v\n%s", err, out)
	}
	var got struct {
		SearchParseError bool `json:"searchParseError"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.SearchParseError {
		t.Errorf("JSON does not carry searchParseError (err %v):\n%s", err, out)
	}
}

func TestSearchFailuresNameTheirCause(t *testing.T) {
	rejected := registryServer(t, http.StatusUnauthorized, `{"error":"invalid key"}`)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	cases := []struct {
		name, backend, want string
	}{
		{"rejected credential", rejected.URL, "credentials were rejected"},
		{"unreachable backend", closed.URL, "cannot reach"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runSearchAgainst(t, tc.backend, "bk_test", "weather")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestMakeAgentSuggestFn(t *testing.T) {
	t.Run("rows carry the canonical name and visibility", func(t *testing.T) {
		srv := registryServer(t, http.StatusOK, searchFixture)
		got, err := makeAgentSuggestFn(blocksapi.NewClient(srv.URL, "bk_test"))(context.Background(), "weather")
		if err != nil {
			t.Fatalf("suggest fn: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d suggestions, want 2", len(got))
		}
		if got[0].Value != "weather" || got[0].Label != "Weather · private · online" {
			t.Errorf("suggestion = %+v", got[0])
		}
	})

	t.Run("a failure keeps free-text entry and says why", func(t *testing.T) {
		srv := registryServer(t, http.StatusUnauthorized, `{"error":"invalid key"}`)
		_, err := makeAgentSuggestFn(blocksapi.NewClient(srv.URL, "bk_test"))(context.Background(), "weather")
		if err == nil || !strings.Contains(err.Error(), "credentials were rejected") || !strings.Contains(err.Error(), "type an agent name") {
			t.Errorf("error = %v", err)
		}
	})
}
