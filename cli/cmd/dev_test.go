package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/devserver"
)

// writeBlocksConfig writes a minimal blocks.config.json to dir and returns its path.
func writeBlocksConfig(t *testing.T, dir string, agents []string) string {
	t.Helper()
	cfg := map[string]interface{}{
		"templateVersion": "1.0.0",
		"agents":          agents,
		// Required since 1.2.0; a loopback http origin passes ValidateBackendBaseURL
		// so config.Validate no longer short-circuits before the dev-specific logic.
		"backendBaseUrl": "http://localhost:3001",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal blocks.config.json: %v", err)
	}
	path := filepath.Join(dir, "blocks.config.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write blocks.config.json: %v", err)
	}
	return path
}

// TestDevRunsWithoutLogin verifies the impl_07 follow-up #8 fix: `blocks dev`
// no longer hard-fails when `blocks login` has not been run. The dev server
// is a static-file host with hot reload; the embed-auth popup it serves
// authenticates the end user against the backend, and the CLI's API key
// never reaches the dev server.
func TestDevRunsWithoutLogin(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	writeBlocksConfig(t, tmpDir, []string{"myagent"})
	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	// `runDev` would block on srv.Run; cancel the context immediately to
	// exit the listen loop. The point of this test is that runDev does
	// NOT return early with "run 'blocks login' first" — any error other
	// than that, including a context-cancelled shutdown, is acceptable.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runDev(ctx)
	if err != nil && strings.Contains(err.Error(), "blocks login") {
		t.Errorf("runDev should not require login any more; got %q", err.Error())
	}
}

// TestDevRequiresBlocksConfig verifies that blocks dev exits with an error
// when blocks.config.json is missing.
func TestDevRequiresBlocksConfig(t *testing.T) {
	tmpDir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	t.Setenv("BLOCKS_BACKEND_URL", "http://unused")

	err := runDev(context.Background())
	if err == nil {
		t.Fatal("expected error when blocks.config.json is missing")
	}
	if !strings.Contains(err.Error(), "blocks.config.json") {
		t.Errorf("error = %q, want to mention 'blocks.config.json'", err.Error())
	}
}

// TestDevServerStartsWithoutBackendCalls verifies that starting the dev server
// no longer calls any /api/v1/auth/embed/dev-grants endpoints. The per-origin
// allowlist removal eliminated the dev-grant lifecycle.
func TestDevServerStartsWithoutBackendCalls(t *testing.T) {
	var backendCalled bool
	ts := http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendCalled = true
			w.WriteHeader(http.StatusNotFound)
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = ts.Serve(ln) }()
	defer ts.Close()
	backendURL := "http://" + ln.Addr().String()

	// Pick a free port for the dev server.
	devLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	devPort := devLn.Addr().(*net.TCPAddr).Port
	devLn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := devserver.New(devserver.Config{
		Port:           devPort,
		BackendBaseURL: backendURL,
		Agents:         []string{"myagent"},
	})
	go func() { _ = srv.Run(ctx) }()

	// Give the server time to bind and (formerly) hit the backend.
	time.Sleep(200 * time.Millisecond)

	if backendCalled {
		t.Error("blocks dev must not call any backend endpoints — dev-grant lifecycle removed")
	}
}

// TestDevBlocksEmbedDevJsServed verifies that /__blocks_embed_dev.js is served
// with Cache-Control: no-store and contains backendBaseUrl, cdmUrl, agents,
// but NOT devGrant.
func TestDevBlocksEmbedDevJsServed(t *testing.T) {
	// Pick a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := devserver.New(devserver.Config{
		Port:           port,
		BackendBaseURL: "http://localhost:3001",
		Agents:         []string{"myagent"},
	})
	go func() { _ = srv.Run(ctx) }()

	// Wait for the server to be ready.
	var lastErr error
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/__blocks_embed_dev.js", port))
		if err == nil {
			resp.Body.Close()
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		t.Fatalf("server did not become ready: %v", lastErr)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/__blocks_embed_dev.js", port))
	if err != nil {
		t.Fatalf("GET /__blocks_embed_dev.js: %v", err)
	}
	defer resp.Body.Close()

	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	for _, want := range []string{"__BLOCKS_EMBED_DEV__", "backendBaseUrl", "cdmUrl", "agents"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response body missing %q, got:\n%s", want, bodyStr)
		}
	}
	// devGrant is removed — assert it does NOT appear in the injected script.
	for _, forbidden := range []string{"devGrant", "expiresAt"} {
		if strings.Contains(bodyStr, forbidden) {
			t.Errorf("response body must NOT contain %q, got:\n%s", forbidden, bodyStr)
		}
	}
}
