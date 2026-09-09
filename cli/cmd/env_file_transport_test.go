package cmd

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cases drive the real loader over a real file, because what is under test is
// which assignments a project file is allowed to make. Populating the environment or
// the provenance record by hand would prove only that a predicate matches a string,
// while the defect it closes is entirely a question of what the loader does with the
// line it just read.
//
// Two properties are held apart on purpose, because a rule that satisfies one of them
// and not the other is easy to write and wrong: a project file must not be able to
// point the CLI at another proxy or certificate authority, and an ordinary
// application variable must still arrive, since `blocks run` builds the delegated
// agent's environment from this process's own.

// projectEnvWithoutImports puts the test in a fresh project directory holding the
// given .env, clears the variables under test so the file is the only thing that can
// set them, and returns what the load wrote to stderr.
//
// The clearing matters: the loader deliberately skips a variable the environment
// already carries, so a developer's own exported proxy would otherwise decide the
// outcome. Both halves of the record — the provenance map and the values behind it —
// are restored afterwards because the load replaces them, and leaving one case's
// record behind would make the next case's notes name a file it never read and its
// child environment carry a variable it never set.
func projectEnvWithoutImports(t *testing.T, content string, keys ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	priorKeys, priorValues := maps.Clone(envFileKeys), maps.Clone(projectEnvValues)
	t.Cleanup(func() { envFileKeys, projectEnvValues = priorKeys, priorValues })
	return captureLoadEnvFile(t, ".env")
}

// captureLoadEnvFile runs the real loader with stderr redirected, so a case can
// assert on the note a refusal prints. The note has to reach stderr rather than
// stdout: the load runs for every command, including the ones whose stdout is a
// machine-readable document.
func captureLoadEnvFile(t *testing.T, path string) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	loadEnvFile(path)
	os.Stderr = orig
	w.Close()
	note, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	r.Close()
	return string(note)
}

// The reproduction: a cloned repository's .env carries a proxy and a certificate
// authority of the attacker's choosing. Go honours both, so importing them would
// route every HTTPS request the CLI makes through a host that can read it — API
// keys, OAuth codes, invitation tokens, hosting-provider credentials — while every
// certificate still validates against the bundle committed beside the file.
func TestAProjectEnvCannotRedirectTransportOrTLSTrust(t *testing.T) {
	note := projectEnvWithoutImports(t,
		"HTTPS_PROXY=http://intercept.example.test:8080\nSSL_CERT_FILE=./ca-bundle.pem\n",
		"HTTPS_PROXY", "SSL_CERT_FILE")

	for _, k := range []string{"HTTPS_PROXY", "SSL_CERT_FILE"} {
		if got := os.Getenv(k); got != "" {
			t.Errorf("%s = %q, want it never imported from a project .env", k, got)
		}
	}
	// The refusal has to be legible: a developer who put a proxy in .env on purpose
	// needs to know which variables stopped working, where the CLI read them, and
	// what to do instead.
	for _, want := range []string{"HTTPS_PROXY", "SSL_CERT_FILE", "./.env", "export"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not mention %q", note, want)
		}
	}
}

// Go's proxy lookup accepts either spelling, so a rule that only refuses the
// uppercase one refuses nothing.
func TestAProjectEnvCannotRedirectTransportInLowercase(t *testing.T) {
	note := projectEnvWithoutImports(t,
		"https_proxy=http://intercept.example.test:8080\n",
		"https_proxy", "HTTPS_PROXY")

	if got := os.Getenv("https_proxy"); got != "" {
		t.Errorf("https_proxy = %q, want it never imported from a project .env", got)
	}
	if !strings.Contains(note, "https_proxy") {
		t.Errorf("note %q does not name the variable it refused", note)
	}
}

// Every member of the list, in both spellings: the whole family has to be covered,
// because leaving one name in place leaves the interception path open. The home and
// XDG names are cleared for the duration like any other, which is also the only state
// in which they are importable at all — the loader skips whatever the environment
// already carries, and an ordinary session carries them.
func TestAProjectEnvCannotSetAnyTransportOrRuntimeVariable(t *testing.T) {
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "GODEBUG",
		"XDG_CONFIG_HOME", "HOME", "USERPROFILE",
		"http_proxy", "https_proxy", "all_proxy", "no_proxy",
		"ssl_cert_file", "ssl_cert_dir", "godebug",
		"xdg_config_home", "home", "userprofile",
	} {
		t.Run(key, func(t *testing.T) {
			note := projectEnvWithoutImports(t, key+"=from-the-project-file\n",
				key, strings.ToUpper(key), strings.ToLower(key))
			if got := os.Getenv(key); got != "" {
				t.Errorf("%s = %q, want it never imported from a project .env", key, got)
			}
			if !strings.Contains(note, key) {
				t.Errorf("note %q does not name the variable it refused", note)
			}
		})
	}
}

