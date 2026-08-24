package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
)

// Schema limits mirrored from schemas/agent-card.schema.json (v4.1.3):
// identity.webApps has maxItems:25 and webApps[].label has maxLength:80.
// Writing past either produces a card that fails the next `blocks publish`.
const (
	maxWebApps     = 25
	maxWebAppLabel = 80
)

// parseCardPathFlags parses `--card-path agent=path` repeats into a map.
// Returns an error on malformed entries (no `=`, empty key or value).
func parseCardPathFlags(flags []string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("--card-path %q is malformed; expected <agent>=<path>", f)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			return nil, fmt.Errorf("--card-path %q is malformed; expected <agent>=<path>", f)
		}
		out[k] = v
	}
	return out, nil
}

// resolveLocalCardPath finds an agent card on disk for the named agent.
// Resolution priority: CLI override, then blocks.config.json:agentCardPaths,
// then a sibling-of-cwd convention (../<agent>/agent-card.json). Returns ""
// when none of those resolve to an existing file.
func resolveLocalCardPath(cwd, agentName string, cfg *config.BlocksConfig, overrides map[string]string, stderr io.Writer) string {
	if p, ok := overrides[agentName]; ok && p != "" {
		abs := resolveAbs(cwd, p)
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
		fmt.Fprintf(stderr, "Warning: --card-path %s=%q does not exist; skipping card update\n", agentName, p)
		return ""
	}
	if cfg.AgentCardPaths != nil {
		if p, ok := cfg.AgentCardPaths[agentName]; ok && p != "" {
			abs := resolveAbs(cwd, p)
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
			fmt.Fprintf(stderr,
				"Warning: agentCardPaths[%s] = %q does not exist; skipping card update\n",
				agentName, p)
			return ""
		}
	}
	sibling := filepath.Join(filepath.Dir(cwd), agentName, "agent-card.json")
	if _, err := os.Stat(sibling); err == nil {
		return sibling
	}
	return ""
}

func resolveAbs(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(cwd, p))
}

// maybeUpdateLocalAgentCards walks the agents listed in cfg and offers to
// append the deployed URL to each local agent-card.json's identity.webApps.
// If a card is not reachable locally, prints a copy-pasteable snippet to
// stderr instead. Idempotent — already-present URLs are not re-added.
func maybeUpdateLocalAgentCards(cfg *config.BlocksConfig, deployedURL string, overrides map[string]string, stdin io.Reader, stdout io.Writer, stderr io.Writer) {
	// Validate the URL with the same rule the backend enforces on
	// identity.webApps[].url before writing it into any card. A plugin /
	// partner response (or a manual mistake) that yields a schema-invalid
	// URL would otherwise be persisted and fail the next `blocks publish`.
	// Built-in deploy adapters already validate via plugin.go; this guards
	// the manual / misbehaving-plugin paths that reach the card updater.
	if err := deploy.ValidateWebAppURL(deployedURL); err != nil {
		fmt.Fprintf(stderr, "Warning: skipping card update — deployed URL %q is not a valid webApp URL: %v\n", deployedURL, err)
		return
	}

	cwd := mustCwd()
	projectLabel := filepath.Base(cwd)
	if len(projectLabel) > maxWebAppLabel {
		fmt.Fprintf(stderr, "Warning: skipping card update — derived webApp label %q is %d chars, exceeds the %d-char limit. Rename the project directory or add the entry manually.\n", projectLabel, len(projectLabel), maxWebAppLabel)
		return
	}
	reader := bufio.NewReader(stdin)

	for _, agent := range cfg.Agents {
		path := resolveLocalCardPath(cwd, agent, cfg, overrides, stderr)
		if path == "" {
			printCardSnippet(stderr, agent, deployedURL, projectLabel)
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: read %s: %v\n", path, err)
			continue
		}
		alreadyPresent, indent, err := cardContainsWebApp(raw, deployedURL)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: parse %s: %v\n", path, err)
			continue
		}
		if alreadyPresent {
			continue
		}
		atCap, err := cardWebAppsAtCap(raw)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: parse %s: %v\n", path, err)
			continue
		}
		if atCap {
			fmt.Fprintf(stderr, "Warning: skipping card update for %s — identity.webApps already has %d entries (the maximum). Remove an entry or add %s manually.\n", agent, maxWebApps, deployedURL)
			continue
		}
		fmt.Fprintf(stdout, "Add %s to identity.webApps of %s's card? [Y/n] ", deployedURL, agent)
		line, readErr := reader.ReadString('\n')
		ans := strings.TrimSpace(strings.ToLower(line))
		// Treat EOF / non-interactive stdin as "skip", NOT "yes". A blocks
		// deploy invoked from CI or piped script must not silently mutate
		// agent-card.json — the user can pass --no-card-update to suppress
		// the prompt entirely, or `yes | blocks deploy ...` to opt-in to
		// auto-accept.
		if readErr != nil {
			fmt.Fprintf(stderr, "Note: skipping card update for %s (no interactive stdin). Pass --no-card-update to silence this prompt; copy-pasteable snippet:\n", agent)
			printCardSnippet(stderr, agent, deployedURL, projectLabel)
			continue
		}
		if ans == "n" || ans == "no" {
			continue
		}
		updated, err := insertWebApp(raw, deployedURL, projectLabel, indent)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: edit %s: %v\n", path, err)
			continue
		}
		if err := os.WriteFile(path, updated, 0644); err != nil {
			fmt.Fprintf(stderr, "Warning: write %s: %v\n", path, err)
			continue
		}
		fmt.Fprintf(stdout, "Updated %s — re-run 'blocks publish' from %s/ to push the change.\n", path, agent)
	}
}

