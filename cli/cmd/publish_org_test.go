package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/auth"
	"github.com/pubnub/blocks-sdk/cli/internal/profiles"
)

func TestResolveOrgKeyUsesCacheThenMints(t *testing.T) {
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	defer func() { profiles.ContextsPathFunc = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"apiKey":"bk_minted","keyId":"k2","expiresAt":""}`))
	}))
	defer srv.Close()

	// Profile has a key for o1 but not o2.
	p := profiles.Profile{BaseURL: srv.URL, Enterprise: true, DefaultOrgID: "o1",
		Orgs: map[string]profiles.OrgKey{"o1": {OrgName: "Finance", ApiKey: "bk_o1"}}}
	if err := profiles.Upsert("acme", p, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Cached org → returns cached key, no mint.
	key, minted, err := resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o1", "Finance")
	if err != nil || key != "bk_o1" || minted {
		t.Fatalf("cached: key=%q minted=%v err=%v", key, minted, err)
	}
	// Uncached org → mints + caches, and says so.
	key, minted, err = resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o2", "IT")
	if err != nil || key != "bk_minted" || !minted {
		t.Fatalf("mint: key=%q minted=%v err=%v", key, minted, err)
	}
	c, _ := profiles.Load()
	// The key exists on the server the moment it is minted, so it has to be cached:
	// dropping it would leave a credential the user cannot see, and the next attempt
	// would mint another one.
	if c.Profiles["acme"].Orgs["o2"].ApiKey != "bk_minted" {
		t.Fatalf("minted key not cached: %+v", c.Profiles["acme"].Orgs)
	}
}

// A stated expiry that cannot be read must not be recorded as "no expiry". The zero
// time is how "the deployment stated no expiry" is stored, and every expiry check
// reads it as never expiring — so converting an unreadable timestamp to the zero time
// hands the CLI a key it trusts forever. It keeps presenting a credential that really
// has expired, never mints the replacement it would mint for an expiry it could read,
// and every command then fails to authenticate with nothing local able to say why.
func TestOrgKeyExpiryFromAnUnreadableDeploymentTimestamp(t *testing.T) {
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name         string
		expiresAt    string
		wantNoExpiry bool
		wantExpired  bool
	}{
		{"no expiry stated", "", true, false},
		{"a future RFC3339 expiry", future, false, false},
		{"a past RFC3339 expiry", past, false, true},
		{"not a date at all", "not-a-date", false, true},
		{"a plausible but wrong format", "2026-09-05 12:00:00", false, true},
		{"a date with no time", "2026-09-05", false, true},
		{"a unix timestamp", "1789000000", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := profiles.OrgKey{ApiKey: "bk_x", ExpiresAt: auth.ParseKeyExpiry(tc.expiresAt)}
			if got := k.ExpiresAt.IsZero(); got != tc.wantNoExpiry {
				t.Errorf("recorded as 'no expiry' = %v, want %v", got, tc.wantNoExpiry)
			}
			if got := k.IsExpired(); got != tc.wantExpired {
				t.Errorf("IsExpired() = %v, want %v — an expiry that cannot be read is not a licence to trust the key forever", got, tc.wantExpired)
			}
		})
	}
}

// The same fact end to end: a deployment answering with a timestamp nobody can read
// gets a replacement minted on the next call, rather than having its key reused as a
// permanently valid one.
func TestResolveOrgKeyRemintsAfterAnUnreadableExpiry(t *testing.T) {
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	defer func() { profiles.ContextsPathFunc = orig }()

	mints := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mints++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"apiKey":"bk_mint_%d","keyId":"k%d","expiresAt":"31/12/2026"}`, mints, mints)
	}))
	defer srv.Close()

	p := profiles.Profile{BaseURL: srv.URL, Enterprise: true, DefaultOrgID: "o1",
		Orgs: map[string]profiles.OrgKey{"o1": {OrgName: "Finance", ApiKey: "bk_o1"}}}
	if err := profiles.Upsert("acme", p, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	key, minted, err := resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o2", "IT")
	if err != nil || key != "bk_mint_1" || !minted {
		t.Fatalf("first mint: key=%q minted=%v err=%v", key, minted, err)
	}
	key, minted, err = resolveOrgPublishKey(srv.URL, "bk_o1", "acme", "o2", "IT")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if !minted || key != "bk_mint_2" {
		t.Fatalf("second resolve reused the key from an unreadable expiry (key=%q minted=%v); it must mint a replacement instead", key, minted)
	}
}

