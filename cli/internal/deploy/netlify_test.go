package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// fastNetlifyPoll temporarily shortens the Netlify poll interval. Tests use
// this to avoid a 5-second sleep per deployment poll.
func fastNetlifyPoll(t *testing.T) {
	t.Helper()
	orig := netlifyPollInterval
	netlifyPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { netlifyPollInterval = orig })
}

func newNetlifyCreds(token string) *auth.ProviderCredentials {
	return &auth.ProviderCredentials{
		Provider:    "netlify",
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}
}

// netlifyMockState captures call counts for assertion.
type netlifyMockState struct {
	sitesGET    atomic.Int32
	sitesPOST   atomic.Int32
	deploysPOST atomic.Int32
	deploysGET  atomic.Int32
	siteGET     atomic.Int32
}

// newNetlifyMockServer wires a happy-path Netlify mock. List-sites returns
// empty so create-site fires; create-site returns a fixed site id; deploy
// returns a pending deploy; poll flips to "ready" immediately and the
// site lookup returns the canonical site URL.
func newNetlifyMockServer(t *testing.T, state *netlifyMockState) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			state.sitesGET.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case http.MethodPost:
			state.sitesPOST.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":   "site-abc",
				"name": "web",
				"url":  "https://web.netlify.app",
			})
		default:
			t.Logf("unhandled netlify mock: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	mux.HandleFunc("/api/v1/sites/site-abc/deploys", func(w http.ResponseWriter, r *http.Request) {
		state.deploysPOST.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "deploy-xyz",
			"state": "uploading",
		})
	})

	mux.HandleFunc("/api/v1/deploys/deploy-xyz", func(w http.ResponseWriter, r *http.Request) {
		state.deploysGET.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state":      "ready",
			"site_id":    "site-abc",
			"deploy_url": "https://deploy-xyz--web.netlify.app",
		})
	})

	mux.HandleFunc("/api/v1/sites/site-abc", func(w http.ResponseWriter, r *http.Request) {
		state.siteGET.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":  "site-abc",
			"url": "https://web.netlify.app",
		})
	})

	return httptest.NewServer(mux)
}

// TestNetlifyUpload_HappyPath exercises netlifyUploadAt end-to-end through
// the real adapter. Asserts each leg of the list/create-site → deploy →
// poll → site-lookup pipeline fired.
func TestNetlifyUpload_HappyPath(t *testing.T) {
	fastNetlifyPoll(t)
	state := &netlifyMockState{}
	ts := newNetlifyMockServer(t, state)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{
		"index.html": "<html>hi</html>",
		"app.js":     "console.log(1)",
	})

	url, err := netlifyUploadAt(context.Background(), newNetlifyCreds("api-token"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("netlifyUploadAt: %v", err)
	}
	if url != "https://web.netlify.app" {
		t.Errorf("URL = %q, want https://web.netlify.app", url)
	}

	if state.sitesGET.Load() != 1 {
		t.Errorf("sitesGET = %d, want 1", state.sitesGET.Load())
	}
	if state.sitesPOST.Load() != 1 {
		t.Errorf("sitesPOST = %d, want 1 (no existing site → create)", state.sitesPOST.Load())
	}
	if state.deploysPOST.Load() != 1 {
		t.Errorf("deploysPOST = %d, want 1", state.deploysPOST.Load())
	}
	if state.deploysGET.Load() == 0 {
		t.Errorf("deploysGET = 0, want ≥1")
	}
	if state.siteGET.Load() != 1 {
		t.Errorf("siteGET = %d, want 1 (canonical-URL lookup)", state.siteGET.Load())
	}
}

