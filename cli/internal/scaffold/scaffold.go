package scaffold

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"text/template"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

//go:embed templates
var templateFS embed.FS

// SDKVersion is the SDK version used when scaffolding new projects.
// Update this when releasing a new SDK version.
const SDKVersion = "latest"

// DefaultAssetBaseURL is the Blocks asset host used when --blocks-base-url is
// unset. It is also the last-resort fallback for the webapp backend origin
// when no profile / flag / env provides one.
const DefaultAssetBaseURL = "https://app.blocks.ai"

// agentNameRe enforces the bare agent-name pattern (matches registry column).
var agentNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// Project generates all project files in dir based on the given config.
//
// For webapp scaffolds (cfg.Mode == "webapp"), cards must contain one entry
// per agent listed in cfg.Agents (in the same order). Provider/consumer
// scaffolds ignore cards and may be passed nil.
func Project(dir string, cfg wizard.Config, cards []*cardfetch.AgentCard) error {
	if cfg.Mode == "webapp" {
		return scaffoldWebapp(dir, cfg, cards)
	}
	if cfg.Mode != "provider" && cfg.Mode != "consumer" {
		return fmt.Errorf("unsupported mode %q (use \"provider\", \"consumer\", or \"webapp\")", cfg.Mode)
	}
	if cfg.Language != "node" && cfg.Language != "python" {
		return fmt.Errorf("unsupported language %q (use \"node\" or \"python\")", cfg.Language)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// .env with CDM config placeholders
	if err := copyEmbedded(dir, ".env", "templates/common/env"); err != nil {
		return err
	}

	// .gitignore (keeps .env out of version control)
	if err := copyEmbedded(dir, ".gitignore", "templates/common/gitignore"); err != nil {
		return err
	}

	switch cfg.Mode {
	case "consumer":
		if cfg.Language == "node" {
			return scaffoldConsumerNode(dir, cfg)
		}
		return scaffoldConsumerPython(dir, cfg)
	default: // "provider"
		if cfg.Language == "node" {
			return scaffoldProviderNode(dir, cfg)
		}
		return scaffoldProviderPython(dir, cfg)
	}
}

func scaffoldProviderNode(dir string, cfg wizard.Config) error {
	if err := writeAgentCard(dir, cfg); err != nil {
		return err
	}

	// handler.ts (static)
	if err := copyEmbedded(dir, "handler.ts", "templates/node/provider/handler.ts"); err != nil {
		return err
	}

	// trigger.ts (template on disk)
	if err := writeTemplate(dir, "trigger.ts", "templates/node/provider/trigger.ts.tmpl", cfg); err != nil {
		return err
	}

	// package.json
	if err := writeNodePackageJSON(dir, cfg); err != nil {
		return err
	}

	// Dockerfile
	if cfg.Docker {
		if err := copyEmbedded(dir, "Dockerfile", "templates/node/provider/Dockerfile"); err != nil {
			return err
		}
	}

	return nil
}

func scaffoldProviderPython(dir string, cfg wizard.Config) error {
	if err := writeAgentCard(dir, cfg); err != nil {
		return err
	}

	// handler.py (template on disk)
	if err := writeTemplate(dir, "handler.py", "templates/python/provider/handler.py.tmpl", cfg); err != nil {
		return err
	}

	// trigger.py (template on disk)
	if err := writeTemplate(dir, "trigger.py", "templates/python/provider/trigger.py.tmpl", cfg); err != nil {
		return err
	}

	// pyproject.toml (template on disk)
	if err := writeTemplate(dir, "pyproject.toml", "templates/python/provider/pyproject.toml.tmpl", cfg); err != nil {
		return err
	}

	// Dockerfile
	if cfg.Docker {
		if err := copyEmbedded(dir, "Dockerfile", "templates/python/provider/Dockerfile"); err != nil {
			return err
		}
	}

	return nil
}

// Structs for ordered JSON output of agent-card.json.
// Field order matches the canonical layout: identity → capabilities → io → streams → tags → runtime.

type agentCardJSON struct {
	Identity     identityJSON          `json:"identity"`
	Capabilities capabilitiesJSON      `json:"capabilities"`
	IO           *ioJSON               `json:"io,omitempty"`
	Streams      map[string]streamJSON `json:"streams,omitempty"`
	Tags         []tagJSON             `json:"tags"`
	Runtime      runtimeJSON           `json:"runtime"`
}

type identityJSON struct {
	AgentName   string       `json:"agentName"`
	DisplayName string       `json:"displayName"`
	Description string       `json:"description"`
	Version     string       `json:"version"`
	Provider    providerJSON `json:"provider"`
}

type providerJSON struct {
	Organization string `json:"organization"`
}

type capabilitiesJSON struct {
	TaskKinds []string `json:"taskKinds"`
}

type ioJSON struct {
	Inputs  []ioInputJSON  `json:"inputs"`
	Outputs []ioOutputJSON `json:"outputs"`
}

type ioInputJSON struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	ContentType string          `json:"contentType"`
	Required    bool            `json:"required"`
	Example     json.RawMessage `json:"example,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type ioOutputJSON struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	ContentType string          `json:"contentType"`
	Guaranteed  bool            `json:"guaranteed"`
	Example     json.RawMessage `json:"example,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

type streamJSON struct {
	Direction   string `json:"direction"`
	Format      string `json:"format"`
	Description string `json:"description,omitempty"`
	Affinity    string `json:"affinity,omitempty"`
}

type tagJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type runtimeJSON struct {
	Handler           string `json:"handler"`
	HandlerExport     string `json:"handlerExport"`
	Concurrency       int    `json:"concurrency"`
	ExpectedInstances int    `json:"expectedInstances"`
}

// writeAgentCard generates agent-card.json programmatically for clean JSON output.
func writeAgentCard(dir string, cfg wizard.Config) error {
	isNode := cfg.Language == "node"

	handler := "./handler.py"
	export := "handler"
	if isNode {
		handler = "./handler.ts"
		export = "default"
	}

	taskKinds := cfg.TaskKinds
	if len(taskKinds) == 0 {
		taskKinds = []string{"request"}
	}

	displayName := cfg.DisplayName
	if displayName == "" {
		displayName = cfg.Name
	}

	card := agentCardJSON{
		Identity: identityJSON{
			AgentName:   cfg.Name,
			DisplayName: displayName,
			Description: cfg.Description,
			Version:     "1.0.0",
			Provider:    providerJSON{Organization: cfg.Name},
		},
		Capabilities: capabilitiesJSON{
			TaskKinds: taskKinds,
		},
		IO: &ioJSON{
			Inputs: []ioInputJSON{
				{
					ID:          "request",
					Description: "Task input.",
					ContentType: "application/json",
					Required:    true,
					Example:     json.RawMessage(`{"text":"Hello from the Blocks Network!"}`),
					Schema:      json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string","title":"Input Text","default":"Hello from the Blocks Network!"}}}`),
				},
			},
			// text/plain outputs omit schema and example because the
			// dashboard renders them as raw text with no structured fields.
			Outputs: []ioOutputJSON{
				{
					ID:          "result",
					Description: "Task output.",
					ContentType: "text/plain",
					Guaranteed:  true,
				},
			},
		},
		Tags: []tagJSON{
			{
				ID:          "main",
				Name:        "Main",
				Description: "Handles tasks",
			},
		},
		Runtime: runtimeJSON{
			Handler:           handler,
			HandlerExport:     export,
			Concurrency:       cfg.Concurrency,
			ExpectedInstances: cfg.ExpectedInstances,
		},
	}

	// Add stream definition when streaming is enabled.
	// Pipe-only agents get a named "stream" with dedicated affinity;
	// agents that support request tasks get the _default stream.
	if cfg.Streaming {
		if isPipeOnly(taskKinds) {
			card.Streams = map[string]streamJSON{
				"stream": {
					Direction:   "outbound",
					Format:      "events",
					Description: "Primary event stream.",
					Affinity:    "dedicated",
				},
			}
		} else {
			card.Streams = map[string]streamJSON{
				"_default": {
					Direction:   "outbound",
					Format:      "events",
					Description: "Default event stream.",
				},
			}
		}
	}

	data, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent-card.json: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, "agent-card.json"), data, 0644)
}