// Resolving the key for a picked organization must not re-point the profile's
// default. This test's premise was the opposite until the picker's writes were
// ordered against the publish itself: DefaultOrgID decides which organization
// every later `run` / `whoami` authenticates as, so switching it here would make a
// publish the user then cancels — or one the registry rejects — silently change
// the organization those commands act as. The switch now happens once the registry
// has accepted the agent, via setProfileDefaultOrg.
func TestResolveOrgKeyLeavesTheDefaultOrgAlone(t *testing.T) {
	dir := t.TempDir()
	orig := profiles.ContextsPathFunc
	profiles.ContextsPathFunc = func() (string, error) { return filepath.Join(dir, "contexts.json"), nil }
	defer func() { profiles.ContextsPathFunc = orig }()

	// Default is o1; both orgs already have cached keys (no mint needed).
	p := profiles.Profile{Enterprise: true, DefaultOrgID: "o1",
		Orgs: map[string]profiles.OrgKey{
			"o1": {OrgName: "Finance", ApiKey: "bk_o1"},
			"o2": {OrgName: "IT", ApiKey: "bk_o2"},
		}}
	if err := profiles.Upsert("acme", p, true); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Pick the cached o2. backendURL is unused — no mint happens on the cache hit.
	key, minted, err := resolveOrgPublishKey("http://unused", "bk_o1", "acme", "o2", "IT")
	if err != nil || key != "bk_o2" || minted {
		t.Fatalf("cached o2: key=%q minted=%v err=%v", key, minted, err)
	}
	c, _ := profiles.Load()
	if got := c.Profiles["acme"].DefaultOrgID; got != "o1" {
		t.Fatalf("DefaultOrgID = %q, want it untouched at o1 until the publish lands", got)
	}

	// Once the agent is registered, the choice becomes the default so run/whoami
	// authenticate as the organization it was published under.
	if err := setProfileDefaultOrg("acme", "o2"); err != nil {
		t.Fatalf("setProfileDefaultOrg: %v", err)
	}
	c, _ = profiles.Load()
	pr := c.Profiles["acme"]
	if pr.DefaultOrgID != "o2" {
		t.Fatalf("DefaultOrgID = %q, want o2 after the publish landed", pr.DefaultOrgID)
	}
	// whoami/run resolve via DefaultOrgKey() — it must now surface o2's key.
	if k, ok := pr.DefaultOrgKey(); !ok || k.ApiKey != "bk_o2" {
		t.Fatalf("DefaultOrgKey = %+v ok=%v, want o2 key bk_o2", k, ok)
	}
}

// multiOrgEnterpriseServer stands in for an enterprise deployment whose caller
// belongs to two organizations. It answers the organization list, mints a key for
// the second organization on demand, and records the credential the registry
// request actually arrived with. Everything else 404s, which every optional
// lookup in the publish flow tolerates.
type multiOrgEnterpriseServer struct {
	orgListCalls  int
	mintCalls     int
	registryCalls int
	// registryAuth is the Authorization header the registry POST carried, i.e. the
	// organization the mutation was really performed as.
	registryAuth string
	// registryStatus rejects the registry POST with this status when non-zero, so a
	// test can exercise what a failed publish leaves behind locally.
	registryStatus int
	// secondOrgName renames the organization the picker's second entry describes, so a
	// test can serve a name a hostile deployment would. Empty means "Operations".
	secondOrgName string
}

// orgListPayload is the organization list the deployment answers with, encoded
// rather than hand-written so a name carrying control characters is transported as
// valid JSON and only becomes dangerous at the point the CLI prints it.
func orgListPayload(secondName string) []byte {
	if secondName == "" {
		secondName = "Operations"
	}
	body, _ := json.Marshal(map[string]any{"orgs": []map[string]string{
		{"id": "org-eng", "name": "Engineering"},
		{"id": "org-ops", "name": secondName},
	}})
	return body
}

