package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/cdm"
)

// internalRefRe matches any internal tracker ID. Customer-facing output must
// never contain one.
var internalRefRe = regexp.MustCompile(`BLOCKS-\d+`)

// validateCardWithLegacyShim is the shared entry used by `blocks run`,
// `blocks check`, and `blocks publish`. The legacy-skills path must succeed
// with a warning so the local dev loop is not broken by a stale card; the
// no-skills path must remain quiet and produce no warning. This is the
// minimum guarantee `run` / `check` rely on for parity with `publish`.
func TestValidateCardWithLegacyShimSurfacesWarning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyCard := map[string]interface{}{
		"identity": map[string]interface{}{
			"agentName":   "legacy_agent",
			"displayName": "Legacy Agent",
			"description": "Old card with skills field",
			"version":     "1.0.0",
			"provider":    map[string]interface{}{"organization": "TestOrg"},
		},
		"capabilities": map[string]interface{}{"taskKinds": []interface{}{"request"}},
		"skills": []interface{}{
			map[string]interface{}{"id": "main", "name": "Main", "description": "old"},
		},
		"runtime": map[string]interface{}{"handler": "./handler.py"},
	}
	cardBytes, _ := json.MarshalIndent(legacyCard, "", "  ")
	cardPath := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(cardPath, cardBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	oldStderr := os.Stderr
	rPipe, wPipe, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	os.Stderr = wPipe

	res := validateCardWithLegacyShim(cardPath)

	_ = wPipe.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, rPipe)
	stderr := stderrBuf.String()

	if len(res.Errors) != 0 {
		t.Fatalf("expected legacy card to validate cleanly after shim, got errors: %v", res.Errors)
	}
	if !strings.Contains(stderr, "DEPRECATED") {
		t.Errorf("stderr must mark the warning as DEPRECATED so customers know to act; got=%q", stderr)
	}
	if !strings.Contains(stderr, "skills") || !strings.Contains(stderr, "tags") {
		t.Errorf("stderr must mention both skills and tags so the action is obvious; got=%q", stderr)
	}
	if !strings.Contains(stderr, "Action") {
		t.Errorf("stderr should call out an explicit Action: line; got=%q", stderr)
	}
	// Internal-only references must not leak to customers (Jira IDs, internal URLs).
	if internalRefRe.MatchString(stderr) || strings.Contains(stderr, "atlassian") {
		t.Errorf("stderr leaked an internal reference to the customer; got=%q", stderr)
	}

	rt, _ := res.Card["runtime"].(map[string]interface{})
	handler, _ := rt["handler"].(string)
	if handler != "./handler.py" {
		t.Errorf("expected handler path to round-trip through the shim; got=%q", handler)
	}

	// Source file MUST NOT be mutated — same on-disk invariant as publish.
	postBytes, _ := os.ReadFile(cardPath)
	var postCard map[string]interface{}
	_ = json.Unmarshal(postBytes, &postCard)
	if _, stillHasSkills := postCard["skills"]; !stillHasSkills {
		t.Errorf("source file was mutated; the shim must operate in-memory only")
	}
}

