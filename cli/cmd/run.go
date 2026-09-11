package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
	"github.com/pubnub/blocks-sdk/cli/internal/clictx"
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

		// Pre-check: surface a clear warning early if the user isn't logged in.
		// The resolved key is injected into the delegated SDK process env by
		// buildChildEnv (see withAPIKey), so it need not be written to .env.
		if _, err := loadCredentials(); err != nil {
			// Non-fatal: allow run even without auth (may fail at registration)
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
		}

		cardPath := filepath.Join(cwd, "agent-card.json")

		if _, statErr := os.Stat(cardPath); os.IsNotExist(statErr) {
			return agentCardNotFoundError(cardPath)
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

	return pythonVenvNotFoundError()
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

// withTargetRuntimeEnv appends BLOCKS_CDM_URL and BLOCKS_BACKEND_URL pointing at
// the deployment named by backendURL, so the delegated SDK process resolves that
// deployment's keysets from its own CDM endpoint and registers against the same
// backend.
//
// backendURL is the deployment this invocation actually targets, not whichever
// profile is active — the two differ whenever an ambient backend URL displaces
// the profile, and the runtime would then resolve one deployment's keyset while
// registering against another. A no-op for an empty origin, which is the stock
// Blocks Network case: its CDM is the runtime's own built-in default and pinning
// anything would freeze an answer the runtime is meant to resolve for itself.
//
// The CDM endpoint is asked of cdm.EndpointFor rather than spelled out again
// here, so the endpoint the delegated agent fetches, the one login pins into
// .env, and the one the dev server injects into a browser payload cannot drift.
//
// It never overrides a value already present in env (an explicitly-set env var
// wins).
func withTargetRuntimeEnv(env []string, backendURL string) []string {
	base := strings.TrimRight(strings.TrimSpace(backendURL), "/")
	if base == "" {
		return env
	}
	env = setEnvDefault(env, cdm.URLEnv, cdm.EndpointFor(base))
	env = setEnvDefault(env, blocksBackendURLEnv, base)
	return env
}

// withAPIKey injects BLOCKS_API_KEY into env so the delegated SDK process
// authenticates with the active profile's stored key without it having to be
// written into the project .env. A no-op for an empty key, and it never
// overrides an explicitly-set BLOCKS_API_KEY (shell env or .env wins).
func withAPIKey(env []string, apiKey string) []string {
	if apiKey == "" {
		return env
	}
	return setEnvDefault(env, "BLOCKS_API_KEY", apiKey)
}

// setEnvDefault appends KEY=value to env only when KEY is not already present
// with a non-empty value. An empty value is treated as absent to prevent
// shadowing of resolved credentials.
func setEnvDefault(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			existingValue := e[len(prefix):]
			if existingValue != "" {
				// Non-empty value wins; do not override
				return env
			}
			// Empty value: replace in-place to avoid duplicate entries
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}

// withProjectEnv merges every assignment the project .env supplied into env, in the
// spelling the file used.
//
// This is the half of the project-file rule that makes the other half safe. The CLI's
// own process imports only the variables it consumes (cliProjectEnvKeys), because a
// file that arrives with a cloned repository must not be able to choose the CLI's
// proxy, its certificate authority, where it keeps credentials, or what code gets
// injected into a process holding a hosting-provider token. None of that reasoning
// applies to the agent: `blocks run` exists to execute this repository's own handler,
// so the repository already decides what that process runs, and a NODE_OPTIONS or a
// PYTHONPATH it set buys an attacker nothing it did not already have. Withholding them
// from the child would only break the legitimate uses — PYTHONPATH for a src layout,
// NODE_OPTIONS for a heap size or source maps — and every scaffolded agent's own
// settings besides.
//
// Values the real environment already carries still win, via setEnvDefault, and the
// loader never recorded an assignment for a variable the environment held non-empty in
// the first place, so this cannot displace a shell export.
//
// Entries a decline withdrew are gone from projectEnv() by then, so a pin the CLI
// refused for itself is not quietly re-delivered to the runtime it named.
func withProjectEnv(env []string) []string {
	for _, e := range projectEnv() {
		env = setEnvDefault(env, e.name, e.value)
	}
	return env
}

// buildChildEnv assembles the environment for the delegated SDK process:
// the parent env + the project .env + BLOCKS_CLI_VERSION, plus the CDM/backend
// pointers of the deployment this invocation targets, plus BLOCKS_API_KEY resolved
// from the active profile/credentials so the runner authenticates without the key
// being written to .env. Explicitly-set env values always win.
func buildChildEnv() []string {
	env := withCLIVersion(withProjectEnv(os.Environ()))
	// The pointers travel with the deployment the user picked, not with the
	// enterprise verdict — the same rule `blocks login --write-env` applies when it
	// writes these two variables into a project .env, and the two have to agree or
	// `blocks run` and a directly launched script would target differently from one
	// file. A deployment that reports itself as non-enterprise, and one whose
	// discovery request never completed, both still serve the keysets a runtime has
	// to resolve from them: gating on the verdict drops the pointers in exactly
	// those cases, and the agent then resolves stock Network keysets while the CLI
	// registers it elsewhere.
	//
	// The origin is the effective target, never the merely-active profile: a
	// displaced profile's CDM endpoint belongs to a deployment this run will not
	// touch. Asking clictx covers both directions — a profile that is still the
	// target, and an override pointing at some other deployment — and yields ""
	// when the user pointed at nothing, which is stock Blocks Network and needs no
	// pointers at all.
	env = withTargetRuntimeEnv(env, clictx.ChosenBackendURL())
	if key, err := loadCredentials(); err == nil {
		env = withAPIKey(env, key)
	}
	return env
}

// sysExec is implemented per-platform:
//   - Unix (run_unix.go): replaces the current process via syscall.Exec
//   - Windows (run_windows.go): runs as a subprocess via exec.Command