func (s *multiOrgEnterpriseServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs":
			s.orgListCalls++
			w.Write(orgListPayload(s.secondOrgName))
		case r.Method == http.MethodPost && r.URL.Path == "/api/auth/api-key/create":
			s.mintCalls++
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"apiKey":"bk_ops","keyId":"k-ops","expiresAt":""}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/registry/agents":
			s.registryCalls++
			s.registryAuth = r.Header.Get("Authorization")
			if s.registryStatus != 0 {
				w.WriteHeader(s.registryStatus)
				w.Write([]byte(`{"error":{"message":"registry unavailable"}}`))
				return
			}
			w.Write(registeredResponseBody(validProjectAgentName))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// enterpriseProjectAt puts the test in a valid agent project pointed at the given
// deployment, with an active enterprise profile that records it and holds a key
// for Engineering only. It returns the agent-card path. Every prompt-affecting
// global is restored by restoreCLIState.
func enterpriseProjectAt(t *testing.T, backendURL string) string {
	t.Helper()
	restoreCLIState(t)
	t.Cleanup(isolateProfiles(t))
	isolateCredentials(t)

	seedProfile(t, "acme.example.com", profiles.Profile{
		BaseURL:      backendURL,
		Enterprise:   true,
		ProductName:  "Acme Corp",
		DefaultOrgID: "org-eng",
		Orgs:         map[string]profiles.OrgKey{"org-eng": {OrgName: "Engineering", ApiKey: "bk_eng"}},
	})

	dir := writeValidProject(t)
	t.Chdir(dir)
	t.Setenv(blocksBackendURLEnv, backendURL)
	t.Setenv(blocksAPIKeyEnv, "")
	t.Setenv(blocksAppBaseURLEnv, "")
	t.Setenv(blocksDashboardURLEnv, "")
	resetPublishFlags()
	return filepath.Join(dir, "agent-card.json")
}

// answerPrompts makes the invocation interactive and feeds the given lines to the
// prompt primitives, so a test can drive the real prompt path instead of calling
// the prompt helpers directly.
func answerPrompts(t *testing.T, lines string) {
	t.Helper()
	isInteractive = func() bool { return true }
	setNoInputMode(false)
	stdinScanner = bufio.NewScanner(strings.NewReader(lines))
	t.Cleanup(func() { setNoInputMode(false) })
}

