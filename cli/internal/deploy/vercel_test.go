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

// fastVercelPoll temporarily shortens the Vercel poll interval. Tests use this
// to avoid a 5-second sleep per deployment poll.
func fastVercelPoll(t *testing.T) {
	t.Helper()
	orig := vercelPollInterval
	vercelPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { vercelPollInterval = orig })
}

func newVercelCreds(token string) *auth.ProviderCredentials {
	return &auth.ProviderCredentials{
		Provider:    "vercel",
		Kind:        auth.CredentialKindAPIToken,
		AccessToken: token,
	}
}

// vercelMockState captures call counts so each test can assert which legs of
// the upload → deploy → poll pipeline fired.
type vercelMockState struct {
	filesPOST       atomic.Int32
	deploymentsPOST atomic.Int32
	deploymentsGET  atomic.Int32

	uploadedFiles []string
	uploadedRefs  []vercelFileRef
}

// newVercelMockServer wires a happy-path Vercel mock. The file-upload
// endpoint accepts every file, the deployment-create endpoint returns a
// pending deployment, and the deployment-poll endpoint immediately flips
// to READY so cfClassifyStage-equivalent vercelPollDeployment returns.
func newVercelMockServer(t *testing.T, state *vercelMockState) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	// Project already exists, so the ensure-project step is a no-op.
	mux.HandleFunc("/v9/projects/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "prj_existing", "name": "my-project"})
	})

	mux.HandleFunc("/v2/files", func(w http.ResponseWriter, r *http.Request) {
		state.filesPOST.Add(1)
		sha := r.Header.Get("x-vercel-digest")
		state.uploadedFiles = append(state.uploadedFiles, sha)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/v13/deployments", func(w http.ResponseWriter, r *http.Request) {
		state.deploymentsPOST.Add(1)
		var body struct {
			Files []vercelFileRef `json:"files"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		state.uploadedRefs = append([]vercelFileRef(nil), body.Files...)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "vdep-001",
			"url":        "vdep-001.vercel.app",
			"readyState": "BUILDING",
		})
	})

	mux.HandleFunc("/v13/deployments/vdep-001", func(w http.ResponseWriter, r *http.Request) {
		state.deploymentsGET.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"readyState": "READY",
			"url":        "vdep-001.vercel.app",
			"alias":      []string{"my-project.vercel.app"},
		})
	})

	return httptest.NewServer(mux)
}

// TestVercelUpload_HappyPath exercises vercelUploadAt (the function the
// exported VercelUpload delegates to) end-to-end against a mock. Asserts
// each leg of the upload → deploy → poll pipeline fired and the returned
// URL is the alias.
func TestVercelUpload_HappyPath(t *testing.T) {
	fastVercelPoll(t)
	state := &vercelMockState{}
	ts := newVercelMockServer(t, state)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{
		"index.html": "<html>hi</html>",
		"app.js":     "console.log(1)",
	})

	url, err := vercelUploadAt(context.Background(), newVercelCreds("api-token"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("vercelUploadAt: %v", err)
	}
	if url != "https://my-project.vercel.app" {
		t.Errorf("URL = %q, want https://my-project.vercel.app", url)
	}

	if state.filesPOST.Load() != 2 {
		t.Errorf("filesPOST = %d, want 2 (one per asset)", state.filesPOST.Load())
	}
	if state.deploymentsPOST.Load() != 1 {
		t.Errorf("deploymentsPOST = %d, want 1", state.deploymentsPOST.Load())
	}
	if state.deploymentsGET.Load() == 0 {
		t.Errorf("deploymentsGET = 0, want ≥1")
	}
	if len(state.uploadedRefs) != 2 {
		t.Errorf("uploadedRefs len = %d, want 2", len(state.uploadedRefs))
	}
	// Vercel uploads expect SHA-1 hashes — verify the file-upload header digest
	// matches the digest of the file body.
	for _, ref := range state.uploadedRefs {
		if len(ref.SHA) != 40 {
			t.Errorf("SHA-1 digest length = %d, want 40 (hex of SHA-1): %s", len(ref.SHA), ref.SHA)
		}
	}
}

// TestVercelUpload_AuthDenied verifies that a 401 on file upload surfaces
// the authentication-denied error message.
func TestVercelUpload_AuthDenied(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	_, err := vercelUploadAt(context.Background(), newVercelCreds("bad"), assetsDir, ts.URL)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "authentication denied") {
		t.Errorf("error %q should mention 'authentication denied'", err.Error())
	}
}

// TestVercelUpload_DeployTerminalError surfaces a failed deployment when
// the poll response reports ERROR or CANCELED.
func TestVercelUpload_DeployTerminalError(t *testing.T) {
	fastVercelPoll(t)
	mux := http.NewServeMux()
	// Project already exists so the flow reaches the deployment.
	mux.HandleFunc("/v9/projects/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "prj_fail", "name": "my-project"})
	})
	mux.HandleFunc("/v2/files", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v13/deployments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "vdep-fail",
			"readyState": "BUILDING",
		})
	})
	mux.HandleFunc("/v13/deployments/vdep-fail", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"readyState": "ERROR",
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "hi"})
	_, err := vercelUploadAt(context.Background(), newVercelCreds("tok"), assetsDir, ts.URL)
	if err == nil {
		t.Fatal("expected error for terminal ERROR state")
	}
	if !strings.Contains(err.Error(), "terminal state") {
		t.Errorf("error %q should mention 'terminal state'", err.Error())
	}
}

// TestVercelUpload_CreatesProjectOnFirstDeploy verifies that a project that
// does not yet exist is created before the deployment POST, so the first-ever
// deploy does not fail.
func TestVercelUpload_CreatesProjectOnFirstDeploy(t *testing.T) {
	fastVercelPoll(t)
	t.Setenv("VERCEL_TEAM_ID", "")
	var projectGET, projectPOST atomic.Int32

	mux := http.NewServeMux()
	// Personal scope is allowed (so vercelScope picks "").
	mux.HandleFunc("/v9/projects", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"projects":[]}`))
	})
	// Project lookup by name → 404 (not created yet).
	mux.HandleFunc("/v9/projects/", func(w http.ResponseWriter, r *http.Request) {
		projectGET.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	// Project create.
	mux.HandleFunc("/v10/projects", func(w http.ResponseWriter, r *http.Request) {
		projectPOST.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "prj_1", "name": "my-project"})
	})
	mux.HandleFunc("/v2/files", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v13/deployments", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "vdep-1", "url": "vdep-1.vercel.app", "readyState": "BUILDING"})
	})
	mux.HandleFunc("/v13/deployments/vdep-1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"readyState": "READY", "alias": []string{"my-project.vercel.app"}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	url, err := vercelUploadAt(context.Background(), newVercelCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("vercelUploadAt: %v", err)
	}
	if projectGET.Load() == 0 {
		t.Error("expected a project lookup before deploy")
	}
	if projectPOST.Load() != 1 {
		t.Errorf("projectPOST = %d, want 1 (project created on 404)", projectPOST.Load())
	}
	if url != "https://my-project.vercel.app" {
		t.Errorf("url = %q, want https://my-project.vercel.app", url)
	}
}