// A corporate proxy is real and is set in the shell, which is intent. Only the
// project file is untrusted, so an exported value must survive byte for byte and the
// file must not be able to replace it.
func TestAShellExportedProxySurvivesTheProjectEnv(t *testing.T) {
	const exported = "http://proxy.acme.example.com:3128"
	t.Setenv("HTTPS_PROXY", exported)
	t.Setenv("SSL_CERT_FILE", "/etc/ssl/certs/acme-ca.pem")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("HTTPS_PROXY=http://intercept.example.test:8080\nSSL_CERT_FILE=./ca-bundle.pem\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Chdir(dir)
	priorKeys, priorValues := maps.Clone(envFileKeys), maps.Clone(projectEnvValues)
	t.Cleanup(func() { envFileKeys, projectEnvValues = priorKeys, priorValues })

	note := captureLoadEnvFile(t, ".env")

	if got := os.Getenv("HTTPS_PROXY"); got != exported {
		t.Errorf("HTTPS_PROXY = %q, want the exported %q left untouched", got, exported)
	}
	if got := os.Getenv("SSL_CERT_FILE"); got != "/etc/ssl/certs/acme-ca.pem" {
		t.Errorf("SSL_CERT_FILE = %q, want the exported value left untouched", got)
	}
	// Nothing was refused that would otherwise have applied — the shell's value was
	// already in effect — so there is nothing to explain.
	if note != "" {
		t.Errorf("note = %q, want silence when the shell already supplied the value", note)
	}
}

// The counterpart guard: an agent's own configuration has to keep arriving from the
// project .env. It no longer arrives in *this* process — the CLI takes only what it
// reads — but it is recorded, and TestAProjectEnvStillReachesTheDelegatedAgent proves
// the record is what `blocks run` hands the agent. Silence matters as much as the
// record: an ordinary application variable is not something to explain.
func TestAProjectEnvStillRecordsApplicationVariablesForTheAgent(t *testing.T) {
	note := projectEnvWithoutImports(t,
		"MY_AGENT_TOKEN=tok_project\nOPENWEATHER_UNITS=metric\n",
		"MY_AGENT_TOKEN", "OPENWEATHER_UNITS")

	for k, want := range map[string]string{"MY_AGENT_TOKEN": "tok_project", "OPENWEATHER_UNITS": "metric"} {
		if got := os.Getenv(k); got != "" {
			t.Errorf("%s = %q, want a project file never to reach this process", k, got)
		}
		if src := envFileSource(k); src != "./.env" {
			t.Errorf("envFileSource(%s) = %q, want the file that supplied it recorded", k, src)
		}
		if got := projectEnvValueFor(k); got != want {
			t.Errorf("projectEnvValues[%s] = %q, want %q kept for the delegated agent", k, got, want)
		}
	}
	if note != "" {
		t.Errorf("note = %q, want silence for ordinary project configuration", note)
	}
}

// The CLI's own variables are the ordinary `blocks login --write-env` shape and must
// keep loading, provenance included: the pin declines depend on that record.
func TestAProjectEnvStillImportsTheCLIsOwnVariables(t *testing.T) {
	note := projectEnvWithoutImports(t,
		blocksAPIKeyEnv+"=bk_project\n"+blocksBackendURLEnv+"=https://acme.example.com\n",
		blocksAPIKeyEnv, blocksBackendURLEnv)

	for k, want := range map[string]string{
		blocksAPIKeyEnv:     "bk_project",
		blocksBackendURLEnv: "https://acme.example.com",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q imported from the project .env", k, got, want)
		}
		if src := envFileSource(k); src != "./.env" {
			t.Errorf("envFileSource(%s) = %q, want the file that supplied it recorded", k, src)
		}
	}
	if note != "" {
		t.Errorf("note = %q, want silence when nothing was refused", note)
	}
}
