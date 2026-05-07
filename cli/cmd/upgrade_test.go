package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// resetTokenCache allows tests to clear the sync.Once-cached token.
func resetTokenCache() {
	cachedToken = ""
	cachedTokenOnce = sync.Once{}
}

func TestBuildArchiveName(t *testing.T) {
	name := buildArchiveName("v1.2.3")

	// Should strip v prefix
	if strings.Contains(name, "v1.2.3") {
		t.Error("should strip v prefix from tag")
	}

	// Should follow format: blocks_{ver}_{os}_{arch}.{ext}
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	want := "blocks_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + "." + ext
	if name != want {
		t.Errorf("buildArchiveName(\"v1.2.3\") = %q, want %q", name, want)
	}
}

func TestBuildArchiveNameNoPrefix(t *testing.T) {
	name := buildArchiveName("1.2.3")

	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	want := "blocks_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + "." + ext
	if name != want {
		t.Errorf("buildArchiveName(\"1.2.3\") = %q, want %q", name, want)
	}
}

func TestCheckLatestRelease_FastPathCliTag(t *testing.T) {
	resetTokenCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/releases/latest" {
			json.NewEncoder(w).Encode(githubRelease{
				TagName:     "cli-v1.5.0",
				PublishedAt: "2025-06-01T00:00:00Z",
			})
			return
		}
		t.Errorf("unexpected request: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	ver, dl, err := checkLatestRelease()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v1.5.0" {
		t.Errorf("version = %q, want %q", ver, "v1.5.0")
	}
	if !strings.Contains(dl, "cli-v1.5.0") {
		t.Errorf("download URL %q should contain cli-v1.5.0", dl)
	}
}

func TestCheckLatestRelease_FastPathFails_FallsThrough(t *testing.T) {
	resetTokenCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			http.NotFound(w, r)
		case "/releases":
			json.NewEncoder(w).Encode([]githubRelease{
				{TagName: "sdk/v2.0.0", PublishedAt: "2025-07-01T00:00:00Z"},
				{TagName: "cli-v1.3.0", PublishedAt: "2025-06-15T00:00:00Z"},
			})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	ver, _, err := checkLatestRelease()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v1.3.0" {
		t.Errorf("version = %q, want %q", ver, "v1.3.0")
	}
}

func TestCheckLatestRelease_FallbackSortsByPublishedAt(t *testing.T) {
	resetTokenCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			// Latest is not a CLI release
			json.NewEncoder(w).Encode(githubRelease{
				TagName:     "sdk/v3.0.0",
				PublishedAt: "2025-08-01T00:00:00Z",
			})
		case "/releases":
			// Return out of order — older release first in the list
			json.NewEncoder(w).Encode([]githubRelease{
				{TagName: "cli-v1.1.0", PublishedAt: "2025-05-01T00:00:00Z"},
				{TagName: "cli-v1.4.0", PublishedAt: "2025-07-01T00:00:00Z"},
				{TagName: "cli-v1.2.0", PublishedAt: "2025-06-01T00:00:00Z"},
			})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	ver, _, err := checkLatestRelease()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v1.4.0" {
		t.Errorf("version = %q, want %q (should pick most recent by published_at)", ver, "v1.4.0")
	}
}

func TestCheckLatestRelease_SkipsDraftAndPrerelease(t *testing.T) {
	resetTokenCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			json.NewEncoder(w).Encode(githubRelease{
				TagName:     "sdk/v2.0.0",
				PublishedAt: "2025-08-01T00:00:00Z",
			})
		case "/releases":
			json.NewEncoder(w).Encode([]githubRelease{
				{TagName: "cli-v2.0.0-rc1", Prerelease: true, PublishedAt: "2025-07-20T00:00:00Z"},
				{TagName: "cli-v2.0.0-draft", Draft: true, PublishedAt: "2025-07-15T00:00:00Z"},
				{TagName: "cli-v1.9.0", PublishedAt: "2025-07-10T00:00:00Z"},
			})
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = orig }()

	ver, _, err := checkLatestRelease()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != "v1.9.0" {
		t.Errorf("version = %q, want %q (should skip draft and prerelease)", ver, "v1.9.0")
	}
}

func TestGithubToken_Caching(t *testing.T) {
	resetTokenCache()
	t.Setenv("GITHUB_TOKEN", "test-token-123")

	tok1 := githubToken()
	if tok1 != "test-token-123" {
		t.Errorf("githubToken() = %q, want %q", tok1, "test-token-123")
	}

	// Change env — cached value should persist
	t.Setenv("GITHUB_TOKEN", "different-token")
	tok2 := githubToken()
	if tok2 != "test-token-123" {
		t.Errorf("githubToken() after env change = %q, want cached %q", tok2, "test-token-123")
	}
}
