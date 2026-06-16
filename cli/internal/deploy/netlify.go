package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// Netlify REST API base URL.
// Verified 2026-05-08: https://docs.netlify.com/api/get-started/
const netlifyAPIBase = "https://api.netlify.com"

var (
	netlifyPollInterval = 5 * time.Second
	netlifyPollTimeout  = 120 * time.Second
)

// netlifyCreateMaxAttempts bounds the suffix-retry loop when a site name is
// already taken globally.
const netlifyCreateMaxAttempts = 5

// netlifyDeployResponse is the subset of the Netlify deploy response we need.
type netlifyDeployResponse struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// netlifySiteResponse is the Netlify site record.
type netlifySiteResponse struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	URL           string                 `json:"url"`
	CustomDomain  string                 `json:"custom_domain"`
	CurrentDeploy *netlifyDeployResponse `json:"current_deploy"`
}

// NetlifyUpload deploys assetsDir to Netlify using the zip-deploy protocol and returns
// the deployed site URL.
//
// Protocol:
//  1. POST /api/v1/sites to create (or reuse existing) Netlify site.
//  2. POST /api/v1/sites/:site_id/deploys with zip body.
//  3. Poll GET /api/v1/deploys/:deploy_id until current_deploy.state == "ready".
func NetlifyUpload(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error) {
	return netlifyUploadAt(ctx, creds, assetsDir, netlifyAPIBase)
}

// netlifyUploadAt is the testable implementation that accepts an injected
// API base URL. The exported NetlifyUpload pins it to the live Netlify host;
// tests point it at httptest.Server.URL.
func netlifyUploadAt(ctx context.Context, creds *auth.ProviderCredentials, assetsDir, apiBase string) (string, error) {
	token := creds.AccessToken
	siteName := slugify(filepath.Base(filepath.Dir(assetsDir)))
	if siteName == "" {
		siteName = "blocks-app"
	}

	site, err := netlifyEnsureSiteAt(ctx, token, siteName, apiBase)
	if err != nil {
		return "", fmt.Errorf("netlify: ensure site: %w", err)
	}

	zipData, err := zipAssetsDir(assetsDir)
	if err != nil {
		return "", fmt.Errorf("netlify: zip assets: %w", err)
	}

	deploy, err := netlifyCreateDeployAt(ctx, token, site.ID, zipData, apiBase)
	if err != nil {
		return "", fmt.Errorf("netlify: create deploy: %w", err)
	}

	siteURL, err := netlifyPollDeployAt(ctx, token, deploy.ID, apiBase)
	if err != nil {
		return "", err
	}
	if siteURL == "" {
		siteURL = site.URL
	}
	return siteURL, nil
}

// netlifyEnsureSiteAt finds an existing site by name (paginating the sites
// list) or creates a new one. Netlify site names are globally-unique
// subdomains, so a create can 422 on a name another account already holds;
// we retry with a numeric suffix before giving up.
func netlifyEnsureSiteAt(ctx context.Context, token, siteName, apiBase string) (*netlifySiteResponse, error) {
	nextURL := apiBase + "/api/v1/sites?per_page=100"
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := (&http.Client{}).Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			resp.Body.Close()
			return nil, fmt.Errorf("authentication denied (HTTP %d) — check your Netlify token", resp.StatusCode)
		}

		var sites []netlifySiteResponse
		if err := json.NewDecoder(resp.Body).Decode(&sites); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("parse sites list: %w", err)
		}
		next := netlifyNextLink(resp.Header.Get("Link"))
		resp.Body.Close()

		for i := range sites {
			if sites[i].Name == siteName {
				return &sites[i], nil
			}
		}
		nextURL = next
	}

	for attempt := 0; attempt < netlifyCreateMaxAttempts; attempt++ {
		candidate := siteName
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", siteName, attempt)
		}
		site, status, respBody, err := netlifyCreateSiteAt(ctx, token, candidate, apiBase)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnprocessableEntity {
			continue
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("create site returned HTTP %d: %s", status, respBody)
		}
		return site, nil
	}
	return nil, fmt.Errorf("netlify: could not find an available site name after %d attempts (base %q is taken)", netlifyCreateMaxAttempts, siteName)
}

// netlifyCreateSiteAt POSTs a single create-site request and returns the
// parsed site plus the HTTP status so the caller can distinguish a 422
// name collision from other failures. On a non-2xx response it also returns
// the raw response body so the caller can surface it for triage (parity with
// the Cloudflare deploy path).
func netlifyCreateSiteAt(ctx context.Context, token, name, apiBase string) (*netlifySiteResponse, int, string, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/api/v1/sites", bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, string(b), nil
	}
	var site netlifySiteResponse
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil {
		return nil, resp.StatusCode, "", fmt.Errorf("parse create-site response: %w", err)
	}
	return &site, resp.StatusCode, "", nil
}

// netlifyNextLink extracts the rel="next" URL from a Link header, or "".
func netlifyNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		seg := strings.TrimSpace(part)
		if !strings.Contains(seg, `rel="next"`) {
			continue
		}
		lt := strings.Index(seg, "<")
		gt := strings.Index(seg, ">")
		if lt >= 0 && gt > lt {
			return seg[lt+1 : gt]
		}
	}
	return ""
}

// netlifyCreateDeployAt POSTs the zip body to the Netlify deploy endpoint.
func netlifyCreateDeployAt(ctx context.Context, token, siteID string, zipData []byte, apiBase string) (*netlifyDeployResponse, error) {
	url := fmt.Sprintf("%s/api/v1/sites/%s/deploys", apiBase, siteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(zipData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/zip")
	req.ContentLength = int64(len(zipData))

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("authentication denied (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("deploy returned HTTP %d: %s", resp.StatusCode, string(b))
	}

	var deploy netlifyDeployResponse
	if err := json.NewDecoder(resp.Body).Decode(&deploy); err != nil {
		return nil, fmt.Errorf("parse deploy response: %w", err)
	}
	return &deploy, nil
}

// netlifyPollDeployAt polls until the deploy is ready and returns the site URL.
func netlifyPollDeployAt(ctx context.Context, token, deployID, apiBase string) (string, error) {
	deadline := time.Now().Add(netlifyPollTimeout)
	pollURL := fmt.Sprintf("%s/api/v1/deploys/%s", apiBase, deployID)

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("netlify: deploy timed out after %s — check the Netlify dashboard for status", netlifyPollTimeout)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(netlifyPollInterval):
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
			State     string `json:"state"`
			SiteID    string `json:"site_id"`
			DeployURL string `json:"deploy_url"`
		}
		json.NewDecoder(resp.Body).Decode(&pollResp)
		resp.Body.Close()

		switch pollResp.State {
		case "ready":
			siteURL, err := netlifyGetSiteURLAt(ctx, token, pollResp.SiteID, apiBase)
			if err != nil {
				return pollResp.DeployURL, nil
			}
			return siteURL, nil
		case "error":
			return "", fmt.Errorf("netlify: deployment %s failed", deployID)
		}
	}
}

// netlifyGetSiteURLAt fetches the canonical URL for a site.
func netlifyGetSiteURLAt(ctx context.Context, token, siteID, apiBase string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/sites/%s", apiBase, siteID)
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

	var site struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&site); err != nil {
		return "", err
	}
	return site.URL, nil
}

// zipAssetsDir builds an in-memory zip archive of all files in assetsDir.
func zipAssetsDir(assetsDir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	files, err := collectFiles(assetsDir)
	if err != nil {
		return nil, err
	}

	for relPath, content := range files {
		fw, err := zw.Create(relPath)
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(content); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
