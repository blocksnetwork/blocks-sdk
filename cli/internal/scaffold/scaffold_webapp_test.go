package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/config"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

func TestScaffoldWebapp_PersistsResolvedBackendURL(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	dir := t.TempDir()

	cfg := wizard.Config{
		Name:           "demo",
		Mode:           "webapp",
		Agents:         []string{"echo2"},
		BlocksBaseURL:  "https://assets.example.test",
		BackendBaseURL: "https://blocks.acme.com",
	}
	if err := Project(dir, cfg, []*cardfetch.AgentCard{card}); err != nil {
		t.Fatalf("Project: %v", err)
	}

	// blocks.config.json records the resolved backend.
	bc, err := config.Load(filepath.Join(dir, "blocks.config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if bc.BackendBaseUrl != "https://blocks.acme.com" {
		t.Errorf("config backendBaseUrl = %q, want https://blocks.acme.com", bc.BackendBaseUrl)
	}

	// app.js bakes the backend; index.html still loads the widget from the asset host.
	appJS, _ := os.ReadFile(filepath.Join(dir, "web", "app.js"))
	if !strings.Contains(string(appJS), `"https://blocks.acme.com"`) {
		t.Error("app.js must bake the backend URL")
	}
	// index.html builds the widget src as `base + '/embed/auth.<ver>.min.js'`,
	// where base is the JS-string-escaped asset host — so assert on the escaped
	// asset host plus the widget path fragment, not a concatenated literal.
	indexBytes, rerr := os.ReadFile(filepath.Join(dir, "web", "index.html"))
	if rerr != nil {
		t.Fatalf("read index.html: %v", rerr)
	}
	indexHTML := string(indexBytes)
	if !strings.Contains(indexHTML, `https:\/\/assets.example.test`) {
		t.Errorf("index.html must load the widget from the asset host; got:\n%s", indexHTML)
	}
	if !strings.Contains(indexHTML, "/embed/auth.") {
		t.Error("index.html must reference the embed-auth widget bundle path")
	}
	if strings.Contains(indexHTML, "blocks.acme.com") {
		t.Error("index.html must not reference the backend host (asset host only)")
	}
}

func TestScaffoldWebapp_BackendDefaultsToAssetBase(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	dir := t.TempDir()

	// No BackendBaseURL set → resolved to the asset base at scaffold time.
	cfg := wizard.Config{Name: "demo", Mode: "webapp", Agents: []string{"echo2"}}
	if err := Project(dir, cfg, []*cardfetch.AgentCard{card}); err != nil {
		t.Fatalf("Project: %v", err)
	}
	bc, _ := config.Load(filepath.Join(dir, "blocks.config.json"))
	if bc.BackendBaseUrl != DefaultAssetBaseURL {
		t.Errorf("config backendBaseUrl = %q, want default %q", bc.BackendBaseUrl, DefaultAssetBaseURL)
	}
}
