package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"lukechampine.com/blake3"
)

// Cloudflare Pages direct-upload API.
// Reference: Wrangler's wrangler-pages direct-upload sequence.
// https://developers.cloudflare.com/pages/platform/direct-upload/
const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

var (
	cfPollInterval = 5 * time.Second
	cfPollTimeout  = 120 * time.Second
)

const (
	// cfUploadBatchSize bounds the number of file payloads sent in a single
	// /pages/assets/upload request. Wrangler caps batches at 5000 files or
	// ~50 MiB. Static partner pages are small so 200 keeps each request
	// well under the limit while limiting round-trip count.
	cfUploadBatchSize = 200
)

// cfDeploymentStage is the parsed lifecycle state of a Cloudflare Pages
// deployment, derived from `latest_stage.name` + `latest_stage.status`.
type cfDeploymentStage int

const (
	cfStagePending cfDeploymentStage = iota // keep polling
	cfStageSuccess                          // deployment live
	cfStageFailure                          // terminal failure
)

// cfDeploymentResponse is the subset of the Cloudflare deployment JSON we need.
//
// `latest_stage` is the load-bearing field for poll-decision logic. The
// top-level `success` flag only reports whether the API call itself succeeded
// — it is `true` for queued, building, and even already-failed deployments,
// so polling on `Success` alone would return on the first iteration regardless
// of the deployment's actual state.
type cfDeploymentResponse struct {
	Result struct {
		ID          string `json:"id"`
		URL         string `json:"url"`
		Subdomain   string `json:"subdomain"`
		LatestStage struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"latest_stage"`
	} `json:"result"`
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// cfClassifyStage maps Cloudflare's `latest_stage.{name,status}` to a polling
// decision. The deployment is live when the `deploy` stage reaches `success`.
// A `failure` / `canceled` status at any stage is terminal.
func cfClassifyStage(stageName, stageStatus string) cfDeploymentStage {
	switch strings.ToLower(stageStatus) {
	case "success":
		if strings.EqualFold(stageName, "deploy") {
			return cfStageSuccess
		}
		return cfStagePending
	case "failure", "failed", "canceled", "cancelled":
		return cfStageFailure
	default:
		return cfStagePending
	}
}

// Upload deploys the assetsDir to Cloudflare Pages.
func Upload(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error) {
	return CloudflareUpload(ctx, creds, assetsDir)
}

// CloudflareUpload deploys the assetsDir to Cloudflare Pages and returns the
// deployed URL. Uses the manifest-first direct-upload protocol (mirroring
// Wrangler): account/project setup, upload-token grant, check-missing,
// batched upload, upsert-hashes, deployment create, deployment poll.
func CloudflareUpload(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error) {
	return cloudflareUploadAt(ctx, creds, assetsDir, cloudflareAPIBase)
}

// cloudflareUploadAt is the testable implementation that accepts an injected
// API base URL. The exported CloudflareUpload pins it to the live Cloudflare
// host; tests point it at httptest.Server.URL+"/client/v4".
func cloudflareUploadAt(ctx context.Context, creds *auth.ProviderCredentials, assetsDir, apiBase string) (string, error) {
	token := creds.AccessToken
	accountID, err := cfGetAccountIDAt(ctx, token, apiBase)
	if err != nil {
		return "", fmt.Errorf("cloudflare: get account ID: %w", err)
	}

	projectName := slugify(filepath.Base(filepath.Dir(assetsDir)))
	if projectName == "" {
		projectName = "blocks-app"
	}

	subdomain, err := cfEnsureProjectAt(ctx, token, accountID, projectName, apiBase)
	if err != nil {
		return "", fmt.Errorf("cloudflare: ensure project: %w", err)
	}

	files, err := collectFiles(assetsDir)
	if err != nil {
		return "", fmt.Errorf("cloudflare: collect files: %w", err)
	}

	// Build the path → hash manifest. Cloudflare Pages keys each asset by
	// cfAssetHash (blake3 over base64(content)+extension, 32 hex chars) — the
	// asset server verifies content integrity against this hash on serve, so a
	// wrong algorithm yields an uploaded-but-unservable deploy (HTTP 500).
	// hashToContentType carries each asset's MIME type; without it Cloudflare
	// serves every file as application/octet-stream (the browser downloads
	// index.html instead of rendering it).
	manifest := make(map[string]string, len(files))
	hashToContent := make(map[string][]byte, len(files))
	hashToContentType := make(map[string]string, len(files))
	for relPath, content := range files {
		h := cfAssetHash(content, relPath)
		manifest["/"+relPath] = h
		hashToContent[h] = content
		hashToContentType[h] = cfContentType(relPath)
	}

	uploadToken, err := cfGetUploadTokenAt(ctx, token, accountID, projectName, apiBase)
	if err != nil {
		return "", fmt.Errorf("cloudflare: get upload token: %w", err)
	}

	allHashes := make([]string, 0, len(hashToContent))
	for h := range hashToContent {
		allHashes = append(allHashes, h)
	}

	missing, err := cfCheckMissingAt(ctx, uploadToken, allHashes, apiBase)
	if err != nil {
		return "", fmt.Errorf("cloudflare: check missing: %w", err)
	}

	if len(missing) > 0 {
		if err := cfUploadAssetsAt(ctx, uploadToken, missing, hashToContent, hashToContentType, apiBase); err != nil {
			return "", fmt.Errorf("cloudflare: upload assets: %w", err)
		}
	}

	if err := cfUpsertHashesAt(ctx, uploadToken, allHashes, apiBase); err != nil {
		return "", fmt.Errorf("cloudflare: upsert hashes: %w", err)
	}

	deployment, err := cfCreateDeploymentAt(ctx, token, accountID, projectName, manifest, apiBase)
	if err != nil {
		return "", fmt.Errorf("cloudflare: create deployment: %w", err)
	}

	if err := cfPollDeploymentAt(ctx, token, accountID, projectName, deployment.Result.ID, apiBase); err != nil {
		return "", err
	}

	// Prefer the project's production subdomain (e.g. "livelive-76n.pages.dev")
	// — Cloudflare appends a unique suffix when the bare "<name>.pages.dev" is
	// already taken globally, so guessing "<projectName>.pages.dev" can point
	// at the wrong (or someone else's) host. Fall back to that guess only if
	// the project object carried no subdomain.
	host := subdomain
	if host == "" {
		host = projectName + ".pages.dev"
	}
	return "https://" + host, nil
}

func cfGetAccountIDAt(ctx context.Context, token, apiBase string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/accounts?per_page=1", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", fmt.Errorf("authentication denied (HTTP %d)", resp.StatusCode)
	}

	var result struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if !result.Success || len(result.Result) == 0 {
		return "", fmt.Errorf("no Cloudflare account found for this token")
	}
	return result.Result[0].ID, nil
}

// cfEnsureProjectAt looks up the Pages project (creating it if absent) and
// returns its production subdomain (the full "<name>.pages.dev" host, which
// Cloudflare may suffix to keep it globally unique). The subdomain may be
// empty if the API omits it; callers fall back to "<projectName>.pages.dev".
func cfEnsureProjectAt(ctx context.Context, token, accountID, projectName, apiBase string) (string, error) {
	type projectResult struct {
		Result struct {
			Subdomain string `json:"subdomain"`
		} `json:"result"`
	}

	getURL := fmt.Sprintf("%s/accounts/%s/pages/projects/%s", apiBase, accountID, projectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var pr projectResult
		_ = json.NewDecoder(resp.Body).Decode(&pr)
		return pr.Result.Subdomain, nil
	}
	if resp.StatusCode != 404 {
		return "", fmt.Errorf("project lookup returned HTTP %d", resp.StatusCode)
	}

	createURL := fmt.Sprintf("%s/accounts/%s/pages/projects", apiBase, accountID)
	body, _ := json.Marshal(map[string]interface{}{
		"name":              projectName,
		"production_branch": "main",
	})
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := (&http.Client{}).Do(createReq)
	if err != nil {
		return "", err
	}
	defer createResp.Body.Close()

	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		return "", fmt.Errorf("create project returned HTTP %d", createResp.StatusCode)
	}
	var pr projectResult
	_ = json.NewDecoder(createResp.Body).Decode(&pr)
	return pr.Result.Subdomain, nil
}

// cfGetUploadTokenAt fetches the short-lived JWT scoped to the project's asset
// uploads. Per Wrangler, calls to /pages/assets/* require this token rather
// than the account-level API token.
func cfGetUploadTokenAt(ctx context.Context, token, accountID, projectName, apiBase string) (string, error) {
	url := fmt.Sprintf("%s/accounts/%s/pages/projects/%s/upload-token", apiBase, accountID, projectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", fmt.Errorf("authentication denied (HTTP %d) — check your API token scopes", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload-token returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	var tok struct {
		Result struct {
			JWT string `json:"jwt"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.Result.JWT == "" {
		return "", fmt.Errorf("upload-token returned an empty JWT")
	}
	return tok.Result.JWT, nil
}

func cfCheckMissingAt(ctx context.Context, uploadJWT string, hashes []string, apiBase string) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"hashes": hashes})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/pages/assets/check-missing", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+uploadJWT)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("check-missing returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Result  []string `json:"result"`
		Success bool     `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

// cfUploadAssetsAt POSTs base64-encoded file bodies to /pages/assets/upload in
// batches of cfUploadBatchSize. Each payload is `{ key, value, base64,
// metadata: { contentType } }` per Wrangler's protocol. The contentType is
// required: without it Cloudflare serves the asset as application/octet-stream,
// so the browser downloads index.html instead of rendering it.
func cfUploadAssetsAt(ctx context.Context, uploadJWT string, missingHashes []string, hashToContent map[string][]byte, hashToContentType map[string]string, apiBase string) error {
	type payload struct {
		Key      string         `json:"key"`
		Value    string         `json:"value"`
		Base64   bool           `json:"base64"`
		Metadata map[string]any `json:"metadata,omitempty"`
	}

	for start := 0; start < len(missingHashes); start += cfUploadBatchSize {
		end := start + cfUploadBatchSize
		if end > len(missingHashes) {
			end = len(missingHashes)
		}
		batch := make([]payload, 0, end-start)
		for _, h := range missingHashes[start:end] {
			c, ok := hashToContent[h]
			if !ok {
				return fmt.Errorf("missing hash %s not in local file set", h)
			}
			ct := hashToContentType[h]
			if ct == "" {
				ct = "application/octet-stream"
			}
			batch = append(batch, payload{
				Key:      h,
				Value:    base64.StdEncoding.EncodeToString(c),
				Base64:   true,
				Metadata: map[string]any{"contentType": ct},
			})
		}
		body, _ := json.Marshal(batch)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/pages/assets/upload", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+uploadJWT)
		req.Header.Set("Content-Type", "application/json")

		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return err
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("upload returned HTTP %d: %s", resp.StatusCode, string(respBody))
		}
	}
	return nil
}