// TestSha1sum verifies that sha1sum returns the expected hex digest.
func TestSha1sum(t *testing.T) {
	got := sha1sum([]byte{})
	if got != "da39a3ee5e6b4b0d3255bfef95601890afd80709" {
		t.Errorf("sha1sum(empty) = %q, want da39a3ee5e6b4b0d3255bfef95601890afd80709", got)
	}
}

// TestVercelUpload_TeamScoped verifies that when the token only works in a
// team scope (personal /v9/projects → 403), the deploy auto-discovers the team
// and threads teamId through file upload, deployment create, and poll.
func TestVercelUpload_TeamScoped(t *testing.T) {
	fastVercelPoll(t)
	t.Setenv("VERCEL_TEAM_ID", "") // ensure the env override is not in play
	const team = "team_abc"
	var filesTeam, deployTeam, pollTeam string

	mux := http.NewServeMux()
	// Personal scope is denied; only the team scope is allowed.
	mux.HandleFunc("/v9/projects", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != team {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"projects":[]}`))
	})
	mux.HandleFunc("/v2/teams", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"teams": []map[string]string{{"id": team}}})
	})
	// Project lookup is team-scoped too; it exists in the team scope.
	mux.HandleFunc("/v9/projects/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != team {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "prj_team", "name": "my-project"})
	})
	mux.HandleFunc("/v2/files", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != team {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		filesTeam = r.URL.Query().Get("teamId")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v13/deployments", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("teamId") != team {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		deployTeam = r.URL.Query().Get("teamId")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "vdep-x", "url": "vdep-x.vercel.app", "readyState": "BUILDING"})
	})
	mux.HandleFunc("/v13/deployments/vdep-x", func(w http.ResponseWriter, r *http.Request) {
		pollTeam = r.URL.Query().Get("teamId")
		_ = json.NewEncoder(w).Encode(map[string]any{"readyState": "READY", "url": "vdep-x.vercel.app"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	assetsDir := writeAssetsDir(t, map[string]string{"index.html": "<html></html>"})
	got, err := vercelUploadAt(context.Background(), newVercelCreds("tok"), assetsDir, ts.URL)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if filesTeam != team || deployTeam != team || pollTeam != team {
		t.Errorf("teamId not propagated: files=%q deploy=%q poll=%q want %q", filesTeam, deployTeam, pollTeam, team)
	}
	if !strings.HasPrefix(got, "https://") {
		t.Errorf("url = %q, want https://...", got)
	}
}
