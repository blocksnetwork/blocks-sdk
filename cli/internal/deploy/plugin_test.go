package deploy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
)

// writePluginYAML drops a plugin config into dir.
func writePluginYAML(t *testing.T, dir, fname, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fname), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadPlugins_HappyPath registers a plugin that prints a URL to stdout
// and verifies CLI can resolve and run it.
func TestLoadPlugins_HappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin tests use /bin/echo; skip on Windows")
	}
	Reset()

	dir := t.TempDir()
	writePluginYAML(t, dir, "echo.yml", `
name: echo
description: "Echo URL plugin"
command: ["/bin/echo", "https://example.com"]
credentialFlow: none
`)

	if err := LoadPlugins(dir); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	a, ok := Resolve("echo")
	if !ok {
		t.Fatal("echo not registered")
	}
	if a.Source != SourceDisk {
		t.Errorf("Source = %v, want %v", a.Source, SourceDisk)
	}

	assetsDir := t.TempDir()
	url, err := a.Upload(context.Background(), &auth.ProviderCredentials{}, assetsDir)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if url != "https://example.com" {
		t.Errorf("URL = %q, want https://example.com", url)
	}
}

// TestLoadPlugins_NonZeroExitFails verifies non-zero exits propagate.
func TestLoadPlugins_NonZeroExitFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin tests use /bin/sh; skip on Windows")
	}
	Reset()

	dir := t.TempDir()
	writePluginYAML(t, dir, "fail.yml", `
name: fail
command: ["/bin/sh", "-c", "echo nope >&2; exit 2"]
credentialFlow: none
`)
	if err := LoadPlugins(dir); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}

	a, _ := Resolve("fail")
	_, err := a.Upload(context.Background(), &auth.ProviderCredentials{}, t.TempDir())
	if err == nil {
		t.Fatal("expected error from non-zero exit")
	}
	if !strings.Contains(err.Error(), "command failed") {
		t.Errorf("error %q should mention 'command failed'", err)
	}
}

// TestLoadPlugins_UnknownProtocolRejected ensures forward-compatibility: a
// future protocolVersion the CLI doesn't understand surfaces a clear error.
func TestLoadPlugins_UnknownProtocolRejected(t *testing.T) {
	Reset()
	dir := t.TempDir()
	writePluginYAML(t, dir, "future.yml", `
protocolVersion: 99
name: future
command: ["/bin/echo", "x"]
credentialFlow: none
`)
	err := LoadPlugins(dir)
	if err == nil {
		t.Fatal("expected protocolVersion error")
	}
	if !strings.Contains(err.Error(), "protocolVersion") {
		t.Errorf("error %q should mention 'protocolVersion'", err)
	}
}

// TestLoadPlugins_MissingDirIsNotAnError keeps the CLI happy on a fresh
// install with no plugin directory.
func TestLoadPlugins_MissingDirIsNotAnError(t *testing.T) {
	Reset()
	if err := LoadPlugins(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Errorf("LoadPlugins on missing dir should be a no-op, got: %v", err)
	}
}

// TestLoadPlugins_CredentialEnvVar verifies the API-token-bound env var
// reaches the subprocess.
func TestLoadPlugins_CredentialEnvVar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plugin tests use /bin/sh; skip on Windows")
	}
	Reset()

	dir := t.TempDir()
	writePluginYAML(t, dir, "envcheck.yml", `
name: envcheck
command: ["/bin/sh", "-c", "echo \"https://example.com/$MY_TOKEN\""]
credentialFlow: api-token
credentialEnvVar: MY_TOKEN
`)
	if err := LoadPlugins(dir); err != nil {
		t.Fatalf("LoadPlugins: %v", err)
	}
	a, _ := Resolve("envcheck")
	url, err := a.Upload(context.Background(), &auth.ProviderCredentials{AccessToken: "tok-abc"}, t.TempDir())
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.HasSuffix(url, "/tok-abc") {
		t.Errorf("URL = %q, want suffix /tok-abc", url)
	}
}

// TestLoadPlugins_RejectMissingFields surfaces missing-required-field errors.
func TestLoadPlugins_RejectMissingFields(t *testing.T) {
	Reset()
	dir := t.TempDir()
	writePluginYAML(t, dir, "noname.yml", `
command: ["/bin/echo", "x"]
`)
	if err := LoadPlugins(dir); err == nil {
		t.Error("expected error for missing name")
	}
}

// TestLoadPlugins_RejectInvalidNameSlug ensures plugin manifest names obey
// the same slug pattern as `blocks.config.json:deployTarget`. Without this
// guard a manifest named "AWS" or "rail.way" loads, lets `blocks deploy AWS`
// succeed once, persists `"deployTarget": "AWS"` into the config — and then
// fails config validation on every subsequent deploy.
func TestLoadPlugins_RejectInvalidNameSlug(t *testing.T) {
	cases := []string{"AWS", "-railway", "rail.way", "rail way", "rail/way"}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			Reset()
			dir := t.TempDir()
			writePluginYAML(t, dir, "bad.yml", "name: "+bad+"\ncommand: [\"/bin/echo\", \"x\"]\n")
			err := LoadPlugins(dir)
			if err == nil {
				t.Fatalf("expected error for manifest name %q", bad)
			}
			if !strings.Contains(err.Error(), "must match") {
				t.Errorf("error %q should mention the slug pattern", err.Error())
			}
		})
	}
}

// TestValidatePluginDeployedURL exercises the impl_07 F5 URL policy: the
// plugin's stdout URL must be https:// or http://(localhost|127.0.0.1|::1).
// The post-deploy CLI prompt may persist this URL into an agent-card
// identity.webApps[].url, which is validated by the same rule server-side;
// rejecting at plugin exit avoids producing a card that the registry will
// later reject.
func TestValidatePluginDeployedURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://my-app.pages.dev", true},
		{"https://example.com/path?q=1", true},
		{"http://localhost", true},
		{"http://localhost:5173", true},
		{"http://127.0.0.1:8080", true},
		{"http://[::1]:8080", true},
		{"http://example.com", false},
		{"http://attacker.example.com", false},
		{"ftp://example.com", false},
		{"file:///etc/passwd", false},
		{"not-a-url", false},
		{"", false},
		// F1 follow-up: prefix-only checks let these through.
		{"https://", false},
		{"https:///path", false},
		{"https://?x", false},
		{"https://#frag", false},
		{"http:///localhost", false},

		// F1 round-2: parser-invalid + port out-of-range.
		{"https://example.com:65536", false},
		{"https://example.com:99999", false},
		{"http://localhost:65536", false},
		{"https://example.com:0", false},
		{"https://%zz", false},
		{"https://[x]", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			err := validatePluginDeployedURL(tc.url)
			if (err == nil) != tc.ok {
				t.Errorf("validatePluginDeployedURL(%q) err=%v, ok=%v", tc.url, err, tc.ok)
			}
		})
	}
}
