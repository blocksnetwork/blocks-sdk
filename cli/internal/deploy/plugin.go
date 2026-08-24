package deploy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"gopkg.in/yaml.v3"
)

// pluginProtocolVersion is the only protocol version v0 understands. Plugin
// YAML files may omit `protocolVersion` (it defaults to this); any other
// declared version is rejected.
const pluginProtocolVersion = 1

// pluginConfig models a single plugin YAML file at
// ~/.config/blocks/deploy-targets/<name>.yml.
type pluginConfig struct {
	ProtocolVersion  int               `yaml:"protocolVersion"`
	Name             string            `yaml:"name"`
	Description      string            `yaml:"description"`
	Command          rawCommand        `yaml:"command"`
	Env              map[string]string `yaml:"env"`
	CredentialFlow   string            `yaml:"credentialFlow"`
	CredentialPrompt string            `yaml:"credentialPrompt"`
	CredentialEnvVar string            `yaml:"credentialEnvVar"`
}

// rawCommand accepts either a single string ("/usr/bin/script") or a string
// array (["sh", "-c", "..."]) in the YAML.
type rawCommand []string

func (c *rawCommand) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*c = []string{node.Value}
		return nil
	case yaml.SequenceNode:
		var parts []string
		if err := node.Decode(&parts); err != nil {
			return err
		}
		*c = parts
		return nil
	default:
		return fmt.Errorf("command must be a string or a list of strings")
	}
}

// DefaultPluginDir returns the conventional plugin directory:
// $XDG_CONFIG_HOME/blocks/deploy-targets, falling back to
// ~/.config/blocks/deploy-targets.
func DefaultPluginDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "blocks", "deploy-targets"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "blocks", "deploy-targets"), nil
}

// LoadPlugins scans dir for *.yml and *.yaml files, validates each, and
// registers the resulting adapters. Missing dirs are not an error.
// Returns the first malformed-config error encountered; well-formed plugins
// registered before the failing one stay registered.
func LoadPlugins(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("deploy plugins: read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !(strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			continue
		}
		path := filepath.Join(dir, name)
		ad, err := loadPluginFile(path)
		if err != nil {
			return fmt.Errorf("deploy plugin %s: %w", name, err)
		}
		Register(ad)
	}
	return nil
}

func loadPluginFile(path string) (Adapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Adapter{}, err
	}
	var cfg pluginConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Adapter{}, fmt.Errorf("parse yaml: %w", err)
	}
	if cfg.ProtocolVersion == 0 {
		cfg.ProtocolVersion = pluginProtocolVersion
	}
	if cfg.ProtocolVersion != pluginProtocolVersion {
		return Adapter{}, fmt.Errorf("unknown protocolVersion %d (this CLI understands %d)", cfg.ProtocolVersion, pluginProtocolVersion)
	}
	if err := ValidateTargetName(cfg.Name); err != nil {
		return Adapter{}, fmt.Errorf("plugin manifest name %s", err.Error())
	}
	if len(cfg.Command) == 0 {
		return Adapter{}, fmt.Errorf("command is required")
	}
	flow := CredentialFlowKind(cfg.CredentialFlow)
	if flow == "" {
		flow = CredentialFlowNone
	}
	switch flow {
	case CredentialFlowAPIToken, CredentialFlowBrowserGrant, CredentialFlowNone:
	default:
		return Adapter{}, fmt.Errorf("credentialFlow %q is not one of api-token, browser-grant, none", cfg.CredentialFlow)
	}
	if flow == CredentialFlowAPIToken && cfg.CredentialEnvVar == "" {
		return Adapter{}, fmt.Errorf("credentialEnvVar is required when credentialFlow is api-token")
	}
	return Adapter{
		Name:             cfg.Name,
		Description:      cfg.Description,
		Source:           SourceDisk,
		Credential:       flow,
		CredentialEnvVar: cfg.CredentialEnvVar,
		CredentialPrompt: cfg.CredentialPrompt,
		Upload:           buildPluginUpload(cfg),
	}, nil
}

// buildPluginUpload returns an UploadFunc that shells out to the plugin's
// command. Stdout's last non-empty line is the deployed URL; stderr is
// streamed verbatim to the CLI's stderr. Non-zero exit fails the deploy and
// the captured stderr is surfaced as the error.
func buildPluginUpload(cfg pluginConfig) UploadFunc {
	return func(ctx context.Context, creds *auth.ProviderCredentials, assetsDir string) (string, error) {
		head := cfg.Command[0]
		args := cfg.Command[1:]
		cmd := exec.CommandContext(ctx, head, args...)
		cmd.Dir = filepath.Dir(filepath.Clean(assetsDir))

		envMap := os.Environ()
		envMap = append(envMap, expandEnv(cfg.Env, assetsDir, cfg.Name)...)
		if cfg.CredentialEnvVar != "" && creds != nil && creds.AccessToken != "" {
			envMap = append(envMap, cfg.CredentialEnvVar+"="+creds.AccessToken)
		}
		cmd.Env = envMap
		cmd.Stdin = nil

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("plugin %s: stdout pipe: %w", cfg.Name, err)
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("plugin %s: start %q: %w", cfg.Name, head, err)
		}

		lastURL, err := readLastNonEmptyLine(stdout)
		if err != nil {
			_ = cmd.Wait()
			return "", fmt.Errorf("plugin %s: read stdout: %w", cfg.Name, err)
		}

		if err := cmd.Wait(); err != nil {
			return "", fmt.Errorf("plugin %s: command failed: %w", cfg.Name, err)
		}
		if lastURL == "" {
			return "", fmt.Errorf("plugin %s: no URL on stdout", cfg.Name)
		}
		if err := validatePluginDeployedURL(lastURL); err != nil {
			return "", fmt.Errorf("plugin %s: %w", cfg.Name, err)
		}
		return lastURL, nil
	}
}

// validatePluginDeployedURL enforces the same rule as
// `identity.webApps[].url` in the agent-card schema. The post-deploy
// CLI prompt may persist this URL into a card, and a webapp URL that
// wouldn't pass card validation should never enter the system.
//
// Delegates to `ValidateWebAppURL` so the plugin and the CLI's
// `blocks check` step share one source of truth for webapp-URL semantics.
func validatePluginDeployedURL(raw string) error {
	if err := ValidateWebAppURL(raw); err != nil {
		return fmt.Errorf("stdout %w", err)
	}
	return nil
}

// expandEnv resolves $ASSETS_DIR / $PROJECT_NAME / $BLOCKS_DEPLOY_TARGET
// placeholders. Anything else is passed through unchanged.
func expandEnv(in map[string]string, assetsDir, target string) []string {
	projectName := filepath.Base(filepath.Dir(filepath.Clean(assetsDir)))
	repl := strings.NewReplacer(
		"$ASSETS_DIR", assetsDir,
		"$PROJECT_NAME", projectName,
		"$BLOCKS_DEPLOY_TARGET", target,
	)
	out := make([]string, 0, len(in))
	for k, v := range in {
		out = append(out, k+"="+repl.Replace(v))
	}
	return out
}

func readLastNonEmptyLine(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			last = line
		}
	}
	return last, scanner.Err()
}
