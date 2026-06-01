package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(runCmd)
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start an agent from agent-card.json in the current directory",
	Long: `Start an agent by delegating to the appropriate language-native runner.

For Node projects (detected by package.json): finds and runs local "blocks-run" binary
For Python projects (detected by pyproject.toml): uses venv walk-up with "python -m blocks_network"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd := mustCwd()

		// Pre-check: load credentials and verify they're still valid, then update .env
		// so the SDK process receives a fresh BLOCKS_API_KEY.
		if _, err := loadCredentials(); err != nil {
			// Non-fatal: allow run even without auth (may fail at registration)
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}

		cardPath := filepath.Join(cwd, "agent-card.json")

		if _, statErr := os.Stat(cardPath); os.IsNotExist(statErr) {
			return fmt.Errorf("could not read %s\nCreate an agent-card.json or run: blocks init <name>", cardPath)
		}

		// Validate agent card against schema before running. The shim runs
		// the legacy `skills` → `tags` rewrite first so a stale card
		// boots locally with a warning instead of failing on a confusing
		// schema rejection — same UX as `blocks publish`.
		res := validateCardWithLegacyShim(cardPath)
		if len(res.Errors) > 0 {
			fmt.Fprintf(os.Stderr, "agent-card.json validation failed:\n")
			for _, e := range res.Errors {
				fmt.Fprintf(os.Stderr, "  - %s\n", e)
			}
			return fmt.Errorf("fix validation errors or run 'blocks check' for details")
		}

		// Detect project type from handler extension and project files.
		// The handler path comes from the validated (and possibly shimmed)
		// card so a `skills`-only legacy card can still resolve its handler.
		var handler string
		if rt, ok := res.Card["runtime"].(map[string]interface{}); ok {
			if h, ok := rt["handler"].(string); ok {
				handler = h
			}
		}
		projectType := detectProjectType(cwd, handler)

		switch projectType {
		case "node":
			return execNode(cwd)
		case "python":
			return execPython(cwd)
		default:
			return fmt.Errorf("could not detect project type\nExpected package.json (Node) or pyproject.toml (Python) in %s", cwd)
		}
	},
}

func detectProjectType(dir, handler string) string {
	// First check handler file extension
	handler = strings.ToLower(handler)
	if strings.HasSuffix(handler, ".ts") || strings.HasSuffix(handler, ".js") {
		return "node"
	}
	if strings.HasSuffix(handler, ".py") {
		return "python"
	}

	// Fall back to project file detection
	if fileExists(filepath.Join(dir, "package.json")) {
		return "node"
	}
	if fileExists(filepath.Join(dir, "pyproject.toml")) {
		return "python"
	}

	return ""
}

func execNode(cwd string) error {
	node, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node not found — install Node.js to run Node agents")
	}

	// Search strategy: look for blocks-run in node_modules/.bin (symlink
	// from npm), then for the built dist/cli/run.js relative to the SDK
	// workspace (handles cases where the symlink is missing or dangling).
	// Walk from cwd upward to find a workspace root.
	dir := cwd
	for {
		// Check node_modules/.bin/blocks-run (standard npm bin).
		// On Windows, use the .cmd wrapper and execute it directly;
		// on Unix, invoke the script via node.
		binCandidate := filepath.Join(dir, "node_modules", ".bin", "blocks-run")
		if runtime.GOOS == "windows" {
			cmdCandidate := binCandidate + ".cmd"
			if fileExists(cmdCandidate) {
				fmt.Println("[blocks] Delegating to Node SDK (blocks-run)...")
				return sysExec(cmdCandidate, []string{"blocks-run.cmd"}, cwd)
			}
		} else if fileExists(binCandidate) {
			fmt.Println("[blocks] Delegating to Node SDK (blocks-run)...")
			return sysExec(node, []string{"node", binCandidate}, cwd)
		}

		// Check sdks/node/dist/cli/run.js (direct path in blocks-sdk workspace)
		distCandidate := filepath.Join(dir, "sdks", "node", "dist", "cli", "run.js")
		if fileExists(distCandidate) {
			fmt.Println("[blocks] Delegating to Node SDK (blocks-run)...")
			return sysExec(node, []string{"node", distCandidate}, cwd)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Fall back to PATH
	blocksRun, lookErr := exec.LookPath("blocks-run")
	if lookErr != nil {
		return fmt.Errorf("blocks-run not found — install @blocks-network/sdk or run 'npm install' first")
	}
	fmt.Println("[blocks] Delegating to Node SDK (blocks-run)...")
	return sysExec(blocksRun, []string{"blocks-run"}, cwd)
}

func execPython(cwd string) error {
	// Walk up from cwd to find a .venv with a Python interpreter.
	venvPython := findVenvPython(cwd)
	if venvPython != "" {
		fmt.Println("[blocks] Delegating to Python SDK (venv)...")
		return sysExec(venvPython, []string{"python", "-m", "blocks_network"}, cwd)
	}

	// Fall back to blocks-run on PATH.
	blocksRun, err := exec.LookPath("blocks-run")
	if err == nil {
		fmt.Println("[blocks] Delegating to Python SDK (blocks-run)...")
		return sysExec(blocksRun, []string{"blocks-run"}, cwd)
	}

	return fmt.Errorf("no Python venv found. Run 'make setup' or activate a virtualenv with the Blocks SDK")
}

// findVenvPython walks up from startDir looking for .venv/bin/python (Unix)
// or .venv/Scripts/python.exe (Windows). Returns the absolute path to the
// interpreter, or "" if none found.
func findVenvPython(startDir string) string {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	for {
		candidate := venvInterpreterPath(dir)
		if fileExists(candidate) {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// venvInterpreterPath returns the expected Python interpreter path inside
// a .venv directory, accounting for platform differences.
func venvInterpreterPath(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, ".venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, ".venv", "bin", "python")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cliVersionEnvKey is the environment variable used to pass the CLI
// version to the delegated SDK process.
const cliVersionEnvKey = "BLOCKS_CLI_VERSION"

// withCLIVersion returns a copy of env with BLOCKS_CLI_VERSION set to
// the current CLI version. If the variable already exists in env it is
// replaced; otherwise it is appended.
func withCLIVersion(env []string) []string {
	entry := cliVersionEnvKey + "=" + Version
	prefix := cliVersionEnvKey + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = entry
			return env
		}
	}
	return append(env, entry)
}

// sysExec is implemented per-platform:
//   - Unix (run_unix.go): replaces the current process via syscall.Exec
//   - Windows (run_windows.go): runs as a subprocess via exec.Command
