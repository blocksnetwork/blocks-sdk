package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
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

// A terminal acts on control characters rather than displaying them, so text the CLI
// did not author must arrive inert. An organization name carrying an erase-line or
// cursor-up sequence can overwrite the lines around it — here, the name of the
// organization a key is about to be minted in — and a server error body can do the
// same to the failure the user is trying to read. Both must come out escaped and
// visible instead.
func TestFetchOrCreateApiKey_RendersServerTextInertly(t *testing.T) {
	const nastyOrg = "Acme\r\x1b[2KEvil Corp"
	const nastyBody = "denied\r\x1b[1Athe key was created"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/orgs" {
			name, _ := json.Marshal(nastyOrg)
			w.Write([]byte(`{"orgs":[{"id":"org-1","name":` + string(name) + `}]}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(nastyBody))
	}))
	defer srv.Close()

	out, err := captureStdout(t, func() error {
		_, keyErr := FetchOrCreateApiKey(srv.URL, "session-token")
		return keyErr
	})
	if err == nil {
		t.Fatal("expected the key creation to fail")
	}

	assertInert(t, "the printed organization name", out)
	assertInert(t, "the quoted server message", err.Error())
	if !strings.Contains(out, `Acme\x0d\x1b[2KEvil Corp`) {
		t.Errorf("the organization name must be printed escaped, got:\n%q", out)
	}
	if !strings.Contains(err.Error(), `denied\x0d\x1b[1Athe key was created`) {
		t.Errorf("the server message must be quoted escaped, got:\n%q", err.Error())
	}
}

// The browser login records the expiry of the key it mints, and it must not record an
// unreadable timestamp as the zero time: the zero time is how "the deployment stated no
// expiry" is stored, so it would leave `blocks login` caching a credential the CLI
// trusts forever and re-mints never — every later command failing to authenticate with
// nothing local able to explain it.
func TestFetchOrCreateApiKey_TreatsAnUnreadableExpiryAsExpired(t *testing.T) {
	for _, expiresAt := range []string{"not-a-date", "2026-09-05 12:00:00", "1789000000"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/orgs" {
				w.Write([]byte(`{"orgs":[{"id":"org-1","name":"Acme"}]}`))
				return
			}
			w.Write([]byte(`{"apiKey":"bk_live","keyId":"k1","expiresAt":"` + expiresAt + `"}`))
		}))

		var creds *Credentials
		out, err := captureStdout(t, func() error {
			var keyErr error
			creds, keyErr = FetchOrCreateApiKey(srv.URL, "session-token")
			return keyErr
		})
		srv.Close()
		if err != nil {
			t.Fatalf("FetchOrCreateApiKey (%s): %v\n%s", expiresAt, err, out)
		}
		if creds.ExpiresAt.IsZero() {
			t.Errorf("expiresAt %q was recorded as 'no expiry', which is read as never expiring", expiresAt)
		}
		if !creds.ExpiresAt.Before(time.Now()) {
			t.Errorf("expiresAt %q was recorded as %v, want an expiry already past so a replacement is minted", expiresAt, creds.ExpiresAt)
		}
	}
}

// The org fetch quotes its own response body, and it is the earlier of the two
// failures a user can hit here, so it owes the same guarantee.
func TestFetchOrgs_RendersAServerMessageInertly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("no orgs\r\x1b[2Kall good"))
	}))
	defer srv.Close()

	_, err := fetchOrgs(srv.URL, "session-token")
	if err == nil {
		t.Fatal("expected the org fetch to fail")
	}
	assertInert(t, "the quoted server message", err.Error())
	if !strings.Contains(err.Error(), `no orgs\x0d\x1b[2Kall good`) {
		t.Errorf("the server message must be quoted escaped, got:\n%q", err.Error())
	}
}

// The org list is worse than a single name: the numbers beside the names are the whole
// basis of the choice, so a name that can redraw the rows above it means the user
// selects an organization other than the one they read.
func TestPromptOrgSelection_RendersEveryNameInertly(t *testing.T) {
	restoreStdin := replaceStdin(t, "1\n")
	defer restoreStdin()

	orgs := []orgMembership{
		{OrgId: "org-1", OrgName: "Acme"},
		{OrgId: "org-2\x07", OrgName: "Other\x1b[1A\x1b[2K    [1] Acme"},
	}
	out, err := captureStdout(t, func() error {
		_, selErr := promptOrgSelection(orgs)
		return selErr
	})
	if err != nil {
		t.Fatalf("promptOrgSelection: %v", err)
	}
	assertInert(t, "the printed org list", out)
}

// replaceStdin points os.Stdin at a pipe carrying input and returns a function that
// puts the real one back. promptOrgSelection reads its own scanner from os.Stdin, so
// this is what lets the prompt be driven without a terminal.
func replaceStdin(t *testing.T, input string) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	return func() {
		os.Stdin = orig
		r.Close()
	}
}

// captureStdout collects what fn printed, so a test can assert on the bytes that would
// have reached the terminal rather than on the value that was passed in.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	callErr := fn()
	os.Stdout = orig
	w.Close()
	data, readErr := io.ReadAll(r)
	r.Close()
	if readErr != nil {
		t.Fatalf("read captured output: %v", readErr)
	}
	return string(data), callErr
}

// assertInert fails when s carries a control character a terminal would act on. The
// CLI's own line breaks are excluded: they are literals in the format strings, not
// bytes anything external supplied.
func assertInert(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			t.Errorf("%s reached the terminal carrying %#U: %q", what, r, s)
			return
		}
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

// A caller recording a credential and the deployment it belongs to has one fact to
// write, and two separate writes can fail between them — leaving the new key beside
// the previous deployment's URL. One rewrite applies both or neither.
func TestApplyEnvAt_AppliesAssignmentsAndRemovalsInOneRewrite(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "# comment\nOTHER=keep\nBLOCKS_BACKEND_URL=https://stale.example\nBLOCKS_API_KEY=old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Remove: true},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "new-key"},
	)
	if err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)
	if strings.Contains(content, "BLOCKS_BACKEND_URL") {
		t.Errorf("the removal did not apply:\n%s", content)
	}
	if !strings.Contains(content, "BLOCKS_API_KEY=new-key") {
		t.Errorf("the assignment did not apply:\n%s", content)
	}
	for _, want := range []string{"# comment", "OTHER=keep"} {
		if !strings.Contains(content, want) {
			t.Errorf("rewrite lost %q:\n%s", want, content)
		}
	}
}

// A failed rewrite must be reported and must leave the file exactly as it was: a
// half-applied .env is the state that pairs a fresh key with stale targeting, and a
// dropped error is what lets a caller announce a change that never happened.
//
// The failure is provoked by a directory the replacement file cannot be created in,
// which is the only way this rewrite can fail before the target is touched at all —
// and that is the point: the target's bytes are never the thing being written.
func TestApplyEnvAt_FailedRewriteReportsAndChangesNothing(t *testing.T) {
	initial := "BLOCKS_BACKEND_URL=https://stale.example\n"
	tmpDir, envFile := unwritableProjectDir(t, initial)

	err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Remove: true},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "new-key"},
	)
	if err == nil {
		t.Fatal("a rewrite that could not be written must be reported")
	}

	data, readErr := os.ReadFile(envFile)
	if readErr != nil {
		t.Fatalf("read .env: %v", readErr)
	}
	if string(data) != initial {
		t.Errorf(".env changed despite the failure:\n%s", string(data))
	}
	assertNoTempFiles(t, tmpDir)
}

// The rewrite replaces the file instead of emptying and refilling it. The witness is
// a second name for the original file: a truncating write would show the new content
// through it, because it edits the bytes both names share. Seeing the original there
// is what proves no reader could ever observe a half-written .env — the replacement
// is assembled elsewhere and swapped in whole.
func TestApplyEnvAt_ReplacesTheFileRatherThanTruncatingIt(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "BLOCKS_BACKEND_URL=https://stale.example\nBLOCKS_API_KEY=old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(tmpDir, "witness")
	if err := os.Link(envFile, witness); err != nil {
		t.Skipf("filesystem does not support a second name for the file: %v", err)
	}

	if err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://blocks.acme.example"},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"},
	); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	before, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	if string(before) != initial {
		t.Errorf("the original file was written in place, so a reader can see a partial .env:\n%s", string(before))
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read .env: %v", err)
	}
	for _, want := range []string{"BLOCKS_BACKEND_URL=https://blocks.acme.example", "BLOCKS_API_KEY=bk_new"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rewrite did not apply %q:\n%s", want, string(data))
		}
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %v, want the file's own 0600 to survive the replacement", perm)
	}
	assertNoTempFiles(t, tmpDir)
}

// A key assigned twice is the wrong-target trap: this CLI's loader reads the first
// value while the dotenv loaders in the scaffolded Node and Python agents read the
// last, so a stale duplicate left behind sends a directly launched agent to the
// deployment the user just moved off while every CLI command uses the new one — with
// no error anywhere. A rewrite must leave the key assigned exactly once.
func TestApplyEnvAt_LeavesOneAssignmentWhenTheKeyIsDuplicated(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "BLOCKS_BACKEND_URL=https://stale.example\nOTHER=keep\n" +
		"BLOCKS_BACKEND_URL=https://also-stale.example\n# BLOCKS_BACKEND_URL=commented\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(tmpDir, EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://blocks.acme.example"}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	got := envAssignments(t, envFile, "BLOCKS_BACKEND_URL")
	if len(got) != 1 || got[0] != "https://blocks.acme.example" {
		t.Errorf("assignments = %q, want exactly one holding the new value", got)
	}
	data, _ := os.ReadFile(envFile)
	for _, want := range []string{"OTHER=keep", "# BLOCKS_BACKEND_URL=commented"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("collapsing duplicates lost %q:\n%s", want, string(data))
		}
	}
}

// UpsertEnvKey writes through the same line edits, so it owes the same invariant.
func TestUpsertEnvKey_LeavesOneAssignmentWhenTheKeyIsDuplicated(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=old\nBLOCKS_API_KEY=older\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "bk_new"); err != nil {
		t.Fatalf("UpsertEnvKey: %v", err)
	}

	got := envAssignments(t, envFile, "BLOCKS_API_KEY")
	if len(got) != 1 || got[0] != "bk_new" {
		t.Errorf("assignments = %q, want exactly one holding the new value", got)
	}
}

// envAssignments lists every uncommented value assigned to key, in file order.
func envAssignments(t *testing.T, path, key string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var values []string
	for _, line := range strings.Split(string(data), "\n") {
		if EnvLineMatchesKey(line, key) {
			values = append(values, strings.TrimPrefix(strings.TrimSpace(line), key+"="))
		}
	}
	return values
}

// unwritableProjectDir returns a directory holding a .env with the given content
// that no new file can be created in, and the path of that .env. It is how a rewrite
// is made to fail without making the .env itself unreadable.
func unwritableProjectDir(t *testing.T, content string) (dir, envFile string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root can write an unwritable directory, so the failure cannot be provoked")
	}
	dir = t.TempDir()
	envFile = filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil { // traversable and readable, not writable
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	return dir, envFile
}

// assertNoTempFiles fails when the replacement's temp file outlived the call. A
// rewrite that leaves one behind litters the user's project with copies of a file
// holding a credential.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env.tmp-") {
			t.Errorf("temp file %q survived the rewrite", e.Name())
		}
	}
}

// Nothing to remove is not a change: a removal alone must not create a .env, and a
// removal of a key that is not there must not rewrite the file.
func TestApplyEnvAt_RemovalOnlyLeavesAnAbsentOrUnchangedFile(t *testing.T) {
	missingDir := t.TempDir()
	if err := ApplyEnvAt(missingDir, EnvMutation{Key: "BLOCKS_BACKEND_URL", Remove: true}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(missingDir, ".env")); !os.IsNotExist(err) {
		t.Error("a removal must not create a .env")
	}

	presentDir := t.TempDir()
	envFile := filepath.Join(presentDir, ".env")
	initial := "OTHER=keep\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEnvAt(presentDir, EnvMutation{Key: "BLOCKS_BACKEND_URL", Remove: true}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}
	data, _ := os.ReadFile(envFile)
	if string(data) != initial {
		t.Errorf("a no-op removal rewrote the file:\n%s", string(data))
	}
}

// A new .env carries every assignment the call made, and only 0600: it holds a
// credential.
func TestApplyEnvAt_CreatesTheFileWithEveryAssignment(t *testing.T) {
	tmpDir := t.TempDir()
	if err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://blocks.acme.example"},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"},
	); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}
	envFile := filepath.Join(tmpDir, ".env")
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("expected .env to be created: %v", err)
	}
	want := "BLOCKS_BACKEND_URL=https://blocks.acme.example\nBLOCKS_API_KEY=bk_new\n"
	if string(data) != want {
		t.Errorf("content = %q, want %q", string(data), want)
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %v, want 0600 for a file holding a credential", perm)
	}
}

// A file this package has written a credential into is owner-only afterwards, whatever
// mode it had before. Keeping the existing mode was the previous rule and it hands the
// decision to a file whose mode nobody here chose: the file a .env symlink resolved to,
// which for anything a checkout committed is world-readable 0644. Every mode is walked,
// including one that has to be *widened* to 0600, because "never widen" was the rule
// that let the world-readable case through.
func TestApplyEnvAt_LeavesTheFileOwnerOnlyWhateverModeItHad(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644, 0664, 0666, 0400} {
		t.Run(mode.String(), func(t *testing.T) {
			tmpDir := t.TempDir()
			envFile := filepath.Join(tmpDir, ".env")
			if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=old\n"), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(envFile, mode); err != nil {
				t.Fatal(err)
			}
			if err := ApplyEnvAt(tmpDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"}); err != nil {
				t.Fatalf("ApplyEnvAt: %v", err)
			}
			if perm := statPerm(t, envFile); perm != 0600 {
				t.Errorf("mode = %v after a rewrite of a %v file, want 0600 for a file holding a credential", perm, mode)
			}
		})
	}
}

// linkedProjectEnv builds the shape a shared .env actually takes: a checkout whose
// packages all point at one credentials file kept above them. It returns the package
// directory a command would run in, the symlink standing in for that package's .env,
// the directory holding the real file, and the real file itself.
func linkedProjectEnv(t *testing.T, initial string) (projDir, link, sharedDir, sharedEnv string) {
	t.Helper()
	root := t.TempDir()
	// The marker fixes the project boundary for this test rather than leaving it to
	// whatever happens to sit above the temp dir.
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	sharedDir, projDir = filepath.Join(root, "shared"), filepath.Join(root, "packages", "api")
	for _, d := range []string{sharedDir, projDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sharedEnv = filepath.Join(sharedDir, ".env")
	if err := os.WriteFile(sharedEnv, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(projDir, ".env")
	if err := os.Symlink(sharedEnv, link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	return projDir, link, sharedDir, sharedEnv
}

// assertStillASymlink fails when a rewrite replaced the link with a regular file —
// the state in which the command reports success and the file the user actually reads
// still holds the old credential.
func assertStillASymlink(t *testing.T, link string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat %s: %v", link, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink: the rewrite renamed a regular file over the link, so the file it pointed at was never updated", link)
	}
}

// A .env that is a symlink must be written *through*: one shared credentials file, or
// one file a monorepo's packages all point at, is why a .env is a link in the first
// place. Renaming the replacement over the link instead destroys the link and leaves
// the real file holding the old key, while `--write-env` reports the new one written.
func TestApplyEnvAt_WritesThroughASymlinkedEnv(t *testing.T) {
	projDir, link, sharedDir, sharedEnv := linkedProjectEnv(t, "BLOCKS_BACKEND_URL=https://stale.example\nKEEP=me\n")

	if err := ApplyEnvAt(projDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://blocks.acme.example"},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"},
	); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	assertStillASymlink(t, link)
	data, err := os.ReadFile(sharedEnv)
	if err != nil {
		t.Fatalf("read shared .env: %v", err)
	}
	for _, want := range []string{"BLOCKS_BACKEND_URL=https://blocks.acme.example", "BLOCKS_API_KEY=bk_new", "KEEP=me"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file the link points at is missing %q:\n%s", want, string(data))
		}
	}
	if perm := statPerm(t, sharedEnv); perm != 0600 {
		t.Errorf("mode = %v, want the real file's own 0600 to survive", perm)
	}
	assertNoTempFiles(t, projDir)
	assertNoTempFiles(t, sharedDir)
}

// Removal owes the same guarantee, and owes it more loudly: `blocks logout` and
// `blocks profile rm` both announce the credential removed afterwards. A rename over
// the link leaves the key exactly where it was, in the file every later command reads.
func TestRemoveEnvKeys_WritesThroughASymlinkedEnv(t *testing.T) {
	projDir, link, sharedDir, sharedEnv := linkedProjectEnv(t, "BLOCKS_API_KEY=bk_old\nKEEP=me\n")

	removed, err := RemoveEnvKeys(link, "BLOCKS_API_KEY")
	if err != nil {
		t.Fatalf("RemoveEnvKeys: %v", err)
	}
	if len(removed) != 1 || removed[0] != "BLOCKS_API_KEY" {
		t.Fatalf("removed = %v, want the key it dropped", removed)
	}

	assertStillASymlink(t, link)
	data, err := os.ReadFile(sharedEnv)
	if err != nil {
		t.Fatalf("read shared .env: %v", err)
	}
	if strings.Contains(string(data), "bk_old") {
		t.Errorf("the credential is still in the file the link points at:\n%s", string(data))
	}
	if !strings.Contains(string(data), "KEEP=me") {
		t.Errorf("removal through the link lost the rest of the file:\n%s", string(data))
	}
	assertNoTempFiles(t, projDir)
	assertNoTempFiles(t, sharedDir)
}

// A link out of the project is refused. A checkout can ship one, so following it would
// let a cloned repository choose where `blocks login --write-env` puts a credential —
// ~/.env, a dotfiles directory, some unrelated file in the user's home. The refusal
// must name the file it declined to write and change nothing at either end.
func TestApplyEnvAt_RefusesASymlinkOutOfTheProject(t *testing.T) {
	root := t.TempDir()
	projDir, outsideDir := filepath.Join(root, "project"), filepath.Join(root, "elsewhere")
	for _, d := range []string{projDir, outsideDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// The project boundary is the checkout, so it is marked here and not at root.
	if err := os.Mkdir(filepath.Join(projDir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	initial := "BLOCKS_API_KEY=bk_old\n"
	outsideEnv := filepath.Join(outsideDir, ".env")
	if err := os.WriteFile(outsideEnv, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projDir, ".env")
	if err := os.Symlink(outsideEnv, link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}

	err := ApplyEnvAt(projDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"})
	if err == nil {
		t.Fatal("a .env linking out of the project must be refused, not followed silently")
	}
	if !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("refusal does not say why: %v", err)
	}

	data, readErr := os.ReadFile(outsideEnv)
	if readErr != nil {
		t.Fatalf("read the outside file: %v", readErr)
	}
	if string(data) != initial {
		t.Errorf("the refusal wrote to the file outside the project anyway:\n%s", string(data))
	}
	assertStillASymlink(t, link)
	assertNoTempFiles(t, projDir)
	assertNoTempFiles(t, outsideDir)
}

// A link resolving to nothing is refused rather than treated as an absent .env,
// because treating it as absent is precisely the rename that replaces the link with a
// regular file and abandons the path the user set up.
func TestApplyEnvAt_RefusesASymlinkThatResolvesToNothing(t *testing.T) {
	projDir := t.TempDir()
	link := filepath.Join(projDir, ".env")
	if err := os.Symlink(filepath.Join(projDir, "not-there.env"), link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}

	err := ApplyEnvAt(projDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"})
	if err == nil {
		t.Fatal("a .env symlink with no target must be refused, not replaced")
	}
	assertStillASymlink(t, link)
	assertNoTempFiles(t, projDir)
}

// statPerm is the permission bits of the file at path, following any link.
func statPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// RemoveEnvKey's failure has to reach its caller: `blocks logout` and
// `blocks profile rm` both announce the removal afterwards, and a file that still
// carries the line keeps authenticating (or redirecting) every later command.
func TestRemoveEnvKey_ReportsAFailedRewrite(t *testing.T) {
	_, envFile := unwritableProjectDir(t, "BLOCKS_API_KEY=old\n")

	if err := RemoveEnvKey(envFile, "BLOCKS_API_KEY"); err == nil {
		t.Fatal("a removal that could not be written must be reported")
	}
	if err := RemoveEnvKey(envFile, "NOT_PRESENT"); err != nil {
		t.Errorf("removing a key that is not there is not a failure: %v", err)
	}
}

// The two ways a read can come back empty must not be confused. A project with no
// .env has genuinely nothing to remove, and saying so is right. A .env that exists
// and cannot be read is a file whose contents nobody knows: read as "nothing to
// remove", it lets `blocks logout` report success while the credential is still on
// disk, and `blocks profile remove` report completion while the deployment targeting
// is still there.
func TestRemoveEnvKeys_DistinguishesAnAbsentFileFromAnUnreadableOne(t *testing.T) {
	removed, err := RemoveEnvKeys(filepath.Join(t.TempDir(), ".env"), "BLOCKS_API_KEY")
	if err != nil {
		t.Errorf("no .env is nothing to remove, not a failure: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want nothing from a file that is not there", removed)
	}

	if _, err := RemoveEnvKeys(unreadableEnvFile(t), "BLOCKS_API_KEY"); err == nil {
		t.Error("a .env that exists and cannot be read must be reported, not read as an empty file")
	}
}

// RemoveEnvKey is the single-key spelling of the same call and owes the same
// distinction: it is what `blocks logout` removes the credential with.
func TestRemoveEnvKey_ReportsAnUnreadableFile(t *testing.T) {
	if err := RemoveEnvKey(unreadableEnvFile(t), "BLOCKS_API_KEY"); err == nil {
		t.Error("a .env that exists and cannot be read must be reported")
	}
	if err := RemoveEnvKey(filepath.Join(t.TempDir(), ".env"), "BLOCKS_API_KEY"); err != nil {
		t.Errorf("no .env is nothing to remove, not a failure: %v", err)
	}
}

// EnvFileValue is how a caller decides whether an assignment belongs to the
// deployment it is about to forget, so an unreadable file must not answer "no
// assignment": that answer means "leave it alone and say nothing", which is exactly
// wrong for a file that may well carry the line.
func TestEnvFileValue_DistinguishesAnAbsentFileFromAnUnreadableOne(t *testing.T) {
	value, err := EnvFileValue(filepath.Join(t.TempDir(), ".env"), "BLOCKS_BACKEND_URL")
	if err != nil || value != "" {
		t.Errorf("EnvFileValue on an absent .env = (%q, %v), want (\"\", nil)", value, err)
	}

	if _, err := EnvFileValue(unreadableEnvFile(t), "BLOCKS_BACKEND_URL"); err == nil {
		t.Error("a .env that exists and cannot be read must be reported, not read as an empty file")
	}
}

// unreadableEnvFile returns the path of a .env that exists and cannot be read, by
// making it a directory: os.ReadFile then fails on the read itself, for every user
// including root, so the case is reproducible without depending on permission bits
// the platform or the test runner may not enforce.
func unreadableEnvFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("create unreadable .env: %v", err)
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("this platform reads a directory as a file, so an unreadable .env cannot be staged this way")
	}
	return path
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

// A .env value is text a deployment supplied — the API key it minted is the whole
// reason this writer exists — and the writer joins it to its variable name with an
// "=". A value carrying a line break therefore writes a second line, and this CLI's
// own loader imports that line on the next invocation as a variable in its own
// right: a deployment that returns "bk_live\nHTTPS_PROXY=..." would be setting the
// proxy for every later command run in that directory, and could read the traffic of
// commands that have nothing to do with it.
//
// Such a value is refused, and refused before anything is written, rather than
// stripped: a credential silently cut down to its first line looks stored and is not,
// and a user who is told nothing has no reason to look for what went wrong.
func TestApplyEnvAt_RefusesAValueThatWouldWriteASecondAssignment(t *testing.T) {
	const injected = "HTTPS_PROXY=http://attacker.example.test"
	for _, tc := range []struct{ name, value string }{
		{"newline", "bk_live\n" + injected},
		// The loader normalises a lone CR to a newline before splitting, so a CR is
		// the same second line by another spelling.
		{"carriage return", "bk_live\r" + injected},
		{"NUL", "bk_live\x00" + injected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			envFile := filepath.Join(tmpDir, ".env")
			initial := "BLOCKS_API_KEY=old\n"
			if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
				t.Fatal(err)
			}

			err := ApplyEnvAt(tmpDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: tc.value})
			if err == nil {
				t.Fatal("a value that would write a second assignment must be refused")
			}
			if !strings.Contains(err.Error(), "BLOCKS_API_KEY") {
				t.Errorf("the refusal must name the variable it refused: %v", err)
			}
			if strings.Contains(err.Error(), "bk_live") {
				t.Errorf("the refusal must not echo the credential: %v", err)
			}
			data, readErr := os.ReadFile(envFile)
			if readErr != nil {
				t.Fatalf("read .env: %v", readErr)
			}
			if string(data) != initial {
				t.Errorf(".env changed despite the refusal:\n%s", string(data))
			}
			assertNoTempFiles(t, tmpDir)
		})
	}
}

// The end the refusal exists for: read back the way the CLI's own loader reads a
// .env, the file must carry no variable the value smuggled in — neither in the
// existing-file rewrite nor in the create-a-new-file path, which is a separate write
// and so a separate way to bypass the check.
func TestApplyEnvAt_ANewlineBearingValueCannotBecomeASecondAssignment(t *testing.T) {
	poisoned := "bk_live\nHTTPS_PROXY=http://attacker.example.test"

	existingDir := t.TempDir()
	envFile := filepath.Join(existingDir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEnvAt(existingDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: poisoned}); err == nil {
		t.Fatal("a value that would write a second assignment must be refused")
	}
	data, _ := os.ReadFile(envFile)
	if got := loaderAssignments(string(data)); got["HTTPS_PROXY"] != "" {
		t.Errorf("the loader would import HTTPS_PROXY=%q from the rewritten .env:\n%s", got["HTTPS_PROXY"], string(data))
	}

	newDir := t.TempDir()
	if err := ApplyEnvAt(newDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: poisoned}); err == nil {
		t.Fatal("a refused value must not be written into a file this call creates either")
	}
	if _, err := os.Stat(filepath.Join(newDir, ".env")); !os.IsNotExist(err) {
		t.Errorf("a refused write created a .env: %v", err)
	}
}

// The refusal covers the whole rewrite, not the offending mutation: ApplyEnvAt exists
// so a credential and the targeting it belongs to land together, and writing the safe
// half of a batch would leave exactly the mismatched pair it batches to prevent.
func TestApplyEnvAt_RefusesTheWholeRewriteWhenOneValueIsUnsafe(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "BLOCKS_BACKEND_URL=https://old.acme.example\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://new.acme.example"},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_live\nHTTPS_PROXY=http://attacker.example.test"},
	)
	if err == nil {
		t.Fatal("a batch containing an unsafe value must be refused")
	}
	data, _ := os.ReadFile(envFile)
	if string(data) != initial {
		t.Errorf("part of the batch was written despite the refusal:\n%s", string(data))
	}
}

// A variable name is written verbatim too, so a name carrying an "=", whitespace or a
// line break produces a line the loader reads as some other variable — or as two.
// Names are held to the shape an environment variable actually has.
func TestApplyEnvAt_RefusesAKeyThatIsNotAVariableName(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "OTHER=keep\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"", "BLOCKS API_KEY", "BLOCKS=KEY", "BLOCKS_API_KEY\nHTTPS_PROXY", "# BLOCKS_API_KEY"} {
		err := ApplyEnvAt(tmpDir, EnvMutation{Key: key, Value: "bk_live"})
		if err == nil {
			t.Errorf("key %q must be refused", key)
		}
		data, _ := os.ReadFile(envFile)
		if string(data) != initial {
			t.Errorf(".env changed despite refusing key %q:\n%s", key, string(data))
		}
	}
}

// Removal takes the same names as assignment, so it is held to the same shape: a name
// no assignment could have written is a name no removal can be looking for.
func TestRemoveEnvKeys_RefusesAKeyThatIsNotAVariableName(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("OTHER=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveEnvKeys(envFile, "BLOCKS=KEY"); err == nil {
		t.Fatal("a name that is not a variable name must be refused")
	}
}

// UpsertEnvKey is a second door into the same rewrite, and an unsafe value must not
// get through it either.
func TestUpsertEnvKey_RefusesAValueThatWouldWriteASecondAssignment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	initial := "BLOCKS_API_KEY=old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	err := UpsertEnvKey(envFile, "BLOCKS_API_KEY", "bk_live\nHTTPS_PROXY=http://attacker.example.test")
	if err == nil {
		t.Fatal("a value that would write a second assignment must be refused")
	}
	if !strings.Contains(err.Error(), "BLOCKS_API_KEY") {
		t.Errorf("the refusal must name the variable it refused: %v", err)
	}
	data, _ := os.ReadFile(envFile)
	if string(data) != initial {
		t.Errorf(".env changed despite the refusal:\n%s", string(data))
	}
}

// loaderEnvKey reports the variable a .env line assigns, the way this CLI's own
// loader decides it: see loadEnvFile in cmd/root.go — the line is trimmed, blank and
// #-prefixed lines are skipped, and the text before the first "=" is trimmed and used
// as the name. It is transcribed here rather than imported because internal/auth must
// not depend on the command package; TestEnvLineMatchesKey_AgreesWithTheLoader is what
// keeps the transcription and the writer's own matcher from drifting apart.
func loaderEnvKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", false
	}
	k, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(k), true
}

// envFileAssignments is what a loader would end up holding for a file's content: CRLF
// and lone CR normalised to newlines, blank and #-prefixed lines skipped, name and value
// trimmed, and one pair of matching outer quotes taken off the value — the shape a shell,
// Node's dotenv and Python's python-dotenv all agree on.
//
// firstWins is the one place the loaders differ, and it is why both are transcribed here
// rather than one being derived from the other: this CLI's loader takes the first
// non-empty assignment of a name because it only sets a variable the environment does not
// already carry, while a dotenv loader takes the last. Every value written by this package
// has to survive both.
//
// It is transcribed rather than calling EnvLineAssignment on purpose. These helpers are
// what the tests check the writer against, so deriving them from the code under test would
// make the check state only that the writer agrees with itself.
func envFileAssignments(content string, firstWins bool) map[string]string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		key, ok := loaderEnvKey(line)
		if !ok {
			continue
		}
		_, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if firstWins && (value == "" || out[key] != "") {
			continue
		}
		out[key] = value
	}
	return out
}

// loaderAssignments is what this CLI's own loader would hold for a file's content.
func loaderAssignments(content string) map[string]string {
	return envFileAssignments(content, true)
}

// dotenvAssignments is what the dotenv loader inside a directly-launched Node or Python
// agent would hold for the same content: the same parse, with the last assignment of a
// name winning instead of the first.
func dotenvAssignments(content string) map[string]string {
	return envFileAssignments(content, false)
}

// loaderCanonicalEnvKey is transcribed from canonicalEnvKey in cmd/root.go: the loader
// side folds a variable name to uppercase before recording or looking it up, because a
// Windows process environment is case-insensitive and two spellings of a name are one
// variable there.
func loaderCanonicalEnvKey(key string) string { return strings.ToUpper(key) }

// loaderReadsLineAsKey reports whether the loader side would read line as an assignment
// of key. Two rules decide it, and they are separate: the parse says which variable the
// line names, and the canonical fold says which names are one variable — the latter only
// within the CLI's own namespace, which is where cmd/root.go applies it and the only
// place the writer claims it.
func loaderReadsLineAsKey(line, key string) bool {
	name, ok := loaderEnvKey(line)
	if !ok {
		return false
	}
	if name == key {
		return true
	}
	canonical := loaderCanonicalEnvKey(key)
	return strings.HasPrefix(canonical, "BLOCKS_") && loaderCanonicalEnvKey(name) == canonical
}

// envKeySpellings is a variable name and the case variants of it a .env might use.
func envKeySpellings(key string) []string {
	lower := strings.ToLower(key)
	mixed := []rune(lower)
	for i := range mixed {
		if i%2 == 0 {
			mixed[i] = unicode.ToUpper(mixed[i])
		}
	}
	return []string{key, lower, string(mixed)}
}

// The writer's idea of "a line assigning KEY" has to be the loader's idea of it,
// because every guarantee the writer makes is a guarantee about what the loader will
// read back. Where the two disagree the writer edits one line and the loader reads
// another, which is how a spaced assignment survived a rewrite meant to replace it.
//
// This table is the invariant itself, not an example of it: for every shape a .env
// line can take, and every case a name can be spelled in, EnvLineMatchesKey must say
// exactly what the loader's parse and the loader's canonical fold say between them.
//
// Both keys walk the same shapes and the same spellings because the boundary between
// them is as much a part of the rule as the parse is: BLOCKS_API_KEY is a variable this
// CLI owns, so every spelling of it names it, while KEY is not, so only its own spelling
// does.
func TestEnvLineMatchesKey_AgreesWithTheLoader(t *testing.T) {
	shapes := []string{
		"%s=v",
		"%s = v",
		"  %s=v",
		"\t%s\t=\t v",
		"%s=",
		"%s= ",
		"%s",
		"#%s=v",
		"# %s=v",
		"  # %s=v",
		"%sX=v",
		"%s_X=v",
		"X%s=v",
		"OTHER=%s=v",
	}
	for _, key := range []string{"KEY", "BLOCKS_API_KEY"} {
		lines := []string{"", "   "}
		for _, shape := range shapes {
			for _, spelling := range envKeySpellings(key) {
				lines = append(lines, fmt.Sprintf(shape, spelling))
			}
		}
		for _, line := range lines {
			want := loaderReadsLineAsKey(line, key)
			if got := EnvLineMatchesKey(line, key); got != want {
				loaderKey, isAssignment := loaderEnvKey(line)
				t.Errorf("key %q, line %q: writer says matches=%v, want %v — the loader reads the line as %q (assignment=%v)",
					key, line, got, want, loaderKey, isAssignment)
			}
		}
	}
}

// A spaced assignment is an assignment: the loader trims the name before comparing it,
// so "BLOCKS_API_KEY = old" sets BLOCKS_API_KEY. A rewrite that did not recognise the
// shape appended a second assignment beside it and left the duplicate-collapse
// invariant broken — and worse, left the file able to pair a fresh key with the
// previous deployment's targeting, since the loaders that read these files disagree
// about which duplicate wins.
func TestApplyEnvAt_CollapsesASpacedAssignmentTheLoaderWouldRead(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "  BLOCKS_API_KEY = old\nOTHER=keep\nBLOCKS_API_KEY=stale-duplicate\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(tmpDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)
	assignments := 0
	for _, line := range strings.Split(content, "\n") {
		if key, ok := loaderEnvKey(line); ok && key == "BLOCKS_API_KEY" {
			assignments++
		}
	}
	if assignments != 1 {
		t.Errorf("expected exactly one BLOCKS_API_KEY assignment, found %d:\n%s", assignments, content)
	}
	if got := loaderAssignments(content)["BLOCKS_API_KEY"]; got != "bk_new" {
		t.Errorf("the loader would read BLOCKS_API_KEY=%q, want the value just written:\n%s", got, content)
	}
	if !strings.Contains(content, "OTHER=keep") {
		t.Errorf("the rewrite lost an unrelated line:\n%s", content)
	}
}

// `blocks logout` and `blocks profile remove` remove assignments through this path, and
// a credential or a deployment pin the loader still reads is one that was not removed,
// however the line was spelled.
func TestRemoveEnvKeys_DropsASpacedAssignment(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	initial := "  BLOCKS_API_KEY = old\nBLOCKS_BACKEND_URL\t=\thttps://acme.example.com\nOTHER=keep\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveEnvKeys(envFile, "BLOCKS_API_KEY", "BLOCKS_BACKEND_URL")
	if err != nil {
		t.Fatalf("RemoveEnvKeys: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("expected both spaced assignments reported as removed, got %v", removed)
	}
	data, _ := os.ReadFile(envFile)
	got := loaderAssignments(string(data))
	for _, key := range []string{"BLOCKS_API_KEY", "BLOCKS_BACKEND_URL"} {
		if got[key] != "" {
			t.Errorf("the loader would still read %s=%q:\n%s", key, got[key], string(data))
		}
	}
	if got["OTHER"] != "keep" {
		t.Errorf("the removal lost an unrelated line:\n%s", string(data))
	}
}

// `blocks profile remove` decides which pins belong to the deployment it is forgetting
// by reading their values, so this read has to see the value the loader sees — a pin it
// reads as empty is a pin it leaves behind while telling the user the deployment is
// gone.
func TestEnvFileValue_ReadsTheValueTheLoaderWouldRead(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "# BLOCKS_BACKEND_URL=https://commented.example.test\n  BLOCKS_BACKEND_URL = https://acme.example.com  \n"
	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	value, err := EnvFileValue(envFile, "BLOCKS_BACKEND_URL")
	if err != nil {
		t.Fatalf("EnvFileValue: %v", err)
	}
	if want := loaderAssignments(content)["BLOCKS_BACKEND_URL"]; value != want {
		t.Errorf("EnvFileValue read %q, the loader reads %q", value, want)
	}
}

// Agreeing with the loader about a line is only worth as much as agreeing about where
// the lines are. The loader turns CRLF and lone CR into newlines before it splits, so a
// .env holding carriage returns carries assignments the loader reads and a writer
// splitting on newlines alone does not: it sees one long line, and rewriting the
// variable at the front of it deletes every assignment behind it — a `blocks login`
// that quietly drops the user's other settings — while a removal of one of those
// reports there was nothing to remove.
func TestApplyEnvAt_SeesTheLinesTheLoaderSees(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	initial := "OTHER=keep\r\nBLOCKS_API_KEY=old\rKEEP_ME=yes\r\nBLOCKS_BACKEND_URL=https://acme.example.com\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(tmpDir,
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"},
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Remove: true},
	); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	got := loaderAssignments(string(data))
	for key, want := range map[string]string{
		"BLOCKS_API_KEY":     "bk_new",
		"OTHER":              "keep",
		"KEEP_ME":            "yes",
		"BLOCKS_BACKEND_URL": "",
	} {
		if got[key] != want {
			t.Errorf("the loader would read %s=%q, want %q:\n%q", key, got[key], want, string(data))
		}
	}
}

// envAssignment is one uncommented assignment a .env makes, in the spelling the file used.
type envAssignment struct{ name, value string }

// assignmentsOfVariable lists, in file order, every uncommented assignment whose name is
// key under any case. It answers the question the writer's promise is about: a single
// entry means a first-wins loader, a last-wins loader and a case-insensitive process
// environment cannot come to different conclusions about the variable, while two entries
// mean they can.
//
// It folds with strings.EqualFold rather than through the writer's own notion of variable
// identity, so it says what is in the file rather than what the code under test believes
// about it — which is what lets the same helper state both what the rule promises and
// what it does not.
func assignmentsOfVariable(content, key string) []envAssignment {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	var out []envAssignment
	for _, line := range strings.Split(content, "\n") {
		name, ok := loaderEnvKey(line)
		if !ok || !strings.EqualFold(name, key) {
			continue
		}
		_, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		out = append(out, envAssignment{name: name, value: strings.TrimSpace(value)})
	}
	return out
}

// A Windows process environment is case-insensitive, so a .env holding both
// BLOCKS_API_KEY and blocks_api_key holds one variable there — and the second assignment
// of it is the stale duplicate the collapse exists to remove. A writer comparing names
// byte for byte left it in place, which broke the invariant precisely on the platform
// where it decides what an agent authenticates with: this CLI's loader takes the first
// assignment and the scaffolded agents' dotenv loaders take the last, so the surviving
// spelling could hand a directly-launched agent the credential the user just replaced.
func TestApplyEnvAt_CollapsesACaseVariantOfAVariableTheCLIOwns(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY=old\nOTHER=keep\nblocks_api_key=stale\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(tmpDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	content := string(data)
	got := assignmentsOfVariable(content, "BLOCKS_API_KEY")
	if len(got) != 1 {
		t.Fatalf("expected one assignment of the variable under any spelling, found %v:\n%s", got, content)
	}
	// The surviving line carries the canonical spelling, not the one the stale duplicate
	// used, so the file reads as the CLI writes it.
	if got[0] != (envAssignment{name: "BLOCKS_API_KEY", value: "bk_new"}) {
		t.Errorf("surviving assignment is %v, want BLOCKS_API_KEY=bk_new:\n%s", got[0], content)
	}
	if v := loaderAssignments(content)["BLOCKS_API_KEY"]; v != "bk_new" {
		t.Errorf("the loader would read BLOCKS_API_KEY=%q, want the value just written:\n%s", v, content)
	}
	if loaderAssignments(content)["OTHER"] != "keep" {
		t.Errorf("the rewrite lost an unrelated line:\n%s", content)
	}
}

// `blocks logout` and `blocks profile remove` announce a credential removed off this
// rewrite. A removal that dropped only the spelling it was asked about left a live key in
// the file — one the CLI itself reads on Windows, and one any reader folding case reads
// anywhere — under a sentence saying it was gone.
func TestRemoveEnvKeys_DropsEveryCaseSpellingOfAVariableTheCLIOwns(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("blocks_api_key=stale\nBLOCKS_API_KEY=old\nOTHER=keep\n"), 0600); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveEnvKeys(envFile, "BLOCKS_API_KEY")
	if err != nil {
		t.Fatalf("RemoveEnvKeys: %v", err)
	}
	if len(removed) != 1 || removed[0] != "BLOCKS_API_KEY" {
		t.Errorf("removed = %v, want the variable reported once", removed)
	}
	data, _ := os.ReadFile(envFile)
	content := string(data)
	if got := assignmentsOfVariable(content, "BLOCKS_API_KEY"); len(got) != 0 {
		t.Errorf("assignments left behind after removal: %v\n%s", got, content)
	}
	if loaderAssignments(content)["OTHER"] != "keep" {
		t.Errorf("the removal lost an unrelated line:\n%s", content)
	}
}

// The fold stops at this CLI's own namespace, and that boundary is a promise too. On Unix
// MY_VAR and my_var are two variables and a project may hold both on purpose, so a write
// of one must not delete the other and a removal of one must not take the other with it —
// this package has no standing to reorganise names it does not own. The cost is stated
// rather than hidden: for a name outside BLOCKS_*, "assigned exactly once" is a promise
// per spelling, so a .env assigning two spellings of one application variable is not a
// file the exactly-once invariant covers on Windows. No CLI path writes such a name; the
// only keys this writer is ever handed are BLOCKS_API_KEY, BLOCKS_BACKEND_URL and
// BLOCKS_CDM_URL.
func TestApplyEnvAt_LeavesACaseVariantOfAVariableTheCLIDoesNotOwn(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envFile, []byte("MY_VAR=one\nmy_var=two\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(tmpDir, EnvMutation{Key: "MY_VAR", Value: "three"}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}

	data, _ := os.ReadFile(envFile)
	want := []envAssignment{{name: "MY_VAR", value: "three"}, {name: "my_var", value: "two"}}
	if got := assignmentsOfVariable(string(data), "MY_VAR"); !slices.Equal(got, want) {
		t.Errorf("assignments = %v, want %v — the other spelling is the user's own variable:\n%s", got, want, string(data))
	}

	removed, err := RemoveEnvKeys(envFile, "MY_VAR")
	if err != nil {
		t.Fatalf("RemoveEnvKeys: %v", err)
	}
	if len(removed) != 1 || removed[0] != "MY_VAR" {
		t.Errorf("removed = %v, want the name it was asked to remove", removed)
	}
	data, _ = os.ReadFile(envFile)
	want = []envAssignment{{name: "my_var", value: "two"}}
	if got := assignmentsOfVariable(string(data), "MY_VAR"); !slices.Equal(got, want) {
		t.Errorf("assignments = %v, want %v — a removal must not take another variable with it:\n%s", got, want, string(data))
	}
}

// The read has to agree with the writes, because `blocks profile remove` reads these
// values to decide whether the .env still targets the deployment it is forgetting: a pin
// it reads as absent is a pin it leaves behind. The same boundary applies — a variable
// the CLI owns is found under any spelling, and any other variable under its own.
func TestEnvFileValue_HonoursTheSameVariableIdentityAsTheWrites(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("blocks_backend_url=https://acme.example.com\nmy_var=two\n"), 0600); err != nil {
		t.Fatal(err)
	}

	value, err := EnvFileValue(envFile, "BLOCKS_BACKEND_URL")
	if err != nil {
		t.Fatalf("EnvFileValue: %v", err)
	}
	if value != "https://acme.example.com" {
		t.Errorf("EnvFileValue read %q for a pin spelled in another case, want the value the removal would drop", value)
	}

	value, err = EnvFileValue(envFile, "MY_VAR")
	if err != nil {
		t.Fatalf("EnvFileValue: %v", err)
	}
	if value != "" {
		t.Errorf("EnvFileValue read %q for MY_VAR, want none — my_var is a different variable the writes leave alone", value)
	}
}

// shellHostileEnvValues are values a deployment could hand the writer whose bytes mean
// something to a program reading the .env back. Each is the API key an
// attacker-controlled (or merely broken) deployment returned from the mint call, so each
// is text `blocks login --write-env` would otherwise paste into a file that the project's
// own documentation tells people to source.
//
// The first four execute under `source`; the last three do not execute but are read back
// as some other value, which for a credential is the same defect wearing a smaller hat.
var shellHostileEnvValues = []struct{ name, value string }{
	{"command substitution", "bk_live$(touch pwned)"},
	{"backticks", "bk_live`touch pwned`"},
	{"a second command", "bk_live; touch pwned"},
	{"an and-list", "bk_live && touch pwned"},
	{"a parameter expansion", "bk_live${HOME}"},
	{"a hash", "bk_live # still part of the key"},
	{"a leading tilde", "~/bk_live"},
}

// The invariant, proved rather than asserted: after the writer has written a
// deployment-supplied value, `set -a; . ./.env` in that directory runs nothing the value
// asked for and leaves the variable holding exactly the bytes the deployment sent.
//
// A `.env` is sourced — this CLI's own documentation describes doing it — and until the
// value was quoted, refusing a newline only stopped a value from becoming a *second*
// assignment. It did nothing about a value that executes inside the one assignment it
// was given, which is a shell running an attacker's command under the developer's
// account in the developer's checkout.
//
// The side effect is checked as well as the value because they are two different
// failures: `$(touch pwned)` runs a command *and* corrupts the credential, while a
// leading `~` only corrupts it. A test that compared strings alone would pass for a
// writer that neutralised the value while still handing the shell something to run.
func TestApplyEnvAt_ASourcedEnvRunsNothingADeploymentPutInAValue(t *testing.T) {
	sh, lookErr := exec.LookPath("sh")
	if runtime.GOOS == "windows" || lookErr != nil {
		t.Skip("no POSIX shell to source the .env with")
	}
	for _, tc := range shellHostileEnvValues {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := ApplyEnvAt(dir, EnvMutation{Key: "BLOCKS_API_KEY", Value: tc.value}); err != nil {
				t.Fatalf("ApplyEnvAt: %v", err)
			}
			written, err := os.ReadFile(filepath.Join(dir, ".env"))
			if err != nil {
				t.Fatal(err)
			}

			// HOME points at the case's own directory so a tilde or a $HOME that did
			// expand shows up as a difference rather than as a value that happens to
			// match, and PATH is carried so `touch` is findable if anything does run it.
			cmd := exec.Command(sh, "-c", `set -a; . ./.env; printf %s "$BLOCKS_API_KEY" > read-back`)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("sourcing the written .env failed: %v\nshell said: %s\n.env was: %s", err, out, written)
			}

			if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
				t.Errorf("sourcing the .env ran a command the value carried (%v); the file was:\n%s", err, written)
			}
			readBack, err := os.ReadFile(filepath.Join(dir, "read-back"))
			if err != nil {
				t.Fatal(err)
			}
			if string(readBack) != tc.value {
				t.Errorf("a sourced .env read the key back as %q, want %q; the file was:\n%s", string(readBack), tc.value, written)
			}
		})
	}
}

// Quoting is only worth anything if every reader of the file takes the quotes off again.
// Three readers have to agree, because all three read the file `--write-env` produced:
// this package (EnvFileValue, which is what decides whether a pin still names a
// deployment), the CLI's own loader, and the dotenv loader inside an agent the user
// launched directly.
func TestApplyEnvAt_EveryReaderOfTheFileGetsTheValueBack(t *testing.T) {
	for _, tc := range shellHostileEnvValues {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := ApplyEnvAt(dir, EnvMutation{Key: "BLOCKS_API_KEY", Value: tc.value}); err != nil {
				t.Fatalf("ApplyEnvAt: %v", err)
			}
			envFile := filepath.Join(dir, ".env")
			data, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}

			value, err := EnvFileValue(envFile, "BLOCKS_API_KEY")
			if err != nil {
				t.Fatalf("EnvFileValue: %v", err)
			}
			if value != tc.value {
				t.Errorf("EnvFileValue read %q, want %q; the file was:\n%s", value, tc.value, string(data))
			}
			if got := loaderAssignments(string(data))["BLOCKS_API_KEY"]; got != tc.value {
				t.Errorf("the CLI loader would read %q, want %q; the file was:\n%s", got, tc.value, string(data))
			}
			if got := dotenvAssignments(string(data))["BLOCKS_API_KEY"]; got != tc.value {
				t.Errorf("a dotenv loader would read %q, want %q; the file was:\n%s", got, tc.value, string(data))
			}
		})
	}
}

// The values this CLI actually writes are written plainly: a minted key and a deployment
// URL are made of bytes that mean nothing to a shell or to a dotenv parser, so quoting
// them would churn every .env in existence and teach readers that this CLI's files look
// unlike everyone else's. Quotes appear exactly where the value needed them.
func TestApplyEnvAt_QuotesOnlyAValueThatNeedsIt(t *testing.T) {
	dir := t.TempDir()
	if err := ApplyEnvAt(dir,
		EnvMutation{Key: "BLOCKS_BACKEND_URL", Value: "https://blocks.acme.example:8443/api"},
		EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_live_ABC-123.def"},
		EnvMutation{Key: "BLOCKS_CDM_URL", Value: "https://blocks.acme.example/cdm?v=2"},
	); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	want := "BLOCKS_BACKEND_URL=https://blocks.acme.example:8443/api\n" +
		"BLOCKS_API_KEY=bk_live_ABC-123.def\n" +
		"BLOCKS_CDM_URL='https://blocks.acme.example/cdm?v=2'\n"
	if string(data) != want {
		t.Errorf("content =\n%s\nwant\n%s", string(data), want)
	}
}

// A single quote in a value is refused, because single-quoting is what makes every other
// byte exact and there is no spelling of an embedded single quote that a shell and both
// dotenv parsers read the same way. No key or deployment URL contains one; a value that
// does is a value the writer cannot represent, and saying so beats writing a file whose
// meaning depends on which loader opens it.
func TestApplyEnvAt_RefusesAValueHoldingASingleQuote(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	initial := "BLOCKS_API_KEY=bk_old\n"
	if err := os.WriteFile(envFile, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}

	err := ApplyEnvAt(dir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_live' ; touch pwned ; '"})
	if err == nil {
		t.Fatal("a value holding a single quote must be refused, not written")
	}
	if !strings.Contains(err.Error(), "BLOCKS_API_KEY") {
		t.Errorf("the refusal must name the variable it refused: %v", err)
	}
	if strings.Contains(err.Error(), "bk_live") {
		t.Errorf("the refusal must not echo the credential: %v", err)
	}
	data, _ := os.ReadFile(envFile)
	if string(data) != initial {
		t.Errorf(".env changed despite the refusal:\n%s", string(data))
	}
	assertNoTempFiles(t, dir)
}

// A quoted value in a .env nobody here wrote is read as the value inside the quotes,
// because that is what every other reader of that file already does. Before, a
// hand-written BLOCKS_API_KEY='bk_live' reached the scaffolded agent as the key and
// reached this CLI with the quotes still attached: the agent authenticated, the CLI did
// not, and nothing on either side said why.
func TestEnvFileValue_ReadsAQuotedValueTheWayEveryOtherLoaderDoes(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "BLOCKS_API_KEY='bk_single'\nBLOCKS_BACKEND_URL=\"https://blocks.acme.example\"\nOTHER=`not stripped`\n"
	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ key, want string }{
		{"BLOCKS_API_KEY", "bk_single"},
		{"BLOCKS_BACKEND_URL", "https://blocks.acme.example"},
		// Backticks are left alone: it is the one quoting the two dotenv parsers
		// disagree about, and this writer never produces it.
		{"OTHER", "`not stripped`"},
	} {
		value, err := EnvFileValue(envFile, tc.key)
		if err != nil {
			t.Fatalf("EnvFileValue(%s): %v", tc.key, err)
		}
		if value != tc.want {
			t.Errorf("EnvFileValue(%s) = %q, want %q", tc.key, value, tc.want)
		}
	}
}

// A rewrite of a quoted assignment still leaves the key assigned exactly once: quoting
// changes what a value looks like, not which line assigns it.
func TestApplyEnvAt_ReplacesAQuotedAssignmentInPlace(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("BLOCKS_API_KEY='bk_old'\nKEEP=me\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(dir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"}); err != nil {
		t.Fatalf("ApplyEnvAt: %v", err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "BLOCKS_API_KEY=bk_new\nKEEP=me\n"
	if string(data) != want {
		t.Errorf("content =\n%s\nwant\n%s", string(data), want)
	}
}

// inProjectLink builds the other half of the symlink problem: a checkout that ships .env
// as a link to a file that is already in its own tree. It returns the package directory a
// command runs in and the link standing in for that package's .env; the target is created
// with the mode a tracked file has, since inheriting that mode is half of what made this
// worth closing.
func inProjectLink(t *testing.T, relTarget, content string) (projDir, link, target string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	projDir = filepath.Join(root, "packages", "api")
	if err := os.MkdirAll(projDir, 0700); err != nil {
		t.Fatal(err)
	}
	target = filepath.Join(root, relTarget)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(projDir, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	return projDir, link, target
}

// Inside the project is not enough on its own. A checkout can commit .env as a link to
// any file that already exists in its own tree — a tracked README, the repository's own
// .git/config, a checked-in .env.example — and `blocks login --write-env` would then
// overwrite that file with the API key in it, destroying whatever it held. The rule is
// that the link must name the file it points at: the target's file name has to be the
// link's own name or a plain .env, and no part of the path may be a .git directory.
//
// Nothing at either end may change, because the file this refuses to write is a file
// whose contents the user still needs.
func TestApplyEnvAt_RefusesASymlinkToAFileThatIsNotAnEnv(t *testing.T) {
	for _, tc := range []struct{ name, relTarget, content, wantSaid string }{
		{"a tracked readme", "README.md", "# acme agent\n", "is not a .env"},
		{"the repository's git config", ".git/config", "[core]\n\trepositoryformatversion = 0\n", ".git directory"},
		{"a checked-in example", ".env.example", "BLOCKS_API_KEY=\n", "is not a .env"},
		{"a file in the git directory named like a .env", ".git/.env", "nothing reads this\n", ".git directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projDir, link, target := inProjectLink(t, tc.relTarget, tc.content)

			err := ApplyEnvAt(projDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"})
			if err == nil {
				t.Fatalf("a .env linking to %s must be refused, not followed", tc.relTarget)
			}
			if !strings.Contains(err.Error(), tc.wantSaid) {
				t.Errorf("refusal does not say why (want %q): %v", tc.wantSaid, err)
			}

			data, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("read %s: %v", tc.relTarget, readErr)
			}
			if string(data) != tc.content {
				t.Errorf("the refusal wrote to %s anyway:\n%s", tc.relTarget, string(data))
			}
			if strings.Contains(string(data), "bk_new") {
				t.Errorf("the credential was written into %s", tc.relTarget)
			}
			assertStillASymlink(t, link)
			assertNoTempFiles(t, projDir)
			assertNoTempFiles(t, filepath.Dir(target))
		})
	}
}

// The setup the containment rule exists to serve still works, and the file that ends up
// holding the credential is owner-only afterwards even though the shared file was
// world-readable to begin with. A credential must not inherit a readable mode from
// whatever the link happened to point at.
func TestApplyEnvAt_ForcesOwnerOnlyOnTheFileASharedEnvLinkPointsAt(t *testing.T) {
	projDir, link, sharedDir, sharedEnv := linkedProjectEnv(t, "KEEP=me\n")
	if err := os.Chmod(sharedEnv, 0644); err != nil {
		t.Fatal(err)
	}

	if err := ApplyEnvAt(projDir, EnvMutation{Key: "BLOCKS_API_KEY", Value: "bk_new"}); err != nil {
		t.Fatalf("a shared .env above the package directory must still be written through: %v", err)
	}

	assertStillASymlink(t, link)
	data, err := os.ReadFile(sharedEnv)
	if err != nil {
		t.Fatalf("read shared .env: %v", err)
	}
	for _, want := range []string{"BLOCKS_API_KEY=bk_new", "KEEP=me"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the shared file is missing %q:\n%s", want, string(data))
		}
	}
	if perm := statPerm(t, sharedEnv); perm != 0600 {
		t.Errorf("mode = %v, want 0600: the credential must not keep the mode the shared file had", perm)
	}
	assertNoTempFiles(t, projDir)
	assertNoTempFiles(t, sharedDir)
}

// A link that keeps its own name is followed too, so the rule is "the link names the file
// it points at" rather than "the target is called .env": a package pointing .env.local at
// the .env.local above it is the same shared-file setup under a different name.
func TestUpsertEnvKey_FollowsASymlinkThatKeepsItsOwnName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(root, "packages", "api")
	if err := os.MkdirAll(projDir, 0700); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, ".env.local")
	if err := os.WriteFile(shared, []byte("KEEP=me\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projDir, ".env.local")
	if err := os.Symlink(shared, link); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}

	if err := UpsertEnvKey(link, "BLOCKS_API_KEY", "bk_new"); err != nil {
		t.Fatalf("UpsertEnvKey through a same-named link: %v", err)
	}

	assertStillASymlink(t, link)
	data, err := os.ReadFile(shared)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "BLOCKS_API_KEY=bk_new") || !strings.Contains(string(data), "KEEP=me") {
		t.Errorf("the shared file did not get the write:\n%s", string(data))
	}
	if perm := statPerm(t, shared); perm != 0600 {
		t.Errorf("mode = %v, want 0600 for a file holding a credential", perm)
	}
}