func printCardSnippet(w io.Writer, agent, url, label string) {
	fmt.Fprintf(w,
		"The agent '%s' card was not found locally. Ask the owner to add this entry to identity.webApps:\n\n"+
			"  {\n    \"url\": \"%s\",\n    \"label\": \"%s\"\n  }\n\n",
		agent, url, label,
	)
}

// cardContainsWebApp returns whether identity.webApps already contains url
// and detects the indent style for re-serialization. Indent detection looks
// at the leading whitespace before the first property in the file; defaults
// to two spaces.
func cardContainsWebApp(raw []byte, url string) (present bool, indent string, err error) {
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return false, "", err
	}
	identity, _ := card["identity"].(map[string]any)
	if identity == nil {
		return false, detectIndent(raw), nil
	}
	apps, _ := identity["webApps"].([]any)
	for _, w := range apps {
		m, _ := w.(map[string]any)
		if m == nil {
			continue
		}
		if s, _ := m["url"].(string); s == url {
			return true, detectIndent(raw), nil
		}
	}
	return false, detectIndent(raw), nil
}

// cardWebAppsAtCap reports whether identity.webApps already holds the
// maximum number of entries the schema allows (maxWebApps). Appending past
// that would produce a card that fails the next `blocks publish`.
func cardWebAppsAtCap(raw []byte) (bool, error) {
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return false, err
	}
	identity, _ := card["identity"].(map[string]any)
	if identity == nil {
		return false, nil
	}
	apps, _ := identity["webApps"].([]any)
	return len(apps) >= maxWebApps, nil
}

// insertWebApp re-serializes the card with the new webApp appended. The
// strategy is parse → mutate → re-serialize via json.MarshalIndent: this
// preserves the document's indent style but does not preserve key order
// (JSON has no ordering guarantee and Go's encoder emits keys sorted). For
// a card authored by `blocks init` + `blocks publish`, key reordering is
// acceptable; the alternative "punt and emit snippet" path is available
// via `--no-card-update`.
func insertWebApp(raw []byte, url, label, indent string) ([]byte, error) {
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return nil, err
	}
	identity, _ := card["identity"].(map[string]any)
	if identity == nil {
		identity = map[string]any{}
		card["identity"] = identity
	}
	apps, _ := identity["webApps"].([]any)
	apps = append(apps, map[string]any{
		"url":   url,
		"label": label,
	})
	identity["webApps"] = apps
	if indent == "" {
		indent = "  "
	}
	out, err := json.MarshalIndent(card, "", indent)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(string(out), "\n") {
		out = append(out, '\n')
	}
	return out, nil
}

// detectIndent inspects the raw JSON bytes for the leading whitespace before
// the first non-root property. Defaults to two spaces.
func detectIndent(raw []byte) string {
	s := string(raw)
	i := strings.Index(s, "{")
	if i < 0 {
		return "  "
	}
	rest := s[i+1:]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		return "  "
	}
	rest = rest[nl+1:]
	end := 0
	for end < len(rest) && (rest[end] == ' ' || rest[end] == '\t') {
		end++
	}
	if end == 0 {
		return "  "
	}
	return rest[:end]
}
