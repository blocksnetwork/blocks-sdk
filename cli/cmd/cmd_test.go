package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout and returns whatever was printed.
//
// WARNING: This function mutates the process-wide os.Stdout. Tests using
// this helper must NOT use t.Parallel().
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(fmt.Sprintf("os.Pipe failed: %v", err))
	}
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// writeValidProject creates a temp directory with a valid agent-card.json and handler.
func writeValidProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	card := map[string]interface{}{
		"identity": map[string]interface{}{
			"agentName":   "test_agent",
			"displayName": "test_agent",
			"description": "A test agent",
			"version":     "1.0.0",
			"provider":    map[string]interface{}{"organization": "TestOrg"},
		},
		"capabilities": map[string]interface{}{
			"taskKinds": []interface{}{"request"},
		},
		"skills": []interface{}{
			map[string]interface{}{
				"id":   "main",
				"name": "Main Skill",
			},
		},
		"runtime": map[string]interface{}{
			"handler": "./handler.py",
		},
	}

	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-card.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVersionCommand(t *testing.T) {
	old := Version
	Version = "1.2.3-test"
	defer func() { Version = old }()

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"version"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(output, "1.2.3-test") {
		t.Errorf("version output = %q, want to contain %q", output, "1.2.3-test")
	}
}

func TestCheckCommandMissingFile(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	rootCmd.SetArgs([]string{"check"})
	err := rootCmd.Execute()
	if err == nil {
		t.Error("expected error for missing agent-card.json")
	}
}

func TestCheckCommandWithPath(t *testing.T) {
	dir := writeValidProject(t)

	cardPath := filepath.Join(dir, "agent-card.json")
	rootCmd.SetArgs([]string{"check", cardPath})

	// Suppress stdout from check output
	output := captureStdout(func() {
		if err := rootCmd.Execute(); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})
	_ = output
}

func TestInitCommandNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myagent", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	agentDir := filepath.Join(dir, "myagent")
	if _, err := os.Stat(agentDir); err != nil {
		t.Error("expected myagent directory to be created")
	}
	if _, err := os.Stat(filepath.Join(agentDir, "agent-card.json")); err != nil {
		t.Error("expected agent-card.json to be created")
	}
}

func TestInitCommandNonInteractiveNode(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myagent", "--yes", "-l", "node"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	agentDir := filepath.Join(dir, "myagent")
	if _, err := os.Stat(filepath.Join(agentDir, "handler.ts")); err != nil {
		t.Error("expected handler.ts to be created")
	}
	if _, err := os.Stat(filepath.Join(agentDir, "package.json")); err != nil {
		t.Error("expected package.json to be created")
	}
}

func TestInitCommandMissingNameNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing name in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "agent name is required") {
		t.Errorf("expected 'agent name is required' error, got: %v", err)
	}
}

func TestInitCommandInvalidLanguage(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "myagent", "--yes", "-l", "ruby"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Errorf("expected 'unsupported language' error, got: %v", err)
	}
}

func TestInitCommandDirectoryAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	// Pre-create the directory
	if err := os.Mkdir(filepath.Join(dir, "myagent"), 0755); err != nil {
		t.Fatal(err)
	}

	initYes = false
	initLanguage = ""

	rootCmd.SetArgs([]string{"init", "myagent", "--yes"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for existing directory")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestInitCommandConsumerNonInteractive(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initType = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myconsumer", "--type", "consumer", "--language", "node", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	consumerDir := filepath.Join(dir, "myconsumer")
	if _, err := os.Stat(filepath.Join(consumerDir, "index.ts")); err != nil {
		t.Error("expected index.ts in consumer project")
	}
	if _, err := os.Stat(filepath.Join(consumerDir, "agent-card.json")); err == nil {
		t.Error("consumer project should not contain agent-card.json")
	}
}

func TestInitCommandConsumerPythonNonInteractiveDescription(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initType = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "myconsumer", "--type", "consumer", "--language", "python", "--yes"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, "myconsumer", "pyproject.toml"))
	if err != nil {
		t.Fatalf("read pyproject.toml: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `description = "myconsumer consumer"`) {
		t.Errorf("pyproject.toml should describe the project as a consumer, got:\n%s", content)
	}
	if strings.Contains(content, `description = "myconsumer agent"`) {
		t.Errorf("pyproject.toml leaked provider default description:\n%s", content)
	}
}

func TestLoadEnvFileCRLF(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("FOO=bar\r\nBAZ=qux\r\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("FOO")
	os.Unsetenv("BAZ")
	defer os.Unsetenv("FOO")
	defer os.Unsetenv("BAZ")

	loadEnvFile(envPath)

	if got := os.Getenv("FOO"); got != "bar" {
		t.Errorf("FOO = %q, want %q", got, "bar")
	}
	if got := os.Getenv("BAZ"); got != "qux" {
		t.Errorf("BAZ = %q, want %q", got, "qux")
	}
}

func TestLoadEnvFileLF(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("A=1\nB=2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("A")
	os.Unsetenv("B")
	defer os.Unsetenv("A")
	defer os.Unsetenv("B")

	loadEnvFile(envPath)

	if got := os.Getenv("A"); got != "1" {
		t.Errorf("A = %q, want %q", got, "1")
	}
	if got := os.Getenv("B"); got != "2" {
		t.Errorf("B = %q, want %q", got, "2")
	}
}

func TestLoadEnvFileSkipsExistingVars(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("EXISTING=new_value\r\n"), 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("EXISTING", "old_value")
	defer os.Unsetenv("EXISTING")

	loadEnvFile(envPath)

	if got := os.Getenv("EXISTING"); got != "old_value" {
		t.Errorf("EXISTING = %q, want %q (should not override)", got, "old_value")
	}
}

func TestInitCommandInvalidType(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldDir)

	initYes = false
	initLanguage = ""
	initType = ""

	captureStdout(func() {
		rootCmd.SetArgs([]string{"init", "x", "--type", "bogus", "--yes"})
		err := rootCmd.Execute()
		if err == nil {
			t.Fatal("expected error for unknown --type value")
		}
		if !strings.Contains(err.Error(), "unsupported type") {
			t.Errorf("error = %q, want 'unsupported type' wording", err.Error())
		}
	})

	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("directory should not be created on invalid --type")
	}
}