// TestNetlifyUpload_ReusesExistingSite verifies that when list-sites returns
// a matching site name, we do NOT create a new one.
func TestNetlifyUpload_ReusesExistingSite(t *testing.T) {
	fastNetlifyPoll(t)
	state := &netlifyMockState{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			state.sitesGET.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "site-existing", "name": "web", "url": "https://web.netlify.app"},
			})
		case http.MethodPost:
			state.sitesPOST.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/api/v1/sites/site-existing/deploys", func(w http.ResponseWriter, r *http.Request) {
		state.deploysPOST.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "dep-rerun", "state": "uploading"})
	})
	mux.HandleFunc("/api/v1/deploys/dep-rerun", func(w http.ResponseWriter, r *http.Request) {
		state.deploysGET.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state":   "ready",
			"site_id": "site-existing",
		})
	})
	mux.HandleFunc("/api/v1/sites/site-existing", func(w http.ResponseWriter, r *http.Request) {
		state.siteGET.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://web.netlify.app"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})

	url, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("netlifyUploadAt: %v", err)
	}
	if url != "https://web.netlify.app" {
		t.Errorf("URL = %q, want https://web.netlify.app", url)
	}
	if state.sitesPOST.Load() != 0 {
		t.Errorf("sitesPOST = %d, want 0 (existing site reused, no create)", state.sitesPOST.Load())
	}
}

// TestNetlifyUpload_NameCollisionRetries verifies that a 422 on the raw
// site name triggers a retry with a suffixed name rather than failing
// the deploy.
func TestNetlifyUpload_NameCollisionRetries(t *testing.T) {
	fastNetlifyPoll(t)
	var createNames []string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case http.MethodPost:
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			createNames = append(createNames, body.Name)
			// First (raw) name is taken globally → 422. Suffixed retry wins.
			if len(createNames) == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"errors":{"subdomain":["must be unique"]}}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":   "site-2",
				"name": body.Name,
				"url":  "https://" + body.Name + ".netlify.app",
			})
		}
	})
	mux.HandleFunc("/api/v1/sites/site-2/deploys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "dep-2", "state": "uploading"})
	})
	mux.HandleFunc("/api/v1/deploys/dep-2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "ready", "site_id": "site-2"})
	})
	mux.HandleFunc("/api/v1/sites/site-2", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://web-x1.netlify.app"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	url, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("netlifyUploadAt: %v", err)
	}
	if len(createNames) < 2 {
		t.Fatalf("expected a retry after 422, got create attempts: %v", createNames)
	}
	if createNames[0] == createNames[1] {
		t.Errorf("retry reused the same name %q; expected a unique suffix", createNames[1])
	}
	if url == "" {
		t.Error("expected a non-empty site URL after retry")
	}
}

// TestNetlifyUpload_CreateHardErrorSurfacesBody verifies that a non-422
// create-site failure surfaces the Netlify response body (not just the
// status code) for triagability — parity with the Cloudflare deploy path.
func TestNetlifyUpload_CreateHardErrorSurfacesBody(t *testing.T) {
	const errBody = `{"errors":{"name":["is invalid for some reason"]}}`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(errBody))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	_, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err == nil {
		t.Fatal("expected error for 500 on create")
	}
	if !strings.Contains(err.Error(), "create site returned HTTP 500") {
		t.Errorf("error %q should mention 'create site returned HTTP 500'", err.Error())
	}
	if !strings.Contains(err.Error(), errBody) {
		t.Errorf("error %q should include the response body %q", err.Error(), errBody)
	}
}

// TestNetlifyNextLink unit-tests the Link-header pagination parser.
func TestNetlifyNextLink(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"no next", `<https://api.netlify.com/api/v1/sites?page=1>; rel="prev"`, ""},
		{
			"next only",
			`<https://api.netlify.com/api/v1/sites?page=2&per_page=100>; rel="next"`,
			"https://api.netlify.com/api/v1/sites?page=2&per_page=100",
		},
		{
			"next among many",
			`<https://api.netlify.com/api/v1/sites?page=1>; rel="prev", <https://api.netlify.com/api/v1/sites?page=3>; rel="next", <https://api.netlify.com/api/v1/sites?page=9>; rel="last"`,
			"https://api.netlify.com/api/v1/sites?page=3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netlifyNextLink(tc.header); got != tc.want {
				t.Errorf("netlifyNextLink(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestNetlifyUpload_AuthDenied verifies that a 401 from list-sites surfaces
// the authentication-denied error.
func TestNetlifyUpload_AuthDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	_, err := netlifyUploadAt(context.Background(), newNetlifyCreds("bad"), assetsDir, ts.URL)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "authentication denied") {
		t.Errorf("error %q should mention 'authentication denied'", err.Error())
	}
}

