package cmd

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These cases cover the boundary the project-file loader draws between two processes,
// which is what replaced a denylist that had been found incomplete three times over.
//
// The rule is not "these variables are dangerous" — that category kept growing, from
// transport and TLS trust, to state location, to execution control — but "this process
// imports the variables it consumes, and nothing else". The three properties below are
// held apart on purpose, because a change that satisfies any two of them and not the
// third is easy to write and wrong:
//
//   - a variable that reconfigures a process must not reach the CLI's own, whose
//     environment is inherited by a `blocks deploy` plugin invoked with a
//     hosting-provider credential appended, and read by `blocks upgrade` to choose
//     where it writes the next binary;
//   - the same variable must still reach the delegated agent `blocks run` starts,
//     because that is the process a project file exists to configure and the reason an
//     allowlist was rejected the first time it was proposed;
//   - a value the shell exported is the operator's own intent and must survive both.

// executionControlEnv is the set that made the denylist's third miss: each of these
// runs code of the file's choosing in a process this CLI starts.
//
// NODE_OPTIONS=--require and PYTHONPATH reach the interpreter `blocks run` execs
// (run_unix.go replaces the process image with node or python, inheriting the
// environment). LD_PRELOAD, DYLD_INSERT_LIBRARIES and BASH_ENV reach the vendor CLI a
// deploy plugin shells out to, which is invoked with the hosting-provider token in its
// environment. BLOCKS_INSTALL_DIR is the directory `blocks upgrade` writes a freshly
// downloaded binary into.
var executionControlEnv = map[string]string{
	"NODE_OPTIONS":           "--require ./from-the-repository.js",
	"PYTHONPATH":             "./from-the-repository",
	"LD_PRELOAD":             "./from-the-repository.so",
	"DYLD_INSERT_LIBRARIES":  "./from-the-repository.dylib",
	"BASH_ENV":               "./from-the-repository.sh",
	"BLOCKS_INSTALL_DIR":     "./from-the-repository/bin",
	"AGENT_MODEL":            "acme-large",
	"OPENWEATHER_UNITS":      "metric",
	"BLOCKS_DEBUG_INTERNAL":  "diagnostics",
	"MY_AGENT_WEBHOOK_TOKEN": "tok_project",
}

// projectEnvFileContent renders executionControlEnv as a .env, in a stable order so a
// failure is reproducible.
func projectEnvFileContent() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(executionControlEnv)) {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(executionControlEnv[k])
		b.WriteString("\n")
	}
	return b.String()
}

// loadExecutionControlProjectEnv puts the test in a project directory whose .env
// carries every name in executionControlEnv and runs the real loader over it.
func loadExecutionControlProjectEnv(t *testing.T) string {
	t.Helper()
	keys := make([]string, 0, len(executionControlEnv))
	for k := range executionControlEnv {
		keys = append(keys, k, strings.ToLower(k))
	}
	return projectEnvWithoutImports(t, projectEnvFileContent(), keys...)
}

