package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

const (
	githubRepo   = "pubnub/blocksnetwork"
	downloadBase = "https://github.com/" + githubRepo + "/releases/download"
)

var githubAPIBase = "https://api.github.com/repos/" + githubRepo

func init() {
	rootCmd.AddCommand(upgradeCmd)
}

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Check for and install CLI updates",
	Long:  "Check GitHub Releases for a newer version of the Blocks CLI and self-update.",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Printf("Current version: %s\n", Version)

		latest, downloadURL, err := checkLatestRelease()
		if err != nil {
			return fmt.Errorf("could not check for updates: %w", err)
		}

		current := strings.TrimPrefix(Version, "v")
		latest = strings.TrimPrefix(latest, "v")

		if current == latest {
			fmt.Println("Already up to date.")
			return nil
		}

		if current == "dev" {
			fmt.Printf("Latest release: v%s\n", latest)
			fmt.Println("You are running a development build. Install a release version to use upgrade:")
			fmt.Printf("  %s\n", downloadURL)
			return nil
		}

		fmt.Printf("New version available: v%s\n", latest)
		fmt.Printf("Download: %s\n", downloadURL)
		fmt.Println("\nTo update, download the latest release or run:")
		fmt.Println("  brew upgrade blocks       # if installed via Homebrew")
		fmt.Println("  scoop update blocks       # if installed via Scoop")
		return nil
	},
}

type githubRelease struct {
	TagName     string `json:"tag_name"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
	PublishedAt string `json:"published_at"`
}

var (
	cachedToken     string
	cachedTokenOnce sync.Once
)

// resolveGithubToken performs the actual token lookup.
// Fallback chain: GITHUB_TOKEN → GH_TOKEN → gh auth token (5s timeout) → "".
func resolveGithubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// githubToken returns a cached token for GitHub API access.
func githubToken() string {
	cachedTokenOnce.Do(func() { cachedToken = resolveGithubToken() })
	return cachedToken
}

// githubGet performs an authenticated GET against the GitHub API.
func githubGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "token "+tok)
	}
	return http.DefaultClient.Do(req)
}

// checkLatestRelease finds the latest CLI release (cli-v* tag).
// Tries /releases/latest first; falls back to scanning recent releases.
func checkLatestRelease() (version string, downloadURL string, err error) {
	// Fast path: /releases/latest (non-fatal — falls through on error)
	if r, err := fetchRelease(githubAPIBase + "/releases/latest"); err == nil && strings.HasPrefix(r.TagName, "cli-v") {
		ver, dl := releaseResult(r)
		return ver, dl, nil
	}

	// Fallback: scan recent releases for the latest cli-v* tag
	resp, err := githubGet(githubAPIBase + "/releases?per_page=30")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", "", fmt.Errorf("parse releases response: %w", err)
	}

	// Sort by published_at descending so the most recent release wins
	sort.Slice(releases, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, releases[i].PublishedAt)
		tj, _ := time.Parse(time.RFC3339, releases[j].PublishedAt)
		return ti.After(tj)
	})

	for _, rel := range releases {
		if rel.Draft || rel.Prerelease {
			continue
		}
		if strings.HasPrefix(rel.TagName, "cli-v") {
			ver, dl := releaseResult(rel)
			return ver, dl, nil
		}
	}

	return "", "", fmt.Errorf("no CLI release found (looking for tags matching cli-v*)")
}

func fetchRelease(url string) (githubRelease, error) {
	resp, err := githubGet(url)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return githubRelease{}, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body))
	}

	var r githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return githubRelease{}, fmt.Errorf("parse release response: %w", err)
	}
	return r, nil
}

func releaseResult(r githubRelease) (string, string) {
	ver := strings.TrimPrefix(r.TagName, "cli-")
	archiveName := buildArchiveName(ver)
	dl := fmt.Sprintf("%s/%s/%s", downloadBase, r.TagName, archiveName)
	return ver, dl
}

func buildArchiveName(tag string) string {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}

	return fmt.Sprintf("blocks_%s_%s_%s.%s", strings.TrimPrefix(tag, "v"), goos, goarch, ext)
}