// A multi-organization enterprise publish must announce the organization it is
// about to act as, which is the one the picker just selected — not the one the
// local precedence resolved before the user was asked. The banner is the only
// warning a user gets that a publish is about to land in the wrong tenant, so it
// has to describe the request that is actually made.
func TestEnterprisePublishBannerNamesThePickedOrganization(t *testing.T) {
	backend := &multiOrgEnterpriseServer{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // pick Operations, the org the profile does not default to

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.orgListCalls == 0 {
		t.Fatal("the organization picker never ran; the test is not exercising it")
	}
	if want := "Bearer bk_ops"; backend.registryAuth != want {
		t.Fatalf("registry POST authorization = %q, want %q — the publish did not act as the picked organization",
			backend.registryAuth, want)
	}
	if want := "[acme.example.com / Operations]"; !strings.Contains(output, want) {
		t.Errorf("banner must name the organization the publish is authorized as (%s):\n%s", want, output)
	}
	if strings.Contains(output, "/ Engineering]") {
		t.Errorf("banner names an organization the publish did not act as:\n%s", output)
	}
}

// The organization is the only part of the target a publish cannot state before it
// starts, and the picker itself is the first thing that touches the user's local
// state. So the deployment — settled by the profile or the ambient backend URL
// before the command body ran — must be stated before the picker asks anything,
// and the organization once it is known. Printing nothing until after the picker
// would mean the operator's first sight of where the command is pointed came after
// a key had already been minted for a tenant they had only just named.
func TestEnterprisePublishStatesTheDeploymentBeforeThePicker(t *testing.T) {
	backend := &multiOrgEnterpriseServer{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // pick Operations, the org the profile does not default to

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	deployment := strings.Index(output, "Deployment: acme.example.com")
	prompt := strings.Index(output, "Which organization should own this agent?")
	banner := strings.Index(output, "[acme.example.com / Operations]")
	switch {
	case deployment < 0:
		t.Fatalf("the deployment must be stated before the picker prompts:\n%s", output)
	case prompt < 0:
		t.Fatalf("the organization picker never prompted; the test is not exercising it:\n%s", output)
	case banner < 0:
		t.Fatalf("the context banner must name the chosen organization:\n%s", output)
	case deployment > prompt:
		t.Errorf("the deployment is stated after the picker has already prompted:\n%s", output)
	case banner < prompt:
		t.Errorf("the organization is named before it has been chosen:\n%s", output)
	}

	// The banner is only worth printing if it describes the request that was made:
	// the key on the wire is the picked organization's, not the profile default's.
	if want := "Bearer bk_ops"; backend.registryAuth != want {
		t.Fatalf("registry POST authorization = %q, want %q — the banner named an organization the request was not authorized as",
			backend.registryAuth, want)
	}

	// The mint cannot wait for the publish to succeed — the key is what authorizes
	// the request — so the user is told it now exists.
	if backend.mintCalls != 1 {
		t.Fatalf("mint calls = %d, want 1", backend.mintCalls)
	}
	if !strings.Contains(output, "Created an API key for Operations") {
		t.Errorf("a minted credential must be reported, not left invisible:\n%s", output)
	}

	// The registry accepted the agent, so the choice now becomes the profile's
	// default and later run/whoami authenticate as the organization it was published
	// under. This is the write the picker used to perform up front.
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if got := store.Profiles["acme.example.com"].DefaultOrgID; got != "org-ops" {
		t.Errorf("default organization = %q, want org-ops once the publish landed", got)
	}
}

// A publish that the registry rejects must leave the organization later commands
// act as exactly as it was. The picker runs before the request, so without
// ordering its writes against the outcome a failed publish would re-point the
// profile's default at an organization nothing was ever published to, and the next
// `blocks run` would authenticate as it.
func TestFailedEnterprisePublishKeepsTheDefaultOrganization(t *testing.T) {
	backend := &multiOrgEnterpriseServer{registryStatus: http.StatusInternalServerError}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // pick Operations

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err == nil {
			t.Fatal("expected the rejected registry POST to fail the publish")
		}
	})

	if want := "Bearer bk_ops"; backend.registryAuth != want {
		t.Fatalf("registry POST authorization = %q, want %q", backend.registryAuth, want)
	}
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	p := store.Profiles["acme.example.com"]
	if got := p.DefaultOrgID; got != "org-eng" {
		t.Errorf("default organization = %q, want org-eng — a publish that never landed must not re-point later commands", got)
	}
	// The key minted for the request is deliberately kept: it exists on the server
	// either way, so caching it is what lets a retry reuse it instead of minting a
	// second one, and it was reported when it was created.
	if got := p.Orgs["org-ops"].ApiKey; got != "bk_ops" {
		t.Errorf("minted key for org-ops = %q, want it cached so a retry reuses it", got)
	}
}

// A credential the invocation was handed already decides which organization the
// request is authorized as. Offering a picker on top of it would swap the key for
// another organization's behind the caller's back — and would leave the key the
// caller supplied, and anything else reading it, pointing at a different tenant
// than the agent was just published under.
func TestEnterprisePublishKeepsACredentialTheInvocationSupplied(t *testing.T) {
	backend := &multiOrgEnterpriseServer{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	t.Setenv(blocksAPIKeyEnv, "bk_supplied")
	answerPrompts(t, "2\n") // would select Operations if the picker were offered

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.orgListCalls != 0 || backend.mintCalls != 0 {
		t.Errorf("a supplied credential must not be put to an organization picker (orgs=%d mints=%d)",
			backend.orgListCalls, backend.mintCalls)
	}
	if want := "Bearer bk_supplied"; backend.registryAuth != want {
		t.Fatalf("registry POST authorization = %q, want %q — the supplied credential was replaced",
			backend.registryAuth, want)
	}
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.Load: %v", err)
	}
	if got := store.Profiles["acme.example.com"].DefaultOrgID; got != "org-eng" {
		t.Errorf("default organization = %q, want org-eng — a supplied credential must not re-point the profile", got)
	}
}

// hostileOrgName is what a deployment can return for an organization name: erase
// the line, return to column zero, and retitle the window. Printed raw, it can wipe
// out the deployment line above it and make the picker offer two entries that read
// identically, so the answer "2" no longer means what the user saw.
const hostileOrgName = "Operations\x1b[2K\r\x1b]0;pwned\x07Engineering"

