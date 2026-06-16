package deploy

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec — Vercel's file upload API uses SHA-1 for content addressing
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// Vercel REST API base URL.
// Verified 2026-05-08: https://vercel.com/docs/rest-api
const vercelAPIBase = "https://api.vercel.com"

var (
	vercelPollInterval = 5 * time.Second
	vercelPollTimeout  = 120 * time.Second
)

// vercelFileRef describes a file reference in a Vercel deployment body.
type vercelFileRef struct {
	File string `json:"file"`
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

// VercelUpload deploys assetsDir to Vercel and returns the deployed URL.
//
// Protocol:
//  1. POST /v2/files for each file (SHA-1 digest header required).
//  2. POST /v13/deployments with file references and project name.
//  3. Poll GET /v13/deployments/:id until state == "READY".
func VercelUpload(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error) {
	return vercelUploadAt(ctx, creds, assetsDir, vercelAPIBase)
}

// vercelUploadAt is the testable implementation that accepts an injected
// API base URL. The exported VercelUpload pins it to the live Vercel host;
// tests point it at httptest.Server.URL.
func vercelUploadAt(ctx context.Context, creds *auth.ProviderCredentials, assetsDir, apiBase string) (string, error) {
	token := creds.AccessToken
	projectName := slugify(filepath.Base(filepath.Dir(assetsDir)))
	if projectName == "" {
		projectName = "blocks-app"
	}

	// Resolve the team scope. Vercel REST endpoints run in the token's
	// personal context unless teamId is supplied, so a token scoped to a team
	// 403s without it. We pass teamId on every call when the token can only
	// act on a team.
	teamID := vercelScope(ctx, token, apiBase)

	// Ensure the project exists before deploying — Vercel's deploy endpoint
	// requires it, so the first-ever deploy would otherwise fail.
	if err := vercelEnsureProjectAt(ctx, token, teamID, projectName, apiBase); err != nil {
		return "", fmt.Errorf("vercel: ensure project: %w", err)
	}

	files, err := collectFiles(assetsDir)
	if err != nil {
		return "", fmt.Errorf("vercel: collect files: %w", err)
	}

	// Step 1: upload each file.
	var refs []vercelFileRef
	for relPath, content := range files {
		sha := sha1sum(content)
		if err := vercelUploadFileAt(ctx, token, teamID, sha, content, apiBase); err != nil {
			return "", fmt.Errorf("vercel: upload file %s: %w", relPath, err)
		}
		refs = append(refs, vercelFileRef{
			File: relPath,
			SHA:  sha,
			Size: len(content),
		})
	}

	// Step 2: create deployment.
	deployBody, err := json.Marshal(map[string]interface{}{
		"name":   projectName,
		"files":  refs,
		"target": "production",
	})
	if err != nil {
		return "", fmt.Errorf("vercel: marshal deploy body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vercelWithTeam(apiBase+"/v13/deployments", teamID), bytes.NewReader(deployBody))
	if err != nil {
		return "", fmt.Errorf("vercel: create deploy request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("vercel: deploy request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return "", fmt.Errorf("vercel: %s", vercelAuthHint(resp.StatusCode))
	}

	respBody, _ := io.ReadAll(resp.Body)
	var deployResp struct {
		ID         string   `json:"id"`
		URL        string   `json:"url"`
		Aliases    []string `json:"alias"`
		ReadyState string   `json:"readyState"`
	}
	if err := json.Unmarshal(respBody, &deployResp); err != nil {
		return "", fmt.Errorf("vercel: parse deploy response: %w", err)
	}
	if deployResp.ID == "" {
		return "", fmt.Errorf("vercel: deploy returned no deployment ID (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Step 3: poll until READY.
	deployedURL, err := vercelPollDeploymentAt(ctx, token, teamID, deployResp.ID, apiBase)
	if err != nil {
		return "", err
	}
	return deployedURL, nil
}

// vercelUploadFileAt uploads a single file to Vercel's file store at the given apiBase.
func vercelUploadFileAt(ctx context.Context, token, teamID, sha string, content []byte, apiBase string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vercelWithTeam(apiBase+"/v2/files", teamID), bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("x-vercel-digest", sha)
	req.ContentLength = int64(len(content))

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("%s", vercelAuthHint(resp.StatusCode))
	}
	// 200 = uploaded; 204 = already exists; both are success.
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("file upload returned HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// vercelPollDeploymentAt polls until the deployment is READY and returns the alias URL.
func vercelPollDeploymentAt(ctx context.Context, token, teamID, deploymentID, apiBase string) (string, error) {
	deadline := time.Now().Add(vercelPollTimeout)
	pollURL := vercelWithTeam(fmt.Sprintf("%s/v13/deployments/%s", apiBase, deploymentID), teamID)

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("vercel: deploy timed out after %s — check the Vercel dashboard for status", vercelPollTimeout)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(vercelPollInterval):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			continue // transient
		}

		var pollResp struct {
			ReadyState string   `json:"readyState"`
			URL        string   `json:"url"`
			Alias      []string `json:"alias"`
		}
		json.NewDecoder(resp.Body).Decode(&pollResp)
		resp.Body.Close()

		switch strings.ToUpper(pollResp.ReadyState) {
		case "READY":
			// Prefer the first alias, fall back to the deployment URL.
			if len(pollResp.Alias) > 0 {
				u := pollResp.Alias[0]
				if !strings.HasPrefix(u, "https://") {
					u = "https://" + u
				}
				return u, nil
			}
			u := pollResp.URL
			if u != "" && !strings.HasPrefix(u, "https://") {
				u = "https://" + u
			}
			return u, nil
		case "ERROR", "CANCELED":
			return "", fmt.Errorf("vercel: deployment %s reached terminal state %q", deploymentID, pollResp.ReadyState)
		}
		// Otherwise keep polling.
	}
}

// sha1sum returns the lowercase hex SHA-1 of data (Vercel file-upload requirement).
func sha1sum(data []byte) string {
	h := sha1.New() //nolint:gosec
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// vercelWithTeam appends ?teamId=<id> (or &teamId=) to a Vercel API URL when a
// team scope is in effect. Vercel scopes deployments/files to the personal
// account unless teamId is given.
func vercelWithTeam(rawURL, teamID string) string {
	if teamID == "" {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "teamId=" + url.QueryEscape(teamID)
}

// vercelAuthHint formats a 401/403 message that points at the most common
// cause: the token is scoped to a team the request didn't target.
func vercelAuthHint(status int) string {
	return fmt.Sprintf(
		"authentication denied (HTTP %d) — the API token is likely scoped to a team. "+
			"Set VERCEL_TEAM_ID to that team's id, or create a token scoped to the account "+
			"that owns the project at https://vercel.com/account/tokens",
		status,
	)
}

// vercelScope determines the teamId to scope requests to. Vercel REST
// endpoints act in the token's personal context unless teamId is supplied, so
// a team-scoped token 403s without it. Resolution order:
//  1. VERCEL_TEAM_ID env override.
//  2. If the token can act in personal scope, use no team ("").
//  3. Otherwise, find the team the token can actually access.
//
// Best-effort: on any uncertainty it returns "" and lets the real request
// surface a clear 403 via vercelAuthHint.
func vercelScope(ctx context.Context, token, apiBase string) string {
	if v := strings.TrimSpace(os.Getenv("VERCEL_TEAM_ID")); v != "" {
		return v
	}
	if vercelScopeOK(ctx, token, apiBase, "") {
		return ""
	}
	for _, id := range vercelListTeamIDs(ctx, token, apiBase) {
		if vercelScopeOK(ctx, token, apiBase, id) {
			return id
		}
	}
	return ""
}

// vercelScopeOK reports whether the token can read projects in the given scope
// (empty teamID = personal). Used to probe which scope the token belongs to.
func vercelScopeOK(ctx context.Context, token, apiBase, teamID string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, vercelWithTeam(apiBase+"/v9/projects?limit=1", teamID), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// vercelEnsureProjectAt looks up the named project and creates it if absent
// (HTTP 404). Vercel's deployment endpoint requires the target project to
// exist (or full projectSettings inline) — without a pre-created project the
// first-ever deploy fails. Mirrors cloudflare.go's cfEnsureProjectAt. Reads
// use /v9/projects/:name, create uses /v10/projects, both scoped by teamID.
func vercelEnsureProjectAt(ctx context.Context, token, teamID, projectName, apiBase string) error {
	getURL := vercelWithTeam(fmt.Sprintf("%s/v9/projects/%s", apiBase, url.PathEscape(projectName)), teamID)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	getReq.Header.Set("Authorization", "Bearer "+token)

	getResp, err := (&http.Client{}).Do(getReq)
	if err != nil {
		return err
	}
	getResp.Body.Close()

	if getResp.StatusCode >= 200 && getResp.StatusCode < 300 {
		return nil // already exists
	}
	if getResp.StatusCode == 401 || getResp.StatusCode == 403 {
		return fmt.Errorf("%s", vercelAuthHint(getResp.StatusCode))
	}
	if getResp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("vercel: project lookup returned HTTP %d", getResp.StatusCode)
	}

	body, _ := json.Marshal(map[string]interface{}{"name": projectName})
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, vercelWithTeam(apiBase+"/v10/projects", teamID), bytes.NewReader(body))
	if err != nil {
		return err
	}
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")

	createResp, err := (&http.Client{}).Do(createReq)
	if err != nil {
		return err
	}
	defer createResp.Body.Close()

	if createResp.StatusCode == 401 || createResp.StatusCode == 403 {
		return fmt.Errorf("%s", vercelAuthHint(createResp.StatusCode))
	}
	// 409 = created concurrently by another process; treat as success.
	if createResp.StatusCode == http.StatusConflict {
		return nil
	}
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		b, _ := io.ReadAll(createResp.Body)
		return fmt.Errorf("vercel: create project returned HTTP %d: %s", createResp.StatusCode, string(b))
	}
	return nil
}

// vercelListTeamIDs returns the ids of teams the token can see.
func vercelListTeamIDs(ctx context.Context, token, apiBase string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/v2/teams?limit=20", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var out struct {
		Teams []struct {
			ID string `json:"id"`
		} `json:"teams"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	ids := make([]string, 0, len(out.Teams))
	for _, t := range out.Teams {
		ids = append(ids, t.ID)
	}
	return ids
}
