package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateOrgAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/api-key/create" || r.Header.Get("Authorization") != "Bearer bk_existing" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"apiKey":"bk_new","keyId":"k2","expiresAt":""}`))
	}))
	defer srv.Close()

	resp, err := CreateOrgAPIKey(srv.URL, "bk_existing", "org-finance", "cli-test")
	if err != nil {
		t.Fatalf("CreateOrgAPIKey: %v", err)
	}
	if resp.ApiKey != "bk_new" || resp.KeyId != "k2" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestUpsertEnvKey_ReplacesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "# Comment\nFOO=bar\nBLOCKS_API_KEY=old-value\nBAZ=qux\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "new-value"); err != nil {
		t.Fatalf("UpsertEnvKey failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY=new-value") {
		t.Errorf("expected BLOCKS_API_KEY=new-value, got:\n%s", content)
	}
	if strings.Contains(content, "BLOCKS_API_KEY=old-value") {
		t.Error("old value should have been replaced")
	}
	if !strings.Contains(content, "# Comment") {
		t.Error("comment should be preserved")
	}
	if !strings.Contains(content, "FOO=bar") {
		t.Error("other keys should be preserved")
	}
	if !strings.Contains(content, "BAZ=qux") {
		t.Error("other keys should be preserved")
	}
}

func TestUpsertEnvKey_AppendsMissing(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "# Comment\nFOO=bar\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "my-key"); err != nil {
		t.Fatalf("UpsertEnvKey failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY=my-key") {
		t.Errorf("expected BLOCKS_API_KEY=my-key to be appended, got:\n%s", content)
	}
	if !strings.Contains(content, "# Comment") {
		t.Error("comment should be preserved")
	}
	if !strings.Contains(content, "FOO=bar") {
		t.Error("other keys should be preserved")
	}
}

func TestUpsertEnvKey_DoesNotMatchPrefixCollision(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "BLOCKS_API_KEY_EXTRA=keep-me\nBLOCKS_API_KEY=old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "new"); err != nil {
		t.Fatalf("UpsertEnvKey failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)

	if !strings.Contains(content, "BLOCKS_API_KEY_EXTRA=keep-me") {
		t.Error("BLOCKS_API_KEY_EXTRA should not be modified")
	}
	if !strings.Contains(content, "BLOCKS_API_KEY=new") {
		t.Errorf("BLOCKS_API_KEY should be updated, got:\n%s", content)
	}
}

func TestUpsertEnvKey_SkipsCommentedOutLine(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "# BLOCKS_API_KEY=commented-out\nFOO=bar\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "new-value"); err != nil {
		t.Fatalf("UpsertEnvKey failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)

	if !strings.Contains(content, "# BLOCKS_API_KEY=commented-out") {
		t.Error("commented-out line should be preserved")
	}
	if !strings.Contains(content, "BLOCKS_API_KEY=new-value") {
		t.Errorf("new key should be appended, got:\n%s", content)
	}
}

func TestUpsertEnvKey_PreservesComments(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "# PubNub keys\nPUBNUB_PUBLISH_KEY=pub-123\n# Subscribe key\nPUBNUB_SUBSCRIBE_KEY=sub-456\n\n# API key\nBLOCKS_API_KEY=old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "new"); err != nil {
		t.Fatalf("UpsertEnvKey failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	lines := strings.Split(string(data), "\n")

	commentCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			commentCount++
		}
	}
	if commentCount != 3 {
		t.Errorf("expected 3 comments preserved, got %d", commentCount)
	}
}

func TestInjectEnvAt_CreatesNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "my_agent")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := InjectEnvAt(subDir, "BLOCKS_API_KEY", "test-key-123"); err != nil {
		t.Fatalf("InjectEnvAt failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(subDir, ".env"))
	if err != nil {
		t.Fatalf("expected .env to be created: %v", err)
	}
	if string(data) != "BLOCKS_API_KEY=test-key-123\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestInjectEnvAt_UpdatesExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=old\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := InjectEnvAt(tmpDir, "BLOCKS_API_KEY", "new-key"); err != nil {
		t.Fatalf("InjectEnvAt failed: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	if !strings.Contains(string(data), "BLOCKS_API_KEY=new-key") {
		t.Errorf("expected updated key, got: %q", string(data))
	}
}

func TestInjectEnvAt_InvalidDirReturnsError(t *testing.T) {
	err := InjectEnvAt("/nonexistent/path/that/does/not/exist", "KEY", "val")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestRemoveEnvKey(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")

	initial := "FOO=bar\nBLOCKS_API_KEY=old-key\nBAZ=qux\n"
	if err := os.WriteFile(envFile, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	RemoveEnvKey(envFile, "BLOCKS_API_KEY")

	data, _ := os.ReadFile(envFile)
	content := string(data)

	if strings.Contains(content, "BLOCKS_API_KEY") {
		t.Errorf("expected BLOCKS_API_KEY to be removed, got:\n%s", content)
	}
	if !strings.Contains(content, "FOO=bar") {
		t.Error("other keys should be preserved")
	}
	if !strings.Contains(content, "BAZ=qux") {
		t.Error("other keys should be preserved")
	}
}