// isPipeOnly returns true when the task kinds list contains only "pipe"
// (no "request"). Used to decide between named stream and _default.
func isPipeOnly(taskKinds []string) bool {
	for _, k := range taskKinds {
		if k == "request" {
			return false
		}
	}
	return len(taskKinds) > 0
}

func writeNodePackageJSON(dir string, cfg wizard.Config) error {
	pkg := map[string]interface{}{
		"name":    cfg.Name,
		"version": "1.0.0",
		"type":    "module",
		"private": true,
		"scripts": map[string]string{
			"start": "blocks run",
			"check": "blocks check",
		},
		"dependencies": map[string]string{
			"@blocks-network/sdk": SDKVersion,
			"dotenv":              "^16.4.5",
		},
		"devDependencies": map[string]string{
			"typescript": "^5.4.5",
		},
	}

	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal package.json: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)
}

func writeNodeConsumerPackageJSON(dir string, cfg wizard.Config) error {
	pkg := map[string]interface{}{
		"name":    cfg.Name,
		"version": "1.0.0",
		"type":    "module",
		"private": true,
		"scripts": map[string]string{
			"start": "tsx index.ts",
		},
		"dependencies": map[string]string{
			"@blocks-network/sdk": SDKVersion,
			"dotenv":              "^16.4.5",
		},
		"devDependencies": map[string]string{
			"typescript": "^5.4.5",
			"tsx":        "^4.19.2",
		},
	}

	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal package.json: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, "package.json"), data, 0644)
}