// The reproduction. A cloned repository's .env asks for code of its own to be loaded
// into whatever the CLI runs next; the CLI's own process must not take any of it.
func TestAProjectEnvCannotInjectCodeIntoTheCLIsOwnProcess(t *testing.T) {
	note := loadExecutionControlProjectEnv(t)

	for key := range executionControlEnv {
		if got := os.Getenv(key); got != "" {
			t.Errorf("%s = %q, want a project file never to reach this process", key, got)
		}
	}
	// Only the names a developer plausibly set on purpose are explained. An agent's own
	// variables are the ordinary contents of a project file and still reach the agent,
	// so a line about each would bury the ones that matter.
	for _, want := range []string{"BLOCKS_INSTALL_DIR", "./.env", "export"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
	for _, quiet := range []string{"AGENT_MODEL", "OPENWEATHER_UNITS", "MY_AGENT_WEBHOOK_TOKEN"} {
		if strings.Contains(note, quiet) {
			t.Errorf("note %q explains %q, which is ordinary project configuration", note, quiet)
		}
	}
}

// The other half, and the reason the first half is safe to do at all: `blocks run`
// exists to start this repository's own handler, so every variable the project file
// supplies has to arrive in that process — including the ones withheld above, which buy
// an attacker nothing in a process already running the repository's code, and whose
// legitimate uses (a src layout on PYTHONPATH, a heap size in NODE_OPTIONS) are exactly
// what a project file is for.
//
// It drives the real buildChildEnv rather than a stand-in: a re-implementation of the
// merge would pass whether or not the loader kept the values at all.
func TestAProjectEnvStillReachesTheDelegatedAgent(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)
	loadExecutionControlProjectEnv(t)
	resolveCLIContext(t, rootCmd)

	env := buildChildEnv()

	for key, want := range executionControlEnv {
		got, ok := childEnvValue(env, key)
		if !ok {
			t.Errorf("%s never reached the agent; a project file is how an agent is configured", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q in the child env, want %q", key, got, want)
		}
	}
}

// A value the shell exported is the operator's own choice and outranks the file in both
// processes. The loader never records an assignment for a variable the environment
// already carries, so the merge into the child cannot resurrect one either.
func TestAShellExportedValueOutranksTheProjectEnvInBothProcesses(t *testing.T) {
	restoreCLIState(t)
	isolateCredentials(t)
	defer isolateProfiles(t)()
	isolateAmbientState(t)

	const exported = "--max-old-space-size=4096"
	t.Setenv("NODE_OPTIONS", exported)
	t.Setenv("PYTHONPATH", "/opt/acme/lib")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("NODE_OPTIONS=--require ./from-the-repository.js\nPYTHONPATH=./from-the-repository\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	// Cloned, not aliased: the loader clears these maps in place, so keeping the
	// reference would restore the emptied original.
	priorKeys, priorValues := maps.Clone(envFileKeys), maps.Clone(projectEnvValues)
	t.Cleanup(func() { envFileKeys, projectEnvValues = priorKeys, priorValues })

	note := captureLoadEnvFile(t, ".env")

	if got := os.Getenv("NODE_OPTIONS"); got != exported {
		t.Errorf("NODE_OPTIONS = %q, want the exported %q left untouched", got, exported)
	}
	// Nothing was withheld that would otherwise have applied — the shell's value was
	// already in effect — so there is nothing to explain.
	if note != "" {
		t.Errorf("note = %q, want silence when the shell already supplied the value", note)
	}

	resolveCLIContext(t, rootCmd)
	env := buildChildEnv()
	for key, want := range map[string]string{"NODE_OPTIONS": exported, "PYTHONPATH": "/opt/acme/lib"} {
		if got, _ := childEnvValue(env, key); got != want {
			t.Errorf("%s = %q in the child env, want the exported %q", key, got, want)
		}
	}
}

// The allowlist has to be complete, and completeness is the property a reviewer cannot
// check by reading one file: the names arrive from cmd/helpers.go, internal/cdm and
// internal/profiles, and a new one added there would silently stop working from a
// project .env.
//
// So the source tree itself is the input. Every BLOCKS_* name this CLI spells is either
// accepted from a project file or listed below with the reason it is not, which turns
// "someone added a variable and forgot" into a failing test rather than a defect a user
// discovers.
func TestEveryBlocksEnvironmentVariableIsClassified(t *testing.T) {
	// Deliberately not accepted from a project file.
	notFromProjectFiles := map[string]string{
		// Written into the delegated agent's environment by the CLI, never read from
		// the CLI's own; withCLIVersion overwrites whatever is there.
		"BLOCKS_CLI_VERSION": "the CLI reports its own version to the agent",
		// Chooses where `blocks upgrade` writes a freshly downloaded binary.
		"BLOCKS_INSTALL_DIR": "a repository may not choose where this binary is replaced",
		// Injected at link time (-X cmd.defaultInstanceDomain); never read from the
		// environment at runtime.
		"BLOCKS_INSTANCE_DOMAIN": "build-time only",
	}

	found := map[string][]string{}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	literal := regexp.MustCompile(`"(BLOCKS_[A-Z0-9_]+)"`)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range literal.FindAllStringSubmatch(string(data), -1) {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			found[m[1]] = append(found[m[1]], rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(found) == 0 {
		t.Fatal("found no BLOCKS_* names at all; the scan is not looking at the source tree")
	}

	for name, files := range found {
		if cliProjectEnvKeys[name] {
			continue
		}
		if _, ok := notFromProjectFiles[name]; ok {
			continue
		}
		t.Errorf("%s (spelled in %s) is neither accepted from a project file nor listed as deliberately withheld — decide which, in cliProjectEnvKeys or in this test",
			name, strings.Join(files, ", "))
	}
	// And the reverse, so a variable that stops existing does not leave a stale entry
	// vouching for a name nothing reads.
	for name := range cliProjectEnvKeys {
		if _, ok := found[name]; !ok {
			t.Errorf("%s is accepted from a project file but no longer spelled anywhere in the CLI", name)
		}
	}
}