func cfUpsertHashesAt(ctx context.Context, uploadJWT string, hashes []string, apiBase string) error {
	body, _ := json.Marshal(map[string]any{"hashes": hashes})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/pages/assets/upsert-hashes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+uploadJWT)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upsert-hashes returned HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// cfCreateDeploymentAt creates the actual deployment record referencing the
// just-uploaded manifest. After this call the deploy enters the polling
// lifecycle.
//
// The endpoint expects multipart/form-data with a `manifest` form field whose
// value is the JSON-encoded path→hash map — NOT a JSON request body. Sending
// application/json makes Cloudflare's multipart parser reject the request with
// `A "manifest" field was expected in the request body but was not provided.`
func cfCreateDeploymentAt(ctx context.Context, token, accountID, projectName string, manifest map[string]string, apiBase string) (*cfDeploymentResponse, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("manifest", string(manifestJSON)); err != nil {
		return nil, fmt.Errorf("write manifest field: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}

	url := fmt.Sprintf("%s/accounts/%s/pages/projects/%s/deployments", apiBase, accountID, projectName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("authentication denied (HTTP %d) — check your API token scopes", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	var deployResp cfDeploymentResponse
	if err := json.Unmarshal(respBody, &deployResp); err != nil {
		return nil, fmt.Errorf("parse deploy response: %w", err)
	}
	if !deployResp.Success {
		if len(deployResp.Errors) > 0 {
			return nil, fmt.Errorf("deploy failed: %s", deployResp.Errors[0].Message)
		}
		return nil, fmt.Errorf("deploy failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}
	if deployResp.Result.ID == "" {
		return nil, fmt.Errorf("deploy succeeded but returned no deployment ID")
	}
	return &deployResp, nil
}

func cfPollDeploymentAt(ctx context.Context, token, accountID, projectName, deploymentID, apiBase string) error {
	deadline := time.Now().Add(cfPollTimeout)
	// The deployment-status endpoint is scoped under the project; the
	// project-less /pages/deployments/<id> path returns success:false here,
	// which would (silently) poll until the timeout even on a live deploy.
	pollURL := fmt.Sprintf("%s/accounts/%s/pages/projects/%s/deployments/%s", apiBase, accountID, projectName, deploymentID)

	var lastStageName, lastStageStatus string

	for {
		if time.Now().After(deadline) {
			if lastStageName != "" || lastStageStatus != "" {
				return fmt.Errorf(
					"cloudflare: deploy timed out after %s at stage %s/%s — check the Cloudflare dashboard for status",
					cfPollTimeout, lastStageName, lastStageStatus,
				)
			}
			return fmt.Errorf("cloudflare: deploy timed out after %s — check the Cloudflare dashboard for status", cfPollTimeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cfPollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			continue // transient
		}
		var pollResp cfDeploymentResponse
		json.NewDecoder(resp.Body).Decode(&pollResp)
		resp.Body.Close()

		if !pollResp.Success {
			continue
		}

		lastStageName = pollResp.Result.LatestStage.Name
		lastStageStatus = pollResp.Result.LatestStage.Status

		switch cfClassifyStage(lastStageName, lastStageStatus) {
		case cfStageSuccess:
			return nil
		case cfStageFailure:
			return fmt.Errorf(
				"cloudflare: deployment %s failed at stage %s/%s",
				deploymentID, lastStageName, lastStageStatus,
			)
		case cfStagePending:
			// keep polling
		}
	}
}

// collectFiles walks assetsDir and returns a map of relative path -> file content.
//
// Containment: entries are uploaded to a public CDN, so a symlink pointing
// outside assetsDir (e.g. web/secrets -> ../../.env) would exfiltrate local
// files. filepath.Walk uses Lstat, so symlinks arrive un-followed — we resolve
// each one and reject any whose target escapes the (resolved) deploy root.
// In-tree symlinks are allowed; symlinks resolving to a directory are skipped
// (Walk does not descend into them and we do not expand them).
func collectFiles(assetsDir string) (map[string][]byte, error) {
	rootReal, err := filepath.EvalSymlinks(assetsDir)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve deploy dir %q: %w", assetsDir, err)
	}
	files := make(map[string][]byte)
	err = filepath.Walk(assetsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("cannot resolve symlink %q: %w", path, err)
			}
			rel, err := filepath.Rel(rootReal, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("refusing to upload symlink %q: target %q escapes the deploy directory", path, resolved)
			}
			ri, err := os.Stat(resolved)
			if err != nil {
				return fmt.Errorf("cannot stat symlink target %q: %w", resolved, err)
			}
			if ri.IsDir() {
				return nil
			}
		}
		rel, err := filepath.Rel(assetsDir, path)
		if err != nil {
			return err
		}
		rel = strings.ReplaceAll(rel, string(filepath.Separator), "/")
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = content
		return nil
	})
	return files, err
}

// cfAssetHash computes the Cloudflare Pages asset key for a file, matching
// Wrangler's scheme: blake3 over the base64-encoded content concatenated with
// the file extension (no leading dot), hex-encoded and truncated to 32 chars.
// The Pages asset server recomputes this on serve to verify integrity, so it
// MUST match exactly — a mismatch (e.g. sha256) deploys assets that then 500.
func cfAssetHash(content []byte, relPath string) string {
	ext := strings.TrimPrefix(filepath.Ext(relPath), ".")
	b64 := base64.StdEncoding.EncodeToString(content)
	sum := blake3.Sum256([]byte(b64 + ext))
	return hex.EncodeToString(sum[:])[:32]
}

// cfContentType returns the MIME type Cloudflare should serve a file as,
// derived from its extension. Falls back to application/octet-stream for
// unknown extensions.
func cfContentType(relPath string) string {
	if ct := mime.TypeByExtension(filepath.Ext(relPath)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// slugify converts a string to a lowercase, hyphen-separated identifier.
// Spaces, hyphens, underscores, and dots are all collapsed to a single hyphen.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastWasHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastWasHyphen = false
		} else if r == '-' || r == '_' || r == '.' || r == ' ' {
			if !lastWasHyphen {
				b.WriteRune('-')
				lastWasHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