// scaffoldWebapp creates a webapp project from a slice of pre-fetched
// AgentCards. The caller (cmd/init.go) is responsible for resolving
// credentials, contacting the registry, and constructing the cards; this
// function is a pure transformation: card slice → files on disk.
func scaffoldWebapp(dir string, cfg wizard.Config, cards []*cardfetch.AgentCard) error {
	if len(cfg.Agents) == 0 {
		return fmt.Errorf("at least one agent is required for --mode webapp")
	}
	if len(cards) != len(cfg.Agents) {
		return fmt.Errorf("scaffoldWebapp: expected %d cards, got %d", len(cfg.Agents), len(cards))
	}
	for _, name := range cfg.Agents {
		if !agentNameRe.MatchString(name) {
			return fmt.Errorf("use the bare agent name (e.g. 'translator'), not the namespaced form (e.g. 'acme/translator'); got %q", name)
		}
	}

	baseURL := cfg.BlocksBaseURL
	if baseURL == "" {
		baseURL = DefaultAssetBaseURL
	}

	backendURL := cfg.BackendBaseURL
	if backendURL == "" {
		backendURL = baseURL
	}

	vars := EmbedVars{
		ProjectName:        cfg.Name,
		WidgetVersion:      WidgetVersion(),
		BlocksAssetBaseUrl: baseURL,
		BackendBaseUrl:     backendURL,
		CardSnapshotDate:   time.Now().UTC().Format("2006-01-02"),
	}

	app, err := GenerateApp(cards, vars)
	if err != nil {
		return fmt.Errorf("generate embed scaffold: %w", err)
	}

	webDir := filepath.Join(dir, "web")
	if err := os.MkdirAll(webDir, 0755); err != nil {
		return fmt.Errorf("create web directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte(app.IndexHTML), 0644); err != nil {
		return fmt.Errorf("write web/index.html: %w", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "app.js"), []byte(app.AppJS), 0644); err != nil {
		return fmt.Errorf("write web/app.js: %w", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "styles.css"), []byte(app.StylesCSS), 0644); err != nil {
		return fmt.Errorf("write web/styles.css: %w", err)
	}
	// README lives at the project root, not under web/.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(app.ReadmeMD), 0644); err != nil {
		return fmt.Errorf("write README.md: %w", err)
	}

	cfg2 := &config.BlocksConfig{
		TemplateVersion: "1.0.0",
		Agents:          append([]string(nil), cfg.Agents...),
		BackendBaseUrl:  backendURL,
	}
	if err := config.Save(filepath.Join(dir, "blocks.config.json"), cfg2); err != nil {
		return fmt.Errorf("write blocks.config.json: %w", err)
	}

	return nil
}

func copyEmbedded(dir, filename, embedPath string) error {
	data, err := templateFS.ReadFile(embedPath)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", embedPath, err)
	}
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

// writeTemplate loads embedPath from the embedded template FS, parses it as
// text/template, executes it against cfg, and writes the output to
// filepath.Join(dir, outName). Use this for any interpolated template that
// lives on disk as a .tmpl file.
func writeTemplate(dir, outName, embedPath string, cfg wizard.Config) error {
	raw, err := templateFS.ReadFile(embedPath)
	if err != nil {
		return fmt.Errorf("read embedded template %s: %w", embedPath, err)
	}
	tmpl, err := template.New(outName).Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse template %s: %w", embedPath, err)
	}
	f, err := os.Create(filepath.Join(dir, outName))
	if err != nil {
		return fmt.Errorf("create %s: %w", outName, err)
	}
	defer f.Close()
	return tmpl.Execute(f, cfg)
}

func scaffoldConsumerNode(dir string, cfg wizard.Config) error {
	if err := writeTemplate(dir, "index.ts", "templates/node/consumer/index.ts.tmpl", cfg); err != nil {
		return err
	}
	return writeNodeConsumerPackageJSON(dir, cfg)
}

func scaffoldConsumerPython(dir string, cfg wizard.Config) error {
	if err := writeTemplate(dir, "main.py", "templates/python/consumer/main.py.tmpl", cfg); err != nil {
		return err
	}
	return writeTemplate(dir, "pyproject.toml", "templates/python/consumer/pyproject.toml.tmpl", cfg)
}
