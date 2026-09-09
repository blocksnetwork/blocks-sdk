package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/termsafe"
)

// EnsureCredentialsProfile runs the Blocks auth flow and returns the minted
// credential record (org id/name/key id/expiry) so the caller can persist it into
// a profile. It does NOT write the legacy credentials.json — the caller owns
// persistence (a profile in contexts.json).
//
// suppliedKey is a key the caller was handed for this invocation (--api-key, or
// one piped via --api-key-stdin). When it is non-empty the browser flow is skipped
// and the record carries only the ApiKey, since no org metadata is known without a
// session. The caller resolves that key: a piped key can only be read once, so the
// read belongs to whichever component owns the invocation's credential rather than
// happening again here.
func EnsureCredentialsProfile(ctx context.Context, backendURL, clientID, suppliedKey string) (*Credentials, string, error) {
	if suppliedKey != "" {
		return &Credentials{ApiKey: suppliedKey}, suppliedKey, nil
	}
	if backendURL == "" {
		return nil, "", fmt.Errorf("instance URL/BLOCKS_BACKEND_URL must be set")
	}
	if clientID == "" {
		return nil, "", fmt.Errorf("OAuth client id must be set (CLI_OAUTH_CLIENT_ID / cli-config)")
	}
	authURL := backendURL + "/api/auth/oauth2/authorize"
	tokenURL := backendURL + "/api/auth/oauth2/token"
	fmt.Println("  Opening browser for login...")
	result, err := RunBrowserFlow(ctx, authURL, clientID, backendURL)
	if err != nil {
		return nil, "", fmt.Errorf("browser login failed: %w", err)
	}
	exchangeResp, err := ExchangeCode(tokenURL, result.Code, result.CodeVerifier, result.RedirectURI, clientID, result.Audience)
	if err != nil {
		return nil, "", fmt.Errorf("token exchange failed: %w", err)
	}
	newCreds, err := FetchOrCreateApiKey(backendURL, exchangeResp.AccessToken)
	if err != nil {
		return nil, "", fmt.Errorf("API key creation failed: %w", err)
	}
	return newCreds, newCreds.ApiKey, nil
}

// orgMembership represents a user's membership in an organization.
type orgMembership struct {
	OrgId   string `json:"orgId"`
	OrgName string `json:"orgName"`
}

// ApiKeyCreateResponse is the response from POST /api/auth/api-key/create.
type ApiKeyCreateResponse struct {
	ApiKey    string `json:"apiKey"`
	KeyId     string `json:"keyId"`
	ExpiresAt string `json:"expiresAt"`
}

// unreadableKeyExpiry is the expiry recorded for a key whose stated expiry could not
// be read: the epoch, which is already past under every expiry check, and recognisable
// on sight as a date no deployment issued.
var unreadableKeyExpiry = time.Unix(0, 0).UTC()

// ParseKeyExpiry converts the expiresAt a deployment returned alongside a minted API
// key into the expiry to record for that key.
//
// It draws the distinction the zero time cannot. An empty expiresAt means the
// deployment stated no expiry, and the zero time is exactly how "no expiry" is
// recorded — every expiry check treats it as never expiring, which is correct. A
// non-empty expiresAt that cannot be parsed is a different fact, and recording it as
// the zero time makes the CLI trust that key forever: it keeps presenting a credential
// that has really expired and never mints the replacement it would have minted for an
// expiry it could read, so every later command fails to authenticate with nothing on
// the client able to explain why.
//
// An unreadable expiry is therefore recorded as already expired. The cost is at worst
// one extra key, minted and cached by the next command that needs one; the cost of
// trusting it is a CLI that cannot authenticate and cannot say why. A deployment
// answering with a timestamp nobody can read has not earned an extension of its
// credential's life.
//
// It does not report an error, because by the time it is called the key exists on the
// deployment and is usable: failing here would reject a publish or a login that had
// otherwise succeeded and abandon a live credential that nothing local names.
func ParseKeyExpiry(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return unreadableKeyExpiry
	}
	return t
}

// FetchOrCreateApiKey fetches the user's org memberships, selects an org,
// and creates an API key for the CLI.
func FetchOrCreateApiKey(backendURL, sessionToken string) (*Credentials, error) {
	orgs, err := fetchOrgs(backendURL, sessionToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch organizations: %w", err)
	}

	if len(orgs) == 0 {
		return nil, fmt.Errorf("your account has no organization memberships — create or join an org first")
	}

	var selected orgMembership
	if len(orgs) == 1 {
		selected = orgs[0]
		// The organization name comes from the backend, so it reaches the terminal
		// through termsafe.Text: it is the name of the organization a key is about to
		// be minted in, and text that can move the cursor or erase a line can make
		// that sentence — and the ones around it — say something else.
		fmt.Printf("  Using organization: %s\n", termsafe.Text(selected.OrgName))
	} else {
		selected, err = promptOrgSelection(orgs)
		if err != nil {
			return nil, err
		}
	}

	hostname, _ := os.Hostname()
	keyName := BuildApiKeyName(hostname)

	keyResp, err := createApiKey(backendURL, sessionToken, selected.OrgId, keyName)
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	return &Credentials{
		ApiKey:    keyResp.ApiKey,
		OrgId:     selected.OrgId,
		OrgName:   selected.OrgName,
		KeyId:     keyResp.KeyId,
		ExpiresAt: ParseKeyExpiry(keyResp.ExpiresAt),
	}, nil
}