// The organization names in the picker and in the mint notice are the deployment's
// text, not the CLI's, so no byte of them may reach the terminal as a control
// character. The assertion is on the rendered output because that is where the
// damage would happen: escaping anywhere earlier is invisible to the user.
func TestEnterprisePublishEscapesAHostileOrganizationName(t *testing.T) {
	backend := &multiOrgEnterpriseServer{secondOrgName: hostileOrgName}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // pick the hostile organization

	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
	})

	if backend.orgListCalls == 0 || backend.mintCalls != 1 {
		t.Fatalf("the picker and the mint must both have run (orgs=%d mints=%d)", backend.orgListCalls, backend.mintCalls)
	}
	// Nowhere in the output — picker entry, mint notice or context banner.
	for _, raw := range []struct {
		what string
		seq  string
	}{
		{"erase-line", "\x1b[2K"},
		{"OSC window title", "\x1b]0;"},
		{"BEL", "\x07"},
		{"carriage return", "\r"},
	} {
		if i := strings.Index(output, raw.seq); i >= 0 {
			t.Errorf("a %s sequence from the organization name reached the terminal raw, in %q",
				raw.what, lineAtOffset(output, i))
		}
	}
	// Both sites must have printed the name, escaped rather than dropped, so the
	// tampering is visible and cannot be mistaken for the legitimate name.
	for _, line := range []string{
		lineContaining(t, output, "[2] "),
		lineContaining(t, output, "Created an API key for"),
	} {
		if !strings.Contains(line, `Operations\x1b[2K`) {
			t.Errorf("line %q must show the escaped sequence, not swallow it", line)
		}
	}
}

// lineAtOffset returns the output line containing byte offset i, so a failure names
// the line that was compromised instead of dumping the whole run.
func lineAtOffset(output string, i int) string {
	start := strings.LastIndexByte(output[:i], '\n') + 1
	end := strings.IndexByte(output[i:], '\n')
	if end < 0 {
		return output[start:]
	}
	return output[start : i+end]
}

// lineContaining returns the single output line holding want, failing the test when
// no line does.
func lineContaining(t *testing.T, output, want string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no output line contains %q:\n%s", want, output)
	return ""
}

// A mint is a remote write, and the only local record of it is the profile store. So
// when that store cannot be written, the key exists on the deployment and nothing
// anywhere names it: the user cannot list it, revoke it, or stop the next attempt
// from creating another. The command must say so and must fail — reporting the mint
// only on the path where persistence succeeded is what made the orphan invisible.
func TestEnterprisePublishReportsAMintItCouldNotPersist(t *testing.T) {
	backend := &multiOrgEnterpriseServer{}
	srv := backend.start(t)
	cardPath := enterpriseProjectAt(t, srv.URL)
	answerPrompts(t, "2\n") // pick Operations, the org with no cached key

	// Make the store genuinely unwritable rather than stubbing the write: the profile
	// file is read-only, so it still loads and the mint still happens, and the failure
	// lands exactly where a real permissions problem would.
	path, err := profiles.ContextsPathFunc()
	if err != nil {
		t.Fatalf("ContextsPathFunc: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	var execErr error
	output := captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "private", "--billing-mode", "free"})
		execErr = rootCmd.Execute()
	})

	if execErr == nil {
		t.Fatal("a publish that could not record the key it minted must fail, not report success")
	}
	if backend.mintCalls != 1 {
		t.Fatalf("mint calls = %d, want 1", backend.mintCalls)
	}
	if backend.registryCalls != 0 {
		t.Errorf("registry calls = %d, want 0 — the publish must not proceed on state it could not record", backend.registryCalls)
	}
	// The organization and the deployment are what make the orphan findable in the
	// dashboard, so both have to be named.
	if want := "Created an API key for Operations on acme.example.com."; !strings.Contains(output, want) {
		t.Errorf("output must report the key that was created (%q):\n%s", want, output)
	}
	if !strings.Contains(output, "could NOT be stored") {
		t.Errorf("output must say the key was not stored, so the user knows to revoke it:\n%s", output)
	}
	if !strings.Contains(execErr.Error(), "created an API key for Operations") {
		t.Errorf("the error must name the key that was created, not just the write that failed: %v", execErr)
	}
}
