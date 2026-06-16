package scaffold_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/scaffold"
)

// TestWidgetVersionParity asserts that WidgetVersion() matches the "version"
// field in blocks-sdk/embed-auth/package.json.
//
// At test time the source tree is accessible; we walk up from this file to
// find the repo root and load the package.json directly. Any drift between
// widget_version.txt and embed-auth/package.json fails CI before it ships.
func TestWidgetVersionParity(t *testing.T) {
	// Locate this source file at runtime; walk up to find the repo root.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}

	// thisFile is blocks-sdk/cli/internal/scaffold/widget_version_test.go
	// Walk up: scaffold → internal → cli → blocks-sdk → repo root
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))))
	pkgPath := filepath.Join(repoRoot, "blocks-sdk", "embed-auth", "package.json")

	data, err := os.ReadFile(pkgPath)
	if err != nil {
		t.Fatalf("read embed-auth/package.json: %v (resolved path: %s)", err, pkgPath)
	}

	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("parse embed-auth/package.json: %v", err)
	}
	if pkg.Version == "" {
		t.Fatal("embed-auth/package.json has empty version field")
	}

	got := scaffold.WidgetVersion()
	if strings.TrimSpace(got) != strings.TrimSpace(pkg.Version) {
		t.Errorf("WidgetVersion() = %q, embed-auth/package.json version = %q; update widget_version.txt",
			got, pkg.Version)
	}
}

// TestWidgetVersionNonEmpty asserts that WidgetVersion() returns a non-empty
// semver-like string (digits and dots, at minimum).
func TestWidgetVersionNonEmpty(t *testing.T) {
	v := scaffold.WidgetVersion()
	if v == "" {
		t.Fatal("WidgetVersion() returned empty string")
	}
	// Basic semver shape check: must contain at least one dot.
	if !strings.Contains(v, ".") {
		t.Errorf("WidgetVersion() = %q, expected a semver string (e.g. '0.1.0')", v)
	}
}
