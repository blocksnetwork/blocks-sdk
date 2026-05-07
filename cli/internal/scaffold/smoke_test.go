package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	esbuild "github.com/evanw/esbuild/pkg/api"
	"github.com/pubnub/blocks-sdk/cli/internal/wizard"
)

// pyCompile runs `python3 -m py_compile <file>`. Returns nil on success,
// error with stderr on syntax failure. Skips the caller test if python3
// is not on PATH. This catches syntactic regressions in template expansion;
// it does NOT catch runtime issues such as NameError or missing imports.
func pyCompile(t *testing.T, path string) {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not on PATH; skipping Python parse smoke test: %v", err)
	}
	cmd := exec.Command(py, "-m", "py_compile", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python3 -m py_compile %s failed: %v\n%s", path, err, out)
	}
}

func TestScaffoldedProviderPythonParses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "smoke_agent")
	cfg := wizard.Config{
		Name:              "smoke_agent",
		Description:       "smoke",
		Language:          "python",
		Type:              "provider",
		Concurrency:       1,
		ExpectedInstances: 1,
		TaskKinds:         []string{"request"},
	}
	if err := Project(dir, cfg); err != nil {
		t.Fatal(err)
	}
	pyCompile(t, filepath.Join(dir, "handler.py"))
	pyCompile(t, filepath.Join(dir, "trigger.py"))
}

// tsParse calls esbuild.Transform with the TypeScript loader. It only
// checks syntactic validity (not types) — sufficient to catch template
// regressions. Reads the file from disk for accurate diagnostics.
func tsParse(t *testing.T, path string) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	result := esbuild.Transform(string(src), esbuild.TransformOptions{
		Loader:     esbuild.LoaderTS,
		LogLevel:   esbuild.LogLevelSilent,
		Sourcefile: path,
	})
	if len(result.Errors) > 0 {
		t.Fatalf("esbuild parse errors in %s:\n%+v", path, result.Errors)
	}
}

func TestScaffoldedProviderNodeParses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "smoke_agent")
	cfg := wizard.Config{
		Name:              "smoke_agent",
		Description:       "smoke",
		Language:          "node",
		Type:              "provider",
		Concurrency:       1,
		ExpectedInstances: 1,
		TaskKinds:         []string{"request"},
	}
	if err := Project(dir, cfg); err != nil {
		t.Fatal(err)
	}
	tsParse(t, filepath.Join(dir, "handler.ts"))
	tsParse(t, filepath.Join(dir, "trigger.ts"))
}

func TestScaffoldedConsumerNodeParses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	cfg := wizard.Config{
		Name:        "my_consumer",
		Description: "my_consumer consumer",
		Language:    "node",
		Type:        "consumer",
	}
	if err := Project(dir, cfg); err != nil {
		t.Fatal(err)
	}
	tsParse(t, filepath.Join(dir, "index.ts"))
}

func TestScaffoldedConsumerPythonParses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my_consumer")
	cfg := wizard.Config{
		Name:        "my_consumer",
		Description: "my_consumer consumer",
		Language:    "python",
		Type:        "consumer",
	}
	if err := Project(dir, cfg); err != nil {
		t.Fatal(err)
	}
	pyCompile(t, filepath.Join(dir, "main.py"))
}
