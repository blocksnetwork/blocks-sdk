package deploy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// fastPoll temporarily shortens the Cloudflare poll interval. Tests use this
// to avoid a 5-second sleep per deployment poll.
func fastPoll(t *testing.T) {
	t.Helper()
	orig := cfPollInterval
	cfPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { cfPollInterval = orig })
}

func newCFCreds(token string) *auth.ProviderCredentials {
	return &auth.ProviderCredentials{
		Provider:    "cloudflare",
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}
}

// cfMockState captures call counts so each test can assert which legs of the
// manifest-first protocol fired.
type cfMockState struct {
	accountsGET      atomic.Int32
	projectGET       atomic.Int32
	projectCreate    atomic.Int32
	uploadTokenGET   atomic.Int32
	checkMissingPOST atomic.Int32
	uploadPOST       atomic.Int32
	upsertPOST       atomic.Int32
	deploymentPOST   atomic.Int32
	deploymentGET    atomic.Int32

	// Captured payloads.
	uploadedHashes  []string
	upsertedHashes  []string
	manifestSent    map[string]string
	checkMissingReq []string
	uploadedBodies  map[string][]byte
	// contentType captured per uploaded hash, from the payload metadata.
	uploadedContentTypes map[string]string
}

// newCFMockServer wires a happy-path Cloudflare mock. The accounts/projects/
// upload-token endpoints succeed; check-missing reports a configurable subset
// of hashes as missing; upload+upsert succeed; deployment creation returns a
// pending deployment that the GET endpoint flips to deploy/success on the
// first poll. fixtureMissing is the list of hashes to claim are missing.
func newCFMockServer(t *testing.T, state *cfMockState, missingHashesFn func([]string) []string) *httptest.Server {
	t.Helper()
	state.uploadedBodies = map[string][]byte{}
	state.uploadedContentTypes = map[string]string{}

	mux := http.NewServeMux()

	mux.HandleFunc("/client/v4/accounts", func(w http.ResponseWriter, r *http.Request) {
		state.accountsGET.Add(1)
		writeJSON(w, map[string]any{
			"success": true,
			"result":  []map[string]string{{"id": "acct-001"}},
		})
	})

	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web", func(w http.ResponseWriter, r *http.Request) {
		state.projectGET.Add(1)
		writeJSON(w, map[string]any{
			"success": true,
			"result":  map[string]any{"name": "web", "subdomain": "web-prod.pages.dev"},
		})
	})

	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/upload-token", func(w http.ResponseWriter, r *http.Request) {
		state.uploadTokenGET.Add(1)
		writeJSON(w, map[string]any{
			"success": true,
			"result":  map[string]any{"jwt": "upload-jwt-001"},
		})
	})

	mux.HandleFunc("/client/v4/pages/assets/check-missing", func(w http.ResponseWriter, r *http.Request) {
		state.checkMissingPOST.Add(1)
		var body struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		state.checkMissingReq = append([]string(nil), body.Hashes...)
		missing := body.Hashes
		if missingHashesFn != nil {
			missing = missingHashesFn(body.Hashes)
		}
		writeJSON(w, map[string]any{
			"success": true,
			"result":  missing,
		})
	})

	mux.HandleFunc("/client/v4/pages/assets/upload", func(w http.ResponseWriter, r *http.Request) {
		state.uploadPOST.Add(1)
		var payloads []struct {
			Key      string `json:"key"`
			Value    string `json:"value"`
			Base64   bool   `json:"base64"`
			Metadata struct {
				ContentType string `json:"contentType"`
			} `json:"metadata"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payloads)
		for _, p := range payloads {
			state.uploadedHashes = append(state.uploadedHashes, p.Key)
			state.uploadedContentTypes[p.Key] = p.Metadata.ContentType
			if p.Base64 {
				if data, err := base64.StdEncoding.DecodeString(p.Value); err == nil {
					state.uploadedBodies[p.Key] = data
				}
			} else {
				state.uploadedBodies[p.Key] = []byte(p.Value)
			}
		}
		writeJSON(w, map[string]any{"success": true})
	})

	mux.HandleFunc("/client/v4/pages/assets/upsert-hashes", func(w http.ResponseWriter, r *http.Request) {
		state.upsertPOST.Add(1)
		var body struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		state.upsertedHashes = append([]string(nil), body.Hashes...)
		writeJSON(w, map[string]any{"success": true})
	})

	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/deployments", func(w http.ResponseWriter, r *http.Request) {
		state.deploymentPOST.Add(1)
		// The deployment create endpoint takes multipart/form-data with a
		// `manifest` form field (JSON-encoded path→hash), not a JSON body.
		var manifest map[string]string
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("deployment POST is not multipart/form-data: %v", err)
		} else {
			_ = json.Unmarshal([]byte(r.FormValue("manifest")), &manifest)
		}
		state.manifestSent = manifest
		writeJSON(w, map[string]any{
			"success": true,
			"result": map[string]any{
				"id":        "dep-001",
				"subdomain": "web-abc123",
				"latest_stage": map[string]string{
					"name":   "build",
					"status": "active",
				},
			},
		})
	})

	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/deployments/dep-001", func(w http.ResponseWriter, r *http.Request) {
		state.deploymentGET.Add(1)
		writeJSON(w, map[string]any{
			"success": true,
			"result": map[string]any{
				"id": "dep-001",
				"latest_stage": map[string]string{
					"name":   "deploy",
					"status": "success",
				},
			},
		})
	})

	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeAssetsDir creates a fresh <tmp>/web/<sub>/web directory tree so that
// projectName = slugify(filepath.Base(filepath.Dir(assetsDir))) resolves to
// "web" — keeping the mock-server route table small and stable.
func writeAssetsDir(t *testing.T, files map[string]string) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "web")
	projectDir := filepath.Join(parent, "web")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(projectDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir
}

// TestCloudflareUpload_HappyPath_AllMissing exercises the full manifest-first
// flow when Cloudflare reports every hash as missing (cold deploy).
func TestCloudflareUpload_HappyPath_AllMissing(t *testing.T) {
	fastPoll(t)
	state := &cfMockState{}
	ts := newCFMockServer(t, state, nil) // nil = all hashes missing
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{
		"index.html": "<html>hi</html>",
		"app.js":     "console.log(1)",
	})

	url, err := cloudflareUploadAt(context.Background(), newCFCreds("api-token"), assetsDir, ts.URL+"/client/v4")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	// URL comes from the project's production subdomain, not a guessed
	// "<projectName>.pages.dev".
	if url != "https://web-prod.pages.dev" {
		t.Errorf("URL = %q, want https://web-prod.pages.dev (project subdomain)", url)
	}

	// Every uploaded asset must carry a contentType (else Cloudflare serves
	// octet-stream and the browser downloads the page). index.html → text/html.
	sawHTML := false
	for _, ct := range state.uploadedContentTypes {
		if ct == "" {
			t.Error("uploaded asset missing metadata.contentType")
		}
		if strings.HasPrefix(ct, "text/html") {
			sawHTML = true
		}
	}
	if !sawHTML {
		t.Errorf("no asset uploaded with a text/html content type; got %v", state.uploadedContentTypes)
	}

	if state.accountsGET.Load() != 1 {
		t.Errorf("accounts GET = %d, want 1", state.accountsGET.Load())
	}
	if state.uploadTokenGET.Load() != 1 {
		t.Errorf("upload-token GET = %d, want 1", state.uploadTokenGET.Load())
	}
	if state.checkMissingPOST.Load() != 1 {
		t.Errorf("check-missing POST = %d, want 1", state.checkMissingPOST.Load())
	}
	if state.uploadPOST.Load() != 1 {
		t.Errorf("upload POST = %d, want 1", state.uploadPOST.Load())
	}
	if state.upsertPOST.Load() != 1 {
		t.Errorf("upsert-hashes POST = %d, want 1", state.upsertPOST.Load())
	}
	if state.deploymentPOST.Load() != 1 {
		t.Errorf("deployments POST = %d, want 1", state.deploymentPOST.Load())
	}
	if state.deploymentGET.Load() < 1 {
		t.Errorf("deployments GET = %d, want >= 1", state.deploymentGET.Load())
	}

	if len(state.manifestSent) != 2 {
		t.Errorf("manifest = %v, want 2 entries", state.manifestSent)
	}
	for path := range state.manifestSent {
		if !strings.HasPrefix(path, "/") {
			t.Errorf("manifest path %q should be absolute (/-prefixed)", path)
		}
	}
	if len(state.uploadedBodies) != 2 {
		t.Errorf("uploaded %d bodies, want 2", len(state.uploadedBodies))
	}
	for _, body := range state.uploadedBodies {
		s := string(body)
		if s != "<html>hi</html>" && s != "console.log(1)" {
			t.Errorf("unexpected uploaded body %q", s)
		}
	}
}

// TestCloudflareUpload_HappyPath_NoneMissing skips the upload leg when every
// hash already exists on the partner side.
func TestCloudflareUpload_HappyPath_NoneMissing(t *testing.T) {
	fastPoll(t)
	state := &cfMockState{}
	ts := newCFMockServer(t, state, func(hashes []string) []string { return []string{} })
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html>hi</html>"})

	_, err := cloudflareUploadAt(context.Background(), newCFCreds("api-token"), assetsDir, ts.URL+"/client/v4")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if state.uploadPOST.Load() != 0 {
		t.Errorf("upload POST = %d, want 0 (nothing missing)", state.uploadPOST.Load())
	}
	if state.upsertPOST.Load() != 1 {
		t.Errorf("upsert-hashes POST = %d, want 1", state.upsertPOST.Load())
	}
	if state.deploymentPOST.Load() != 1 {
		t.Errorf("deployments POST = %d, want 1", state.deploymentPOST.Load())
	}
}

// TestCloudflareUpload_AccountsAuthDenied surfaces a 401 from the accounts
// endpoint as a clear authentication error.
func TestCloudflareUpload_AccountsAuthDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})

	_, err := cloudflareUploadAt(context.Background(), newCFCreds("bad"), assetsDir, ts.URL+"/client/v4")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "authentication denied") {
		t.Errorf("error %q should mention authentication denied", err)
	}
}

// TestCloudflareUpload_CheckMissingFailure surfaces a 5xx from check-missing.
func TestCloudflareUpload_CheckMissingFailure(t *testing.T) {
	state := &cfMockState{}
	ts := newCFMockServer(t, state, nil)
	defer ts.Close()

	// Wrap the mux to fail the check-missing endpoint specifically.
	wrapper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pages/assets/check-missing") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"success":false,"errors":["boom"]}`)
			return
		}
		// Forward everything else to the happy-path mock.
		http.Redirect(w, r, ts.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer wrapper.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	_, err := cloudflareUploadAt(context.Background(), newCFCreds("api-token"), assetsDir, wrapper.URL+"/client/v4")
	if err == nil {
		t.Fatal("expected check-missing failure")
	}
	if !strings.Contains(err.Error(), "check missing") {
		t.Errorf("error %q should mention 'check missing'", err)
	}
}

// TestCloudflareUpload_UploadFailure surfaces a 5xx from /pages/assets/upload.
func TestCloudflareUpload_UploadFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": []map[string]string{{"id": "acct-001"}}})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"name": "web"}})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/upload-token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"jwt": "j"}})
	})
	mux.HandleFunc("/client/v4/pages/assets/check-missing", func(w http.ResponseWriter, r *http.Request) {
		// Return every hash as missing so the upload leg runs.
		var b struct {
			Hashes []string `json:"hashes"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		writeJSON(w, map[string]any{"success": true, "result": b.Hashes})
	})
	mux.HandleFunc("/client/v4/pages/assets/upload", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"success":false}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	_, err := cloudflareUploadAt(context.Background(), newCFCreds("api-token"), assetsDir, ts.URL+"/client/v4")
	if err == nil {
		t.Fatal("expected upload failure")
	}
	if !strings.Contains(err.Error(), "upload assets") {
		t.Errorf("error %q should mention 'upload assets'", err)
	}
}

// TestCloudflareUpload_DeploymentFailure surfaces a poll-stage failure.
func TestCloudflareUpload_DeploymentFailure(t *testing.T) {
	fastPoll(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/client/v4/accounts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": []map[string]string{{"id": "acct-001"}}})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"name": "web"}})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/upload-token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": map[string]any{"jwt": "j"}})
	})
	mux.HandleFunc("/client/v4/pages/assets/check-missing", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true, "result": []string{}})
	})
	mux.HandleFunc("/client/v4/pages/assets/upsert-hashes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"success": true})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/deployments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"success": true,
			"result": map[string]any{
				"id":        "dep-fail",
				"subdomain": "web",
				"latest_stage": map[string]string{
					"name":   "build",
					"status": "active",
				},
			},
		})
	})
	mux.HandleFunc("/client/v4/accounts/acct-001/pages/projects/web/deployments/dep-fail", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"success": true,
			"result": map[string]any{
				"id": "dep-fail",
				"latest_stage": map[string]string{
					"name":   "deploy",
					"status": "failure",
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	_, err := cloudflareUploadAt(context.Background(), newCFCreds("api-token"), assetsDir, ts.URL+"/client/v4")
	if err == nil {
		t.Fatal("expected deployment failure")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error %q should mention failure", err)
	}
}

// TestCfClassifyStage verifies the polling-decision logic.
func TestCfClassifyStage(t *testing.T) {
	tests := []struct {
		name   string
		stage  string
		status string
		want   cfDeploymentStage
	}{
		{"deploy/success → success", "deploy", "success", cfStageSuccess},
		{"Deploy/Success (case-insensitive)", "Deploy", "Success", cfStageSuccess},
		{"queued/success → pending", "queued", "success", cfStagePending},
		{"build/success → pending", "build", "success", cfStagePending},
		{"clone_repo/success → pending", "clone_repo", "success", cfStagePending},
		{"queued/idle → pending", "queued", "idle", cfStagePending},
		{"build/active → pending", "build", "active", cfStagePending},
		{"deploy/active → pending", "deploy", "active", cfStagePending},
		{"build/failure → failure", "build", "failure", cfStageFailure},
		{"deploy/failure → failure", "deploy", "failure", cfStageFailure},
		{"build/canceled → failure", "build", "canceled", cfStageFailure},
		{"deploy/cancelled (Brit) → failure", "deploy", "cancelled", cfStageFailure},
		{"empty → pending", "", "", cfStagePending},
		{"unknown stage / unknown status → pending", "wat", "wat", cfStagePending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cfClassifyStage(tc.stage, tc.status)
			if got != tc.want {
				t.Errorf("cfClassifyStage(%q, %q) = %d, want %d", tc.stage, tc.status, got, tc.want)
			}
		})
	}
}

// TestSlugify verifies the slugify helper used for project name derivation.
func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"web", "web"},
		{"My Project", "my-project"},
		{"foo_bar.baz", "foo-bar-baz"},
		{"HELLO-WORLD", "hello-world"},
		{"---leading---", "leading"},
	}
	for _, tc := range tests {
		got := slugify(tc.input)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestCfAssetHash guards the Cloudflare Pages asset-key scheme: a 32-char
// (not 64-char sha256) hex blake3 digest whose input includes the file
// extension. A regression here produces deploys that upload but then 500.
func TestCfAssetHash(t *testing.T) {
	content := []byte("<!doctype html><html></html>")

	h := cfAssetHash(content, "index.html")
	if len(h) != 32 {
		t.Fatalf("hash len = %d, want 32 (Cloudflare uses 32-char blake3, not 64-char sha256)", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash %q is not lowercase hex", h)
		}
	}
	// Extension is part of the hash input.
	if cfAssetHash(content, "index.html") == cfAssetHash(content, "index.css") {
		t.Error("hash must depend on the file extension")
	}
	// Deterministic for the same content + extension.
	if cfAssetHash(content, "a/index.html") != cfAssetHash(content, "b/index.html") {
		t.Error("hash must depend only on content + extension, not the full path")
	}
}