// preprocessAgentCard table-driven coverage for the four input shapes
// the shim handles (skills-only, tags-only, both, neither).
func TestPreprocessAgentCard(t *testing.T) {
	cases := []struct {
		name           string
		input          string
		wantTagsValue  string // JSON-encoded expected `tags` array; empty = absent
		wantSkillsKey  bool   // whether `skills` should remain in output
		wantStderrHas  string // substring expected on stderr ("" means no warning)
		wantStderrMiss string // substring that MUST NOT appear on stderr
	}{
		{
			name:           "skills-only is renamed and warned",
			input:          `{"identity":{"agentName":"a"},"skills":[{"id":"x","name":"X"}]}`,
			wantTagsValue:  `[{"id":"x","name":"X"}]`,
			wantSkillsKey:  false,
			wantStderrHas:  "DEPRECATED",
			wantStderrMiss: "both",
		},
		{
			name:           "tags-only is unchanged and quiet",
			input:          `{"identity":{"agentName":"a"},"tags":[{"id":"y","name":"Y"}]}`,
			wantTagsValue:  `[{"id":"y","name":"Y"}]`,
			wantSkillsKey:  false,
			wantStderrHas:  "",
			wantStderrMiss: "DEPRECATED",
		},
		{
			name:           "both present: keep tags, drop skills, warn about conflict",
			input:          `{"identity":{"agentName":"a"},"tags":[{"id":"k","name":"Keep"}],"skills":[{"id":"d","name":"Drop"}]}`,
			wantTagsValue:  `[{"id":"k","name":"Keep"}]`,
			wantSkillsKey:  false,
			wantStderrHas:  "both",
			wantStderrMiss: "DEPRECATED",
		},
		{
			name:           "neither present is unchanged and quiet",
			input:          `{"identity":{"agentName":"a"}}`,
			wantTagsValue:  "",
			wantSkillsKey:  false,
			wantStderrHas:  "",
			wantStderrMiss: "DEPRECATED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			out, err := preprocessAgentCard([]byte(tc.input), "/tmp/agent-card.json", &stderr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Stability contract: when no rewrite is needed (tags-only or
			// neither-present), output bytes MUST equal input bytes — callers
			// that compare for "did the preprocessor touch this card?" rely
			// on this. The skills-present path is intentionally allowed to
			// reformat (see godoc on preprocessAgentCard).
			if tc.name == "tags-only is unchanged and quiet" || tc.name == "neither present is unchanged and quiet" {
				if !bytes.Equal(out, []byte(tc.input)) {
					t.Errorf("expected byte-equal output for %q, got reformatted bytes\nin =%s\nout=%s", tc.name, tc.input, out)
				}
			}

			var card map[string]interface{}
			if jsonErr := json.Unmarshal(out, &card); jsonErr != nil {
				t.Fatalf("preprocessor produced invalid JSON: %v\nout=%s", jsonErr, out)
			}

			_, hasSkills := card["skills"]
			if hasSkills != tc.wantSkillsKey {
				t.Errorf("skills-key presence: got=%v want=%v\nout=%s", hasSkills, tc.wantSkillsKey, out)
			}

			if tc.wantTagsValue == "" {
				if _, hasTags := card["tags"]; hasTags {
					t.Errorf("expected no tags key; got %v", card["tags"])
				}
			} else {
				tagsBytes, _ := json.Marshal(card["tags"])
				if string(tagsBytes) != tc.wantTagsValue {
					t.Errorf("tags mismatch: got=%s want=%s", tagsBytes, tc.wantTagsValue)
				}
			}

			stderrStr := stderr.String()
			if tc.wantStderrHas != "" && !strings.Contains(stderrStr, tc.wantStderrHas) {
				t.Errorf("stderr missing %q: got=%q", tc.wantStderrHas, stderrStr)
			}
			if tc.wantStderrHas == "" && stderrStr != "" {
				t.Errorf("expected no stderr; got=%q", stderrStr)
			}
			if tc.wantStderrMiss != "" && strings.Contains(stderrStr, tc.wantStderrMiss) {
				t.Errorf("stderr unexpectedly contains %q: got=%q", tc.wantStderrMiss, stderrStr)
			}
			// No customer-visible warning may leak internal references.
			if internalRefRe.MatchString(stderrStr) || strings.Contains(stderrStr, "atlassian") {
				t.Errorf("stderr leaked an internal reference; got=%q", stderrStr)
			}
		})
	}
}

func TestPreprocessAgentCardInvalidJSON(t *testing.T) {
	var stderr bytes.Buffer
	out, err := preprocessAgentCard([]byte("{not-json"), "/tmp/agent-card.json", &stderr)
	if err == nil {
		t.Fatalf("expected an error for invalid JSON, got nil")
	}
	if !bytes.Equal(out, []byte("{not-json")) {
		t.Errorf("invalid-JSON case must echo the raw bytes back to the caller so downstream validation can produce a single canonical error; got=%s", out)
	}
}

