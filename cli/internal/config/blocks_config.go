package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/deploy"
)

// agentNamePattern is the bare agent-name pattern enforced by the Blocks registry
// (agent.agentName column). No slashes — bare name only.
var agentNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// BlocksConfig is the in-memory representation of blocks.config.json.
//
// deployTarget, lastDeployedUrl, and agentCardPaths are OMITTED from the
// serialised JSON when unset. The schema enum for deployTarget does NOT
// accept "" or null, so omitempty is required.
type BlocksConfig struct {
	TemplateVersion string            `json:"templateVersion"`
	Agents          []string          `json:"agents"`
	DeployTarget    string            `json:"deployTarget,omitempty"`
	LastDeployedUrl string            `json:"lastDeployedUrl,omitempty"`
	AgentCardPaths  map[string]string `json:"agentCardPaths,omitempty"`
}

// Load reads and JSON-decodes a blocks.config.json file at path.
func Load(path string) (*BlocksConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var cfg BlocksConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg as indented JSON to path atomically (temp-file + rename),
// mode 0644. Optional fields with zero values are omitted via struct tags.
func Save(path string, cfg *BlocksConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}

	// Write to a temp file in the same directory, then rename atomically.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".blocks_config_*.json")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Ensure cleanup on any failure path before rename.
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: rename: %w", err)
	}
	ok = true
	return nil
}

// Validate checks cfg against every rule from
// schemas/internal/blocks-config.schema.json and impl_05 §R8.1.
// It collects ALL violations rather than failing on the first.
func Validate(cfg *BlocksConfig) error {
	var errs []string

	// templateVersion: required, semver-like pattern \d+\.\d+\.\d+
	if cfg.TemplateVersion == "" {
		errs = append(errs, "templateVersion is required")
	} else if !isSemver(cfg.TemplateVersion) {
		errs = append(errs, fmt.Sprintf("templateVersion %q must match \\d+\\.\\d+\\.\\d+", cfg.TemplateVersion))
	}

	// agents: non-empty, length ≤ 25, unique, each matches bare-name pattern.
	if len(cfg.Agents) == 0 {
		errs = append(errs, "agents must contain at least one entry")
	} else {
		if len(cfg.Agents) > 25 {
			errs = append(errs, fmt.Sprintf("agents must contain at most 25 entries (got %d)", len(cfg.Agents)))
		}
		seen := make(map[string]bool, len(cfg.Agents))
		for _, agent := range cfg.Agents {
			if strings.Contains(agent, "/") {
				errs = append(errs, fmt.Sprintf(
					"agent name %q is invalid: agent name must be the bare agent name (e.g. 'translator'), not the namespaced form (e.g. 'acme/translator')",
					agent,
				))
				continue
			}
			if !agentNamePattern.MatchString(agent) {
				errs = append(errs, fmt.Sprintf(
					"agent name %q is invalid: must match ^[a-zA-Z0-9_]+$ (bare agent name, no slashes)",
					agent,
				))
				continue
			}
			if seen[agent] {
				errs = append(errs, fmt.Sprintf("agent name %q appears more than once; agents must be unique", agent))
			}
			seen[agent] = true
		}
	}

	// deployTarget: shape-only gate, sharing the slug pattern enforced at
	// plugin-manifest load and built-in adapter registration. Empty is OK
	// (means "use the CLI default" / fall back to a positional arg).
	if cfg.DeployTarget != "" {
		if err := deploy.ValidateTargetName(cfg.DeployTarget); err != nil {
			errs = append(errs, fmt.Sprintf("deployTarget %s", err.Error()))
		}
	}


	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}

// isSemver reports whether s matches the pattern \d+\.\d+\.\d+.
var semverPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

func isSemver(s string) bool {
	return semverPattern.MatchString(s)
}
