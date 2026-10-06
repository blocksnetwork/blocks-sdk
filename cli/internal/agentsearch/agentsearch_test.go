package agentsearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

func serve(t *testing.T, status int, body string, gotQuery *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != agentsPath {
			http.NotFound(w, r)
			return
		}
		if gotQuery != nil {
			*gotQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchQueryParameters(t *testing.T) {
	cases := []struct {
		name   string
		apiKey string
		query  Query
		want   map[string]string
		absent []string
	}{
		{
			name:   "authenticated search includes the caller's private agents",
			apiKey: "bk_test", query: Query{Text: " weather ", Limit: 10},
			want:   map[string]string{"q": "weather", "sort": "relevance", "listing": "public,private", "limit": "10"},
			absent: []string{"status"},
		},
		{
			name:   "anonymous search asks for the default public listing",
			query:  Query{Text: "weather", Limit: 10},
			want:   map[string]string{"q": "weather", "sort": "relevance"},
			absent: []string{"listing", "cursor"},
		},
		{
			name:   "empty query browses alphabetically",
			apiKey: "bk_test", query: Query{Limit: 20},
			want:   map[string]string{"sort": "name", "listing": "public,private"},
			absent: []string{"q"},
		},
		{
			name:   "a status filters the registry by it",
			apiKey: "bk_test", query: Query{Text: "weather", Limit: 10, Status: StatusOnline},
			want: map[string]string{"q": "weather", "status": "online"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got url.Values
			srv := serve(t, http.StatusOK, `{"agents":[]}`, &got)
			if _, err := Search(context.Background(), blocksapi.NewClient(srv.URL, tc.apiKey), tc.query); err != nil {
				t.Fatalf("Search: %v", err)
			}
			for k, v := range tc.want {
				if got.Get(k) != v {
					t.Errorf("%s = %q, want %q", k, got.Get(k), v)
				}
			}
			for _, k := range tc.absent {
				if got.Has(k) {
					t.Errorf("%s = %q, want absent", k, got.Get(k))
				}
			}
		})
	}
}

func TestSearchMapsRegistryRows(t *testing.T) {
	body := `{"agents":[
		{"agentName":"weather","displayName":"Weather","cardSummary":"Forecasts","description":"Long text","listing":"private","onlineCount":2},
		{"agentName":"weather_bot","displayName":"Weather Bot","description":"Falls back to description","listing":"public","onlineCount":0},
		{"agentName":"storm","displayName":"Storm","listing":"public"}
	],"next":"c2","searchParseError":true}`

	srv := serve(t, http.StatusOK, body, nil)
	res, err := Search(context.Background(), blocksapi.NewClient(srv.URL, "bk_test"), Query{Text: "weather", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !res.Authenticated {
		t.Error("Authenticated = false with an API key")
	}
	if res.Next != "c2" {
		t.Errorf("Next = %q, want the registry's cursor", res.Next)
	}
	if !res.ParseError {
		t.Error("ParseError = false, want the registry's searchParseError")
	}
	want := []struct{ name, listing, summary, availability string }{
		{"weather", ListingPrivate, "Forecasts", "online"},
		{"weather_bot", ListingPublic, "Falls back to description", "offline"},
		{"storm", ListingPublic, "", ""},
	}
	if len(res.Agents) != len(want) {
		t.Fatalf("got %d agents, want %d: %+v", len(res.Agents), len(want), res.Agents)
	}
	for i, w := range want {
		a := res.Agents[i]
		if a.AgentName != w.name || a.Listing != w.listing || a.Summary != w.summary || a.Availability() != w.availability {
			t.Errorf("agent[%d] = %+v (availability %q), want %+v", i, a, a.Availability(), w)
		}
	}
}

func TestSearchClassifiesFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   ErrorKind
	}{
		{"forbidden", http.StatusForbidden, KindAuth},
		{"server failure", http.StatusInternalServerError, KindServer},
		{"bad request", http.StatusBadRequest, KindServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, tc.status, `{"error":"nope"}`, nil)
			_, err := Search(context.Background(), blocksapi.NewClient(srv.URL, "bk_test"), Query{Text: "x", Limit: 10})
			if got := KindOf(err); got != tc.want {
				t.Errorf("KindOf(%v) = %d, want %d", err, got, tc.want)
			}
		})
	}

}