// TestNetlifyUpload_DeployErrorState surfaces a failed deployment when the
// poll response reports state=error.
func TestNetlifyUpload_DeployErrorState(t *testing.T) {
	fastNetlifyPoll(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "site-fail", "url": "https://fail.netlify.app"})
	})
	mux.HandleFunc("/api/v1/sites/site-fail/deploys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "dep-fail", "state": "uploading"})
	})
	mux.HandleFunc("/api/v1/deploys/dep-fail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "error"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	_, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err == nil {
		t.Fatal("expected error for state=error")
	}
	if !strings.Contains(err.Error(), "deployment dep-fail failed") {
		t.Errorf("error %q should mention 'deployment dep-fail failed'", err.Error())
	}
}

// TestNetlifyUpload_PrefersSSLURL verifies the deploy returns the https
// ssl_url even when the site's plain url field is http://, so the deployed
// URL passes the webApp validator and the card update is not skipped.
func TestNetlifyUpload_PrefersSSLURL(t *testing.T) {
	fastNetlifyPoll(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "site-ssl", "name": "web", "url": "http://web.netlify.app"})
		}
	})
	mux.HandleFunc("/api/v1/sites/site-ssl/deploys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "deploy-ssl", "state": "uploading"})
	})
	mux.HandleFunc("/api/v1/deploys/deploy-ssl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": "ready", "site_id": "site-ssl",
			"deploy_url": "http://deploy-ssl--web.netlify.app", "deploy_ssl_url": "https://deploy-ssl--web.netlify.app",
		})
	})
	mux.HandleFunc("/api/v1/sites/site-ssl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "site-ssl", "url": "http://web.netlify.app", "ssl_url": "https://web.netlify.app",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	got, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("netlifyUploadAt: %v", err)
	}
	if got != "https://web.netlify.app" {
		t.Errorf("URL = %q, want https://web.netlify.app (ssl_url)", got)
	}
	if err := ValidateWebAppURL(got); err != nil {
		t.Errorf("deployed URL must pass the webApp validator: %v", err)
	}
}

// TestNetlifyUpload_DeploySSLFallbackWhenSiteLookupFails verifies that when
// the site lookup fails, the poll falls back to deploy_ssl_url (https), not
// the http deploy_url.
func TestNetlifyUpload_DeploySSLFallbackWhenSiteLookupFails(t *testing.T) {
	fastNetlifyPoll(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sites", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "site-f", "name": "web", "url": "http://web.netlify.app"})
		}
	})
	mux.HandleFunc("/api/v1/sites/site-f/deploys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "deploy-f", "state": "uploading"})
	})
	mux.HandleFunc("/api/v1/deploys/deploy-f", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": "ready", "site_id": "site-f",
			"deploy_url": "http://deploy-f--web.netlify.app", "deploy_ssl_url": "https://deploy-f--web.netlify.app",
		})
	})
	// Site lookup 500s → poll returns its deploy fallback.
	mux.HandleFunc("/api/v1/sites/site-f", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	got, err := netlifyUploadAt(context.Background(), newNetlifyCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("netlifyUploadAt: %v", err)
	}
	if got != "https://deploy-f--web.netlify.app" {
		t.Errorf("URL = %q, want https://deploy-f--web.netlify.app (deploy_ssl_url fallback)", got)
	}
}
