package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const updateCheckInterval = 2 * time.Hour

type updateState struct {
	LastCheck     time.Time `json:"last_check"`
	LatestVersion string    `json:"latest_version"`
}

func updateCheckFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".blocks", "update-check.json")
}

func loadUpdateState() (updateState, error) {
	var s updateState
	f := updateCheckFile()
	if f == "" {
		return s, fmt.Errorf("no home directory")
	}
	data, err := os.ReadFile(f)
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}

func saveUpdateState(s updateState) {
	f := updateCheckFile()
	if f == "" {
		return
	}
	data, _ := json.Marshal(s)
	dir := filepath.Dir(f)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(f, data, 0o644)
}

func checkForUpdateNotice() {
	if Version == "dev" {
		return
	}

	state, _ := loadUpdateState()

	if time.Since(state.LastCheck) < updateCheckInterval && state.LatestVersion != "" {
		printUpdateNotice(state.LatestVersion)
		return
	}

	latest, err := fetchLatestNpmVersion()
	if err != nil {
		return
	}

	state.LastCheck = time.Now()
	state.LatestVersion = latest
	saveUpdateState(state)

	printUpdateNotice(latest)
}

func printUpdateNotice(latest string) {
	current := strings.TrimPrefix(Version, "v")
	latest = strings.TrimPrefix(latest, "v")

	if current == latest {
		return
	}

	fmt.Fprintf(os.Stderr, "\nA new version of blocks is available: %s → %s\nRun `blocks upgrade` to update.\n", current, latest)
}
