package devserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveDevScript emits the JS payload that the partner page reads off
// `window.__BLOCKS_EMBED_DEV__`. The widget plumbs `backendBaseUrl` into
// the auth surface and `cdmUrl` into `TaskClient.create` so the SDK
// resolves PubNub keysets and `api.baseUrl` from the local backend
// instead of the production CDM.
func TestServeDevScript_PayloadShape(t *testing.T) {
	s := &Server{
		port:           5175,
		backendBaseURL: "http://localhost:3001",
		agents:         []string{"echo2"},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__blocks_embed_dev.js", nil)
	s.serveDevScript(rec, req)

	res := rec.Result()
	if got, want := res.StatusCode, 200; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := res.Header.Get("Content-Type"), "application/javascript"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := res.Header.Get("Cache-Control"), "no-store"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`window.__BLOCKS_EMBED_DEV__`,
		`backendBaseUrl: "http://localhost:3001"`,
		`cdmUrl: "http://localhost:3001/api/v1/cdm"`,
		`origin: "http://localhost:5175"`,
		`agents: ["echo2"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dev script payload missing %q\n--- payload ---\n%s", want, body)
		}
	}

	// devGrant must NOT be in the injected script — the per-origin
	// allowlist model is removed and `__BLOCKS_EMBED_DEV__.devGrant`
	// is no longer plumbed by the CLI or read by the widget.
	for _, forbidden := range []string{"devGrant", "expiresAt"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("dev script payload must not contain %q\n--- payload ---\n%s", forbidden, body)
		}
	}

	// Hot-reload SSE client must still be appended.
	if !strings.Contains(body, "EventSource") {
		t.Error("dev script payload missing the EventSource SSE client")
	}
}

// CDM URL is derived from backendBaseURL with trailing slashes stripped, so
// `blocks dev --port 5175` against any backendBaseURL form (with or without
// trailing slash) emits the same canonical CDM URL.
func TestServeDevScript_CdmUrlTrailingSlashStripped(t *testing.T) {
	cases := []struct {
		name           string
		backendBaseURL string
		want           string
	}{
		{"no trailing slash", "http://localhost:3001", `cdmUrl: "http://localhost:3001/api/v1/cdm"`},
		{"one trailing slash", "http://localhost:3001/", `cdmUrl: "http://localhost:3001/api/v1/cdm"`},
		{"many trailing slashes", "http://localhost:3001////", `cdmUrl: "http://localhost:3001/api/v1/cdm"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Server{
				port:           5175,
				backendBaseURL: c.backendBaseURL,
				agents:         []string{"echo2"},
			}
			rec := httptest.NewRecorder()
			s.serveDevScript(rec, httptest.NewRequest(http.MethodGet, "/__blocks_embed_dev.js", nil))
			body := rec.Body.String()
			if !strings.Contains(body, c.want) {
				t.Errorf("payload missing %q\n--- payload ---\n%s", c.want, body)
			}
		})
	}
}