// fetchOrgs retrieves the user's organization memberships from the backend.
func fetchOrgs(backendURL, sessionToken string) ([]orgMembership, error) {
	req, err := http.NewRequest("GET", backendURL+"/api/v1/orgs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		// The body is whatever the server sent and it is quoted verbatim into a
		// message the user reads, so it reaches the terminal through termsafe.Text.
		// The same applies wherever this package echoes a response body.
		return nil, fmt.Errorf("GET /api/v1/orgs failed (HTTP %d): %s", resp.StatusCode, termsafe.Text(string(body)))
	}

	// Backend returns { orgs: [{ id, name, slug, ... }], billingEnabled }
	var envelope struct {
		Orgs []struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		} `json:"orgs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("failed to decode org response: %w", err)
	}

	result := make([]orgMembership, len(envelope.Orgs))
	for i, o := range envelope.Orgs {
		result[i] = orgMembership{OrgId: o.Id, OrgName: o.Name}
	}
	return result, nil
}

// promptOrgSelection presents the user with a numbered list of orgs and reads
// their choice from stdin.
//
// Every backend-supplied field in the list goes through termsafe.Text. This is the
// list a user picks from, and the numbers beside the names are the whole basis of the
// choice: a name carrying a cursor-up or erase-line sequence can redraw the rows above
// it, so the org the user selects is not the org they read.
func promptOrgSelection(orgs []orgMembership) (orgMembership, error) {
	fmt.Println("\n  You belong to multiple organizations. Select one:")
	for i, org := range orgs {
		fmt.Printf("    [%d] %s (%s)\n", i+1, termsafe.Text(org.OrgName), termsafe.Text(org.OrgId))
	}
	fmt.Print("  Enter number: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return orgMembership{}, fmt.Errorf("no input received")
	}
	input := strings.TrimSpace(scanner.Text())

	var choice int
	if _, err := fmt.Sscanf(input, "%d", &choice); err != nil || choice < 1 || choice > len(orgs) {
		return orgMembership{}, fmt.Errorf("invalid selection: %q — expected a number between 1 and %d", input, len(orgs))
	}

	selected := orgs[choice-1]
	fmt.Printf("  Selected: %s\n", termsafe.Text(selected.OrgName))
	return selected, nil
}

// CreateOrgAPIKey mints an org-scoped API key. `bearer` may be an OAuth session
// token (login) or an existing org-scoped API key (publish-time per-org mint) —
// the backend verifies the caller's membership of `orgId` either way.
func CreateOrgAPIKey(backendURL, bearer, orgId, keyName string) (*ApiKeyCreateResponse, error) {
	return createApiKey(backendURL, bearer, orgId, keyName)
}

// createApiKey calls the backend to create an API key for the given org.
func createApiKey(backendURL, sessionToken, orgId, keyName string) (*ApiKeyCreateResponse, error) {
	payload := map[string]string{
		"name":  keyName,
		"orgId": orgId,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", backendURL+"/api/auth/api-key/create", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST /api/auth/api-key/create failed (HTTP %d): %s",
			resp.StatusCode, termsafe.Text(string(respBody)))
	}

	var result ApiKeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode API key response: %w", err)
	}
	if result.ApiKey == "" {
		return nil, fmt.Errorf("server returned empty API key")
	}
	return &result, nil
}

// InjectEnv writes the given key=value to the .env file in the current
// working directory. If the file does not exist, it creates one.
func InjectEnv(key, value string) error {
	return InjectEnvAt(".", key, value)
}

// InjectEnvAt writes the given key=value to the .env file in the specified
// directory. If the file does not exist, it creates one.
func InjectEnvAt(dir, key, value string) error {
	return ApplyEnvAt(dir, EnvMutation{Key: key, Value: value})
}

// EnvMutation is one change to a project .env: Key is assigned Value, or Key's
// assignment is dropped when Remove is set.
type EnvMutation struct {
	Key    string
	Value  string
	Remove bool
}

// ApplyEnvAt applies every mutation to the .env file in dir as a single rewrite,
// creating the file when it does not exist. The file ends up with all of the
// mutations applied or with none of them.
//
// Applying them together is the point, not an optimization. A caller writing a
// freshly minted credential and the deployment URL that credential belongs to has
// one fact to record, and two separate writes can fail or be interrupted between
// them — leaving the new key beside the previous deployment's URL, which is a
// credential every later command sends to the wrong place. One rewrite has no such
// window, and replaceEnvFile is what makes that rewrite itself indivisible.
func ApplyEnvAt(dir string, mutations ...EnvMutation) error {
	envFile := filepath.Join(dir, envFileName)
	existing, exists, err := readEnvFile(envFile)
	if err != nil {
		return err
	}
	if !exists {
		return createEnvFile(envFile, mutations)
	}
	lines, assigned, removed, err := applyEnvMutations(existing, mutations)
	if err != nil {
		return err
	}
	if len(assigned) == 0 && len(removed) == 0 {
		return nil
	}
	if err := replaceEnvFile(envFile, strings.Join(lines, "\n")); err != nil {
		return fmt.Errorf("could not update %s: %w", envFile, err)
	}
	fmt.Printf("  %s %s\n", envFile, describeEnvChange(assigned, removed))
	return nil
}

// describeEnvChange words what a rewrite did, naming removals as removals so a
// user reading the line can tell a key that was set from one that was dropped.
func describeEnvChange(assigned, removed []string) string {
	switch {
	case len(assigned) == 0:
		return "updated: removed " + strings.Join(removed, ", ")
	case len(removed) == 0:
		return "updated with " + strings.Join(assigned, ", ")
	}
	return "updated with " + strings.Join(assigned, ", ") + " (removed " + strings.Join(removed, ", ") + ")"
}

// createEnvFile writes a new .env holding the assignments among mutations. A
// mutation that only removes a key is skipped: there is nothing to remove from a
// file that does not exist, and creating one to record that would leave a stray
// file behind.
//
// It builds the file's lines with applyEnvMutations — the same fold the existing-file
// path uses, over no lines — rather than formatting them itself. Creating a file is
// otherwise a second place where a mutation becomes text, and so a second place a
// mutation could reach the disk without being checked.
func createEnvFile(envFile string, mutations []EnvMutation) error {
	lines, keys, _, err := applyEnvMutations(nil, mutations)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	if err := replaceEnvFile(envFile, strings.Join(lines, "\n")+"\n"); err != nil {
		return fmt.Errorf("could not create %s: %w", envFile, err)
	}
	fmt.Printf("  %s created with %s\n", envFile, strings.Join(keys, ", "))
	return nil
}

// envValueRejections are the bytes a .env value must not contain, each with the name
// the refusal calls it by and the reason it cannot be written.
//
// A newline is the original problem: the writer joins a name to a value with an "=", so
// a value carrying one writes a second line, and the .env loader imports that line on
// the next invocation as a variable in its own right. The value here is text a
// deployment supplied — the API key it minted — so a deployment returning
// "bk_live\nHTTPS_PROXY=..." would otherwise be setting the proxy for every later
// command run in that directory. A lone carriage return is the same second line by
// another spelling, because the loader normalises CR to a newline before splitting. A
// NUL cannot survive in an environment variable at all, so a value holding one is a
// credential that is already damaged.
//
// A single quote is refused for a different reason: encodeEnvValue writes anything that
// is not plainly inert inside single quotes, and there is no spelling of an embedded
// single quote that a shell, Node's dotenv and Python's python-dotenv all read back the
// same way — a shell understands the close-escape-reopen spelling of it and neither
// dotenv parser does. Refusing the one byte is what lets the quoting be exact for every
// other byte.
var envValueRejections = []struct {
	char byte
	name string
	why  string
}{
	{'\n', "a newline", "would be read back as a second assignment"},
	{'\r', "a carriage return", "would be read back as a second assignment"},
	{0, "a NUL byte", "cannot survive in an environment variable"},
	{'\'', "a single quote", "cannot be quoted in a way every reader of a .env agrees on"},
}

// inertEnvValueByte reports whether c may stand in a .env value with no quoting around
// it: the bytes that mean nothing to a shell reading `KEY=value` after `source .env`,
// and nothing to the dotenv parsers in the scaffolded Node and Python agents.
//
// It is an allowlist on purpose. The set of bytes that *are* dangerous has to be
// complete against every shell and both dotenv parsers at once — command substitution
// and parameter expansion ($ and a backtick), the bytes that end a shell word and start
// a command (";", "&", "|", "(", ")", "<", ">", whitespace), the bytes a shell removes
// as quoting ("\\", a quote), a leading "~", and "#", which both dotenv parsers read as
// the start of a comment and truncate the value at. Enumerating that correctly is a
// standing liability; enumerating what is safe is not.
//
// Letters, digits and `_-./:@+,%=` are what the values this package writes are made of:
// a minted API key is alphanumeric with underscores behind a "bk_" prefix, and a
// deployment URL is a scheme, a host, a port and a path. So the ordinary write stays
// unquoted and a .env keeps the shape its reader expects, while anything else — which
// for these variables means a value no legitimate deployment sends — is quoted.
func inertEnvValueByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_-./:@+,%=", c) >= 0
}

// encodeEnvValue renders value as the right-hand side of a .env assignment: value
// itself when every byte of it is inert, and value inside single quotes otherwise.
//
// Single quotes rather than double, because single quotes are the one quoting a shell,
// Node's dotenv and python-dotenv all treat as literal — a double-quoted value is still
// subject to $-expansion under `source` and to escape decoding in both parsers, so it
// would be quoted and still not be the value that was written. Combined with the
// refusal of an embedded single quote, the result is that the bytes between the quotes
// are read back exactly, by all three.
//
// Quoting only when it is needed rather than always is what keeps a .env written by
// this CLI looking like a .env: BLOCKS_API_KEY=bk_… and BLOCKS_BACKEND_URL=https://…
// are unchanged, and a reader diffing the file sees quotes exactly where the value
// contained something that needed them.
func encodeEnvValue(value string) string {
	for i := 0; i < len(value); i++ {
		if !inertEnvValueByte(value[i]) {
			return "'" + value + "'"
		}
	}
	return value
}

// unquoteEnvValue removes one pair of matching outer quotes from a .env value. It is
// the read half of encodeEnvValue, and it is also how both dotenv parsers and a shell
// read a quoted value, so the same file means the same thing to the CLI, to a
// directly-launched Node or Python agent, and to `source .env`.
//
// Applying it to every value, not only to values this package wrote, is the point: a
// hand-written BLOCKS_API_KEY="bk_…" already reads as the key without its quotes in the
// scaffolded agents, and used to reach the CLI with the quotes still attached — a
// credential that authenticated for the agent and failed for the CLI, with nothing to
// say why.
//
// It is deliberately only quote removal. A backtick-quoted value is left alone, because
// that is the one shape the two dotenv parsers disagree about (Node strips it, Python
// does not), and neither escape decoding nor ${VAR} interpolation inside double quotes
// is performed, because the two parsers disagree there too. This writer never produces
// either shape.
func unquoteEnvValue(value string) string {
	if len(value) < 2 {
		return value
	}
	quote := value[0]
	if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
		return value[1 : len(value)-1]
	}
	return value
}

// validEnvKey reports whether key is a name an assignment may be written for: the
// shape an environment variable actually has. The names this package writes are all
// of that shape, and holding to it is what rules out a name carrying an "=",
// whitespace, a "#" or a line break — each of which would produce a line the loader
// reads as some other variable, or as two.
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// checkEnvMutation reports why a mutation must not be written, or nil when it may be.
//
// A rejected value is refused rather than repaired. Stripping the line break would
// store a credential cut down to its first line, which looks stored and is not: every
// later command fails to authenticate with no indication of why, whereas a refusal
// names the variable and stops. For the same reason the message never echoes the
// value — this is the one variable in the file whose contents are a secret.
func checkEnvMutation(m EnvMutation) error {
	if !validEnvKey(m.Key) {
		return fmt.Errorf("refusing to write %q to a .env: a variable name must be letters, digits and underscores, and must not start with a digit", m.Key)
	}
	if m.Remove {
		return nil
	}
	for _, rejected := range envValueRejections {
		if strings.IndexByte(m.Value, rejected.char) >= 0 {
			return fmt.Errorf("refusing to write %s to a .env: its value contains %s, which %s — the value is not what it claims to be, so nothing was written; set the variable yourself if it is genuinely correct", m.Key, rejected.name, rejected.why)
		}
	}
	return nil
}

// applyEnvMutations folds the mutations into the file's lines and reports which
// keys it assigned and which it removed. An assignment leaves the key assigned
// exactly once; a removal drops every assignment of it and is reported only when a
// line was actually there. Comments, spacing and key order are preserved because
// the lines are edited in place.
//
// It is the one place the .env line edits live, so a key written here and a key
// removed here cannot disagree about which line they meant. It is therefore also
// where every mutation is checked, before any of them is folded in: this is the only
// route from an EnvMutation to a line of a .env, so a call site added later cannot
// reach the disk without passing the check. Checking the whole batch first is what
// makes a refusal leave the file untouched — and a batch exists precisely so a
// credential and the targeting it belongs to land together, which writing the
// acceptable half of one would defeat.
func applyEnvMutations(lines []string, mutations []EnvMutation) (out, assigned, removed []string, err error) {
	for _, m := range mutations {
		if err := checkEnvMutation(m); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, m := range mutations {
		if m.Remove {
			kept, dropped := dropEnvAssignments(lines, m.Key)
			if dropped {
				lines = kept
				removed = append(removed, m.Key)
			}
			continue
		}
		lines = setEnvAssignment(lines, m.Key, m.Value)
		assigned = append(assigned, m.Key)
	}
	return lines, assigned, removed, nil
}

// setEnvAssignment leaves key assigned value exactly once: the first uncommented
// assignment of key becomes the new one, every later assignment of it is dropped,
// and the assignment is appended when the key was absent. The surviving line keeps
// the position the key already had, so an edited .env still reads in its own order.
// It is spelled with the caller's own name for the variable, so a rewrite that
// replaced a differently-cased assignment of a variable this CLI owns leaves the
// canonical spelling behind rather than the spelling it found.
//
// Collapsing duplicates is a correctness requirement, not tidiness. This CLI's own
// .env loader takes the first value for a key, while the dotenv loaders in the
// scaffolded Node and Python agents take the last. A stale second assignment left
// behind therefore points an agent or trigger launched directly at the deployment
// the user just moved off, while every CLI command in the same directory uses the
// new one — a wrong target with no error anywhere to say so. Because this is the one
// writer, the invariant it leaves behind is that a key appears at most once, and
// first-wins and last-wins loaders cannot disagree about a file like that.
//
// "A key" is sameEnvVariable's key. For the variables this CLI owns — which is every
// variable it writes — that covers a duplicate spelled in another case, because on a
// case-insensitive environment such a duplicate is the same stale second assignment by
// another name. For a variable outside that namespace the invariant is per spelling, and
// deliberately so: see sameEnvVariable.
// The value is rendered by encodeEnvValue rather than pasted in, and this is the only
// place a value becomes a line, so no value can reach a .env unquoted that needed
// quoting. What that buys is stated at encodeEnvValue: the file is a set of assignments
// and nothing else, whether it is read by this CLI, by a dotenv loader in a
// directly-launched agent, or by a shell that was told to `source` it.
func setEnvAssignment(lines []string, key, value string) []string {
	assignment := key + "=" + encodeEnvValue(value)
	kept := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		switch {
		case !EnvLineMatchesKey(line, key):
			kept = append(kept, line)
		case !found:
			kept = append(kept, assignment)
			found = true
		}
	}
	if !found {
		return append(kept, assignment)
	}
	return kept
}

// dropEnvAssignments removes every uncommented assignment of key — under every spelling
// of its name that sameEnvVariable counts as the same variable — and reports whether any
// line was there to remove, so a caller can tell "nothing to do" from "done" without
// rewriting the file to find out. Every spelling matters here more than anywhere else:
// the callers are `blocks logout` and `blocks profile remove`, and a surviving line is a
// live credential under a file the user was told no longer holds one.
func dropEnvAssignments(lines []string, key string) ([]string, bool) {
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if !EnvLineMatchesKey(line, key) {
			kept = append(kept, line)
		}
	}
	return kept, len(kept) != len(lines)
}

// readEnvFile reads the .env at path into its lines and reports whether the file was
// there at all. It is the read counterpart of replaceEnvFile, and the only way this
// package reads a .env, so every caller draws the same line in the same place.
//
// That line is the point: a file that does not exist is not a failure — a project
// without a .env is the ordinary case, and there is genuinely nothing in it to read or
// remove — while a file that exists and cannot be read is. Folding the two together is
// what let a caller announce a credential removed, or a deployment forgotten, off a
// file it never managed to open: nothing was found, so nothing was removed, so nothing
// was wrong. errors.Is(err, fs.ErrNotExist) is the discriminator, since it also covers
// a path whose parent directory is missing and survives the wrapping os returns.
//
// The split is the loader's split: CRLF and lone CR become newlines first, exactly as
// loadEnvFile does it, because agreeing with the loader about which line assigns a
// variable is worth nothing without agreeing about where the lines are. A .env holding
// carriage returns otherwise reads here as one long line, and rewriting the variable at
// the front of it deletes every assignment behind it while a removal of one of those
// reports there was nothing to remove. The cost is that rewriting such a file settles
// it on newline endings, which is the honest consequence of reading it the way
// everything that consumes it reads it.
func readEnvFile(path string) (lines []string, exists bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("could not read %s: %w", path, err)
	}
	return splitEnvLines(string(data)), true, nil
}

// splitEnvLines cuts a .env's bytes into the lines the loader sees, and is the only
// place this package decides where a .env's lines end.
func splitEnvLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.Split(content, "\n")
}

// replaceEnvFile puts content at path in one indivisible step: it writes a temp
// file beside the file it is replacing and renames it over that file, and a rename
// within one directory either happens or does not. The file being replaced is not
// always path — when path is a symlink it is the file the link resolves to, which
// envWriteTarget decides and is the only reason this writer looks at the path at all
// beyond its directory.
//
// It is the only way this package writes a .env, because a truncating write is not
// indivisible however few of them there are. os.WriteFile empties the file and then
// fills it, so an interruption, a full disk or a short write leaves a .env holding
// part of the rewrite — which for these callers means a freshly minted credential
// with no targeting beside it, or targeting with the credential's line lost. That is
// the pairing ApplyEnvAt batches its mutations to prevent, and batching alone cannot
// prevent it.
//
// The temp file must share the directory of the file being replaced: rename is only
// atomic within a filesystem, and the system temp dir is frequently a different one.
// A directory that cannot be written is therefore a failed rewrite even when the .env
// itself is writable, which is the honest answer — a .env that cannot be replaced
// atomically must not be replaced at all. That directory is the *resolved* file's,
// because envWriteTarget may have followed a symlink out of the project's own tree.
//
// The result always carries envFileMode, whatever mode the file being replaced had.
// Inheriting was wrong in the direction that matters: the file being replaced is not
// always one this CLI created — it can be whatever a .env symlink resolved to — so a
// rewrite that kept the old mode would put a credential into a file at 0644 because
// that is the mode the file it landed on happened to have. Narrowing a project's .env
// to owner-only is a change a user may notice; leaving a world-readable credential is
// one they will not.
func replaceEnvFile(path, content string) error {
	target, err := envWriteTarget(path)
	if err != nil {
		return err
	}
	// Said before the write, not after: this is the file the command is about to
	// change, and a user who did not realise their .env was a link needs to know that
	// whether the rewrite then succeeds or fails.
	if target != path {
		fmt.Printf("  %s is a symlink; writing through it to %s\n", termsafe.Text(path), termsafe.Text(target))
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".env.tmp-*")
	if err != nil {
		return err
	}
	// Every path out of here other than a completed rename takes the temp file
	// with it, so a failed rewrite leaves no litter beside the .env. After the
	// rename the temp name no longer exists and both calls are harmless no-ops.
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	if _, err := tmp.WriteString(content); err != nil {
		return err
	}
	// Sync before the rename so a crash cannot land a file whose name is in place
	// while its bytes are not, and so a full disk is reported here rather than
	// silently truncating what the user reads back later.
	if err := tmp.Sync(); err != nil {
		return err
	}
	// Chmod before the rename: the target must never be visible under a mode it
	// was not meant to have, however briefly.
	if err := tmp.Chmod(envFileMode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// envWriteTarget is the file a .env rewrite must actually replace: path itself when
// path is a regular file or is not there at all, and the file path resolves to when
// path is a symlink.
//
// Resolving first is what stops the rewrite destroying the link. os.Rename replaces a
// *name*, so renaming the replacement over a symlink deletes the link and puts a
// regular file where it was, leaving the file it pointed at holding exactly what it
// held before. Every caller here reports success on that: `--write-env` says the
// credential was written, logout says it was removed, profile removal says the
// deployment was forgotten — and the shared credentials file, or the one file a
// monorepo's packages all point at, still carries the old key. The user believes the
// key is gone or replaced when it is neither, which is the worst of the three
// possible outcomes.
//
// A symlink is followed only while the file it resolves to stays inside the project
// (envProjectRoot). A checkout can ship a symlink, so following one that leaves the
// project would let a cloned repository decide that `blocks login --write-env` writes
// a credential to ~/.env, into a dotfiles directory, or over some unrelated file in
// the user's home — a path the user never named and will not think to look in. The
// refusal names both ends of the link, writes nothing, and leaves the user two ways
// forward (point the link inside the project, or run the command where the real file
// lives); following silently leaves them nothing, because they never learn it
// happened. Inside the project is not a new exposure: a plain .env in that same tree
// is where the credential would have gone anyway.
//
// Inside the project is necessary but not sufficient, which is why checkEnvLinkTarget
// runs as well: a checkout can commit .env as a link to any *existing* file in its own
// tree, and "in the project" alone would let it choose a tracked README or .git/config
// as the file that ends up holding the credential.
//
// A link that resolves to nothing is refused rather than treated as an absent file,
// because treating it as absent *is* the rename that destroys it.
func envWriteTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		return "", fmt.Errorf("could not inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("refusing to write %s: it is a symlink that does not resolve to a file (%w) — create the file it points at, or replace the link",
			termsafe.Text(path), err)
	}
	target := canonicalPath(resolved)
	root := envProjectRoot(filepath.Dir(path))
	if !withinDir(root, target) {
		return "", fmt.Errorf("refusing to write %s: it is a symlink to %s, which is outside the project at %s — a file holding a credential must not be written somewhere this command was not pointed at; point the link inside the project, or run this command in the directory that holds the real file",
			termsafe.Text(path), termsafe.Text(target), termsafe.Text(root))
	}
	if err := checkEnvLinkTarget(path, target); err != nil {
		return "", err
	}
	return target, nil
}

// checkEnvLinkTarget reports why a .env symlink at path must not be followed to target,
// or nil when it may be. The rule is that the link has to name the file it points at: a
// target's file name must be the link's own file name, or plain ".env", and no part of
// the path may be a ".git" directory.
//
// The containment rule this sits behind stops a checkout choosing a file in the user's
// home; it does not stop one choosing a file in the checkout. `.env` committed as a link
// to README.md, to a tracked config file, or to .git/config makes that file the one
// `blocks login --write-env` overwrites, with the API key in it — and the two facts that
// made this worth closing are that the write destroys whatever the file held and that
// nothing about the target is a place a user looks for a credential.
//
// Naming rather than a list of forbidden files, because the property wanted is positive:
// the file that ends up holding the credential should be a file whose whole purpose is
// to hold environment assignments. That is satisfied by the setup the containment rule
// exists to serve — packages/api/.env -> ../../.env, the one shared file a monorepo's
// packages all point at — and by a link that keeps its own name, such as
// .env.local -> ../../.env.local. It is not satisfied by README.md, by .git/config, or
// by a tracked .env.example, and the refusal says which end it objected to.
//
// "Tracked by git" was the other candidate and is not used. It would need a git
// invocation (or an index parse) on a path that must work in a checkout with no git
// installed and in a directory that is not a repository at all, and it would answer the
// wrong question: a .env is gitignored by convention — this CLI's own scaffold ignores
// it — so "not tracked" would wave through the tracked .env.example this rule refuses,
// while a target named README.md is wrong whether or not anyone has committed it.
func checkEnvLinkTarget(path, target string) error {
	if pathHasGitDir(target) {
		return fmt.Errorf("refusing to write %s: it is a symlink to %s, which is inside a repository's .git directory — a credential must not be written there, and the write would destroy whatever the file holds; point the link at a .env, or run this command in the directory that holds the real file",
			termsafe.Text(path), termsafe.Text(target))
	}
	if name := filepath.Base(target); name != filepath.Base(path) && name != envFileName {
		return fmt.Errorf("refusing to write %s: it is a symlink to %s, which is not a %s — a file holding a credential must not be written over some other file in the project, and the write would destroy whatever that file holds; point the link at a %s, or run this command in the directory that holds the real file",
			termsafe.Text(path), termsafe.Text(target), envFileName, envFileName)
	}
	return nil
}

// pathHasGitDir reports whether any component of path is a ".git" directory. The name
// rule alone would admit .git/.env, which is not a file anything reads and is not a
// place a user would think to look for a credential they were told was written.
func pathHasGitDir(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == gitDirName {
			return true
		}
	}
	return false
}

// envProjectRoot is the directory a .env in dir belongs to, for the single purpose of
// deciding whether a symlink stays inside the project: the enclosing git repository
// when dir sits in one, and dir itself when it does not.
//
// The repository rather than dir, because one file shared by several packages is the
// ordinary reason a .env is a symlink at all and that file sits above the package
// linking to it — a rule drawn at dir would refuse `packages/api/.env -> ../../.env`,
// which is the setup this resolution exists to keep working. A directory with no
// repository above it falls back to itself, so a project that is not a checkout still
// gets a boundary rather than none.
func envProjectRoot(dir string) string {
	root := canonicalPath(dir)
	for cur := root; ; {
		if _, err := os.Lstat(filepath.Join(cur, gitDirName)); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return root
		}
		cur = parent
	}
}

// canonicalPath returns p as an absolute path with every symlink along it resolved,
// falling back to the absolute cleaned form when it cannot be resolved. Both sides of
// the containment test go through it: a directory reached through a symlink — a temp
// dir under /var on macOS, say — is otherwise not even inside itself.
func canonicalPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// withinDir reports whether target is dir or a path beneath it. Both must already be
// canonical; see canonicalPath.
func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// envFileMode is the mode every .env this package writes ends up with. Owner-only,
// unconditionally: any of these files may hold a credential, and the alternative —
// keeping the mode of the file being replaced — hands the decision to whatever file a
// symlink resolved to, which for a tracked file in a checkout is world-readable 0644.
//
// It is not "never widen", it is "always this". A .env that was 0644 for a reason a user
// remembers is narrowed, and that is the trade: a mode a user can widen again with one
// command against a credential readable by every account on the machine.
const envFileMode os.FileMode = 0600

// envFileName is the file this package reads and writes, and the one name a .env symlink
// may point at whatever the link itself is called.
const envFileName = ".env"

// gitDirName is the directory that marks a checkout: the boundary envProjectRoot draws a
// project at, and a place no credential may be written.
const gitDirName = ".git"

// EnvLineAssignment reports the variable a .env line assigns and the value it
// assigns, or ok=false when the line assigns nothing — it is blank, commented out,
// or carries no "=" at all.
//
// The rule: the line is trimmed, a leading "#" makes it a comment, the name is the text
// before the first "=" with its surrounding space removed, and the value is the rest with
// its own surrounding space removed and one pair of matching outer quotes taken off
// (unquoteEnvValue). That is the whole reason it is worded as a parse rather than as a
// prefix test, and the reason it is exported: the writer's notion of "a line assigning
// KEY" has to be the loader's notion of it, since every guarantee this package makes — a
// key assigned exactly once, a key removed, a pin's value read — is a guarantee about
// what that loader will read back. Two spellings of the rule is what let "KEY = value"
// survive a rewrite meant to replace it, leaving a stale duplicate the loader preferred
// to the value just written.
//
// It is now the loader's own parse and not a description of it: loadEnvFile in the
// command package calls this function, which is the only way the writer's quoting and
// the loader's unquoting can be guaranteed to be the same rule rather than two rules
// that agree today.
func EnvLineAssignment(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	name, rest, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	return strings.TrimSpace(name), unquoteEnvValue(strings.TrimSpace(rest)), true
}

// envKeyNamespace prefixes every variable this CLI owns — BLOCKS_API_KEY,
// BLOCKS_BACKEND_URL, BLOCKS_CDM_URL and the rest of the settings the command layer
// resolves from its environment. It is the set of names whose spelling this package
// gets to decide, and so the boundary of the fold below.
const envKeyNamespace = "BLOCKS_"

// canonicalEnvKey folds a variable name to the one spelling this file reasons about. It
// is the same fold the command package applies to its provenance record and its
// allowlist (canonicalEnvKey in cmd/root.go), and it folds on every platform for that
// helper's reason: a Windows-only fold goes unexercised on the platform this code is
// developed and tested on, which is how it rots.
func canonicalEnvKey(key string) string { return strings.ToUpper(key) }

// sameEnvVariable reports whether two names in a .env name the same variable. Every
// read and every write in this file pairs a line with a key through it, so a line this
// file would rewrite is exactly a line it would remove and exactly a line it would read.
//
// Names in this CLI's own namespace are compared case-insensitively; every other name is
// compared byte for byte. A Windows process environment is case-insensitive — Windows is
// a shipped platform, see run_windows.go — so a .env holding both blocks_api_key=stale
// and BLOCKS_API_KEY=new holds one variable there while a byte comparison sees two. A
// writer that saw two would leave the other spelling behind: setEnvAssignment's collapse
// would not reach it, so "assigned exactly once" would be false exactly where the
// invariant matters, and dropEnvAssignments would report a credential removed while a
// live one survived under the other spelling — which is the sentence `blocks logout` and
// `blocks profile remove` print.
//
// It is bounded to this CLI's namespace because that is the only claim being defended.
// On Unix FOO and foo really are two variables and a project's .env may hold both on
// purpose; folding every name would let a write of one delete the other and a removal of
// one take the other with it, which is not this package's to do to a name it does not
// own. Inside BLOCKS_* nothing legitimate is lost, because every consumer — this CLI, the
// scaffolded Node and Python agents, the deploy plugins — reads these variables under
// their canonical spelling, so another spelling of one is at best inert and at worst the
// stale duplicate above.
func sameEnvVariable(a, b string) bool {
	if a == b {
		return true
	}
	canonical := canonicalEnvKey(a)
	return strings.HasPrefix(canonical, envKeyNamespace) && canonical == canonicalEnvKey(b)
}

// EnvLineMatchesKey returns true if the line assigns the given variable, as the .env
// loader reads it: key="FOO" matches "FOO=bar", "FOO = bar" and "  FOO=bar", but not
// "FOO_EXTRA=bar" or "# FOO=bar". Which names count as the same variable is
// sameEnvVariable's answer, so a variable this CLI owns is matched under any spelling of
// its name and any other variable is matched under its own spelling alone.
func EnvLineMatchesKey(line, key string) bool {
	name, _, ok := EnvLineAssignment(line)
	return ok && sameEnvVariable(key, name)
}

// EnvFileValue reports the value assigned to key in the .env file at path, and ""
// when there is no such file or it carries no uncommented assignment for that key.
// It shares EnvLineAssignment with UpsertEnvKey and RemoveEnvKey, so a line those
// two would rewrite or delete is exactly a line this one reads: a caller deciding
// whether to remove an assignment cannot disagree with the removal about which
// line it meant.
//
// The value is the one the loader would end up with: the first non-empty assignment
// of the key, since the loader assigns nothing for an empty value and so goes on to
// the next line. An empty assignment is therefore not an answer here either.
//
// "The key" is sameEnvVariable's key, so for a variable this CLI owns the first non-empty
// assignment under *any* spelling of its name is the answer. On Windows that is what the
// loader itself reads, since the spellings are one variable there. On Unix a file holding
// two spellings can make this differ from the loader by one line, and the difference falls
// the safe way round: the caller asking the question is deciding whether the .env still
// carries a credential or a pin, and an answer drawn from a spelling the loader would not
// have used leads to removing that line rather than to leaving it — and the removal drops
// every spelling, so nothing is left behind either way.
//
// A file that exists and cannot be read is an error rather than an empty value,
// because these two answers lead callers to opposite actions: "no assignment" means
// leave the file alone and say so, while "unreadable" means the caller does not know
// what the file targets and must not claim to have changed it.
func EnvFileValue(path, key string) (string, error) {
	lines, _, err := readEnvFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range lines {
		if name, value, ok := EnvLineAssignment(line); ok && sameEnvVariable(key, name) && value != "" {
			return value, nil
		}
	}
	return "", nil
}

// UpsertEnvKey reads the .env file line-by-line and leaves key assigned value
// exactly once — replacing a matching KEY= line in place, dropping any duplicate of
// it, or appending the key if it was not there. Preserves all comments, spacing, and
// original key ordering.
func UpsertEnvKey(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines, _, _, err := applyEnvMutations(splitEnvLines(string(data)), []EnvMutation{{Key: key, Value: value}})
	if err != nil {
		return err
	}
	return replaceEnvFile(path, strings.Join(lines, "\n"))
}

// RemoveEnvKey removes all lines matching the given key from the .env file. No such
// file is nothing to remove; a file that cannot be read, and a rewrite that fails,
// are both reported as errors, because a caller that goes on to announce the removal
// — or to write a credential that depended on it — must not do so on a file that
// still carries the line.
func RemoveEnvKey(path, key string) error {
	_, err := RemoveEnvKeys(path, key)
	return err
}

// RemoveEnvKeys removes all lines matching any of the given keys from the .env file
// in a single rewrite, and reports which of them were actually assigned there. The
// keys go together because a caller that must forget several facts at once — the
// deployment URL and the CDM endpoint of one deployment, say — would otherwise be
// able to drop one and fail on the next, leaving half a target behind. Its failure
// and its report are the caller's, since the sentence a caller prints afterwards has
// to name what was removed rather than what it asked to remove.
func RemoveEnvKeys(path string, keys ...string) ([]string, error) {
	existing, exists, err := readEnvFile(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	mutations := make([]EnvMutation, 0, len(keys))
	for _, key := range keys {
		mutations = append(mutations, EnvMutation{Key: key, Remove: true})
	}
	lines, _, removed, err := applyEnvMutations(existing, mutations)
	if err != nil {
		return nil, err
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := replaceEnvFile(path, strings.Join(lines, "\n")); err != nil {
		return nil, err
	}
	return removed, nil
}