// End-to-end exercise of `blocks publish` against an on-disk agent-card.json
// that still uses the deprecated `skills` field. The publish should succeed,
// the backend should receive a card with `tags` (not `skills`), and stderr
// should carry the legacy-field deprecation warning.
func TestPublishRewritesLegacySkillsField(t *testing.T) {
	cleanup := setupFakeCredentials(t)
	defer cleanup()

	var received map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "handler.py"), []byte("# handler"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyCard := map[string]interface{}{
		"identity": map[string]interface{}{
			"agentName":   "legacy_agent",
			"displayName": "Legacy Agent",
			"description": "Old card with skills field",
			"version":     "1.0.0",
			"provider":    map[string]interface{}{"organization": "TestOrg"},
		},
		"capabilities": map[string]interface{}{"taskKinds": []interface{}{"request"}},
		"skills": []interface{}{
			map[string]interface{}{"id": "main", "name": "Main", "description": "old"},
		},
		"runtime": map[string]interface{}{"handler": "./handler.py"},
	}
	cardBytes, _ := json.MarshalIndent(legacyCard, "", "  ")
	cardPath := filepath.Join(dir, "agent-card.json")
	if err := os.WriteFile(cardPath, cardBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("BLOCKS_BACKEND_URL", ts.URL)
	t.Setenv("BLOCKS_APP_BASE_URL", "")
	t.Setenv("BLOCKS_DASHBOARD_URL", "")
	t.Setenv("BLOCKS_CDM_URL", "http://127.0.0.1:1/nonexistent")
	cdm.Reset()

	oldDir, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(oldDir) }()

	resetPublishFlags()

	// Capture stderr (the warning is written there). os.Stderr is overridden
	// only for the duration of the publish call.
	oldStderr := os.Stderr
	rPipe, wPipe, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	os.Stderr = wPipe

	captureStdout(func() {
		rootCmd.SetArgs([]string{"publish", cardPath, "--listing", "public", "--billing-mode", "free", "--accept-terms"})
		if err := rootCmd.Execute(); err != nil {
			os.Stderr = oldStderr
			_ = wPipe.Close()
			t.Fatalf("publish failed: %v", err)
		}
	})

	_ = wPipe.Close()
	os.Stderr = oldStderr
	var stderrBuf bytes.Buffer
	_, _ = io.Copy(&stderrBuf, rPipe)
	stderr := stderrBuf.String()

	if !strings.Contains(stderr, "DEPRECATED") {
		t.Errorf("stderr missing DEPRECATED marker:\n%s", stderr)
	}
	if !strings.Contains(stderr, "skills") || !strings.Contains(stderr, "tags") {
		t.Errorf("stderr should mention both skills and tags to be actionable:\n%s", stderr)
	}
	if internalRefRe.MatchString(stderr) || strings.Contains(stderr, "atlassian") {
		t.Errorf("stderr leaked an internal reference to the customer:\n%s", stderr)
	}

	card, ok := received["card"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing card; payload=%v", received)
	}
	if _, hasSkills := card["skills"]; hasSkills {
		t.Errorf("backend received `skills` on the wire — preprocessor failed to drop it. card=%v", card)
	}
	tags, hasTags := card["tags"]
	if !hasTags {
		t.Errorf("backend should have received `tags`. card=%v", card)
	} else if tagsArr, ok := tags.([]interface{}); !ok || len(tagsArr) != 1 {
		t.Errorf("expected one tag carried over from `skills`. got=%v", tags)
	}

	// The source file on disk must NOT be mutated — the shim is in-memory only.
	postBytes, err := os.ReadFile(cardPath)
	if err != nil {
		t.Fatal(err)
	}
	var postCard map[string]interface{}
	_ = json.Unmarshal(postBytes, &postCard)
	if _, stillHasSkills := postCard["skills"]; !stillHasSkills {
		t.Errorf("source file was mutated; the preprocessor must operate in-memory only")
	}
}
