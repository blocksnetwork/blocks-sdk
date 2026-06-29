package scaffold

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
)

// updateGolden controls whether the test rewrites the golden files on
// disk instead of asserting equality. Run `go test ./internal/scaffold/...
// -update` after intentional generator output changes.
var updateGolden = flag.Bool("update", false, "rewrite testdata/embed/golden/* fixtures from current generator output")

// fixturePath resolves to the cardfetch testdata directory so we can re-use
// the same wrapped-response fixtures for both packages.
func fixturePath(name string) string {
	return filepath.Join("..", "cardfetch", "testdata", name+".json")
}

// loadFixtureCard spins up an httptest.Server returning the wrapped
// envelope for the given fixture name and parses it via cardfetch.Fetch.
// This guarantees the generator tests exercise the real Fetch parser
// (no hand-built struct shortcuts).
func loadFixtureCard(t *testing.T, fixtureName, agentName string, status int) *cardfetch.AgentCard {
	t.Helper()
	body, err := os.ReadFile(fixturePath(fixtureName))
	if err != nil {
		t.Fatalf("read fixture %q: %v", fixtureName, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	client := blocksapi.NewClient(srv.URL, "test-key")
	card, err := cardfetch.Fetch(context.Background(), client, agentName)
	if err != nil {
		t.Fatalf("Fetch %q: %v", agentName, err)
	}
	return card
}

func defaultVars() EmbedVars {
	return EmbedVars{
		WidgetVersion:      "0.1.0",
		BlocksAssetBaseUrl: "https://blocks.ai",
		CardSnapshotDate:   "2026-05-09",
	}
}

// assertOrUpdateGolden compares actual against the golden file. With
// -update the file is rewritten instead.
func assertOrUpdateGolden(t *testing.T, goldenDir, name, actual string) {
	t.Helper()
	if err := os.MkdirAll(goldenDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", goldenDir, err)
	}
	path := filepath.Join(goldenDir, name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(actual), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run with -update to create): %v", path, err)
	}
	if string(want) != actual {
		t.Errorf("%s mismatch — run `go test ./internal/scaffold/... -update` to refresh.\n--- want ---\n%s\n--- got ---\n%s", path, string(want), actual)
	}
}

// generateAndAssert runs GenerateApp and compares all four files against
// the golden directory.
func generateAndAssert(t *testing.T, goldenSubdir string, cards []*cardfetch.AgentCard, vars EmbedVars) AppFiles {
	t.Helper()
	out, err := GenerateApp(cards, vars)
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}
	dir := filepath.Join("testdata", "embed", "golden", goldenSubdir)
	assertOrUpdateGolden(t, dir, "app.js", out.AppJS)
	assertOrUpdateGolden(t, dir, "index.html", out.IndexHTML)
	assertOrUpdateGolden(t, dir, "styles.css", out.StylesCSS)
	assertOrUpdateGolden(t, dir, "README.md", out.ReadmeMD)
	return out
}

// ----------------------------------------------------------------------
// Per-fixture golden tests (R6.1)
// ----------------------------------------------------------------------

func TestGenerator_Echo2_FormInput_TextOutput(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_echo"
	out := generateAndAssert(t, "echo2", []*cardfetch.AgentCard{card}, vars)

	// Behavioural assertions on app.js (independent of golden):
	mustContain(t, out.AppJS, `partId: "text"`, `expected partId 'text' from echo2 io.inputs[0].id`)
	mustNotContain(t, out.AppJS, `partId: "request"`, `echo2 doesn't declare an input id 'request'`)
	mustContain(t, out.AppJS, `JSON.parse(ta_0.value)`, `form-class input must validate JSON before send`)
	mustContain(t, out.AppJS, `text: ta_0.value`, `form-class wire shape: text-of-textarea`)
	mustContain(t, out.AppJS, `session.onArtifact(`, `outputs must be dispatched via onArtifact`)
	// Single-output card: no switch (route every artifact to the only output).
	mustContain(t, out.AppJS, `Card declares exactly 1 output`, `single-output collapse comment`)
	mustNotContain(t, out.AppJS, `session.onStream(`, `echo2 declares no streams`)
	mustContain(t, out.AppJS, `BlocksAuth.signOut(`, `sign-out must call BlocksAuth.signOut()`)
	mustContain(t, out.AppJS, `instanceof BlocksAuth.BlocksAuthError`, `widget-level error branch`)
	mustContain(t, out.AppJS, `err.data.code === 'InsufficientBalance'`, `paid-agent error branch`)
}

func TestGenerator_Stest1_OutboundEventsStream(t *testing.T) {
	card := loadFixtureCard(t, "stest1", "stest1", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_stest"
	out := generateAndAssert(t, "stest1", []*cardfetch.AgentCard{card}, vars)

	mustContain(t, out.AppJS, `partId: "request"`, `stest1's input id is literally 'request'`)
	mustContain(t, out.AppJS, `session.onStream(`, `stest1 declares an outbound stream`)
	mustContain(t, out.AppJS, `descriptor.declaredStream !== "_default"`, `stream filter must use declaredStream === card-key`)
	mustNotContain(t, out.AppJS, `descriptor.streamId`, `must not filter on streamId (runtime instance id)`)
	mustContain(t, out.AppJS, `for await (const ev of stream.events())`, `events-format stream uses .events()`)
}

func TestGenerator_PrivateMe_PrivateCard(t *testing.T) {
	card := loadFixtureCard(t, "private_me", "private_me", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_private"
	out := generateAndAssert(t, "private_me", []*cardfetch.AgentCard{card}, vars)

	mustNotContain(t, out.AppJS, `session.onStream(`, `private_me declares no streams`)
	mustContain(t, out.AppJS, `partId: "request"`, `private_me's input id is 'request'`)
}

func TestGenerator_MultiAgent_Echo2_Stest1(t *testing.T) {
	c1 := loadFixtureCard(t, "echo2", "echo2", 200)
	c2 := loadFixtureCard(t, "stest1", "stest1", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_multi"
	out := generateAndAssert(t, "multi_echo2_stest1", []*cardfetch.AgentCard{c1, c2}, vars)

	mustContain(t, out.AppJS, `signInAndGetClients`, `multi-agent must use signInAndGetClients`)
	mustContain(t, out.AppJS, `agents: ["echo2", "stest1"]`, `multi-agent flag-order list`)
	mustContain(t, out.AppJS, `=== Agent: echo2 ===`, `per-agent block for echo2`)
	mustContain(t, out.AppJS, `=== Agent: stest1 ===`, `per-agent block for stest1`)
	mustContain(t, out.IndexHTML, `id="section-echo2"`, `echo2 section in HTML`)
	mustContain(t, out.IndexHTML, `id="section-stest1"`, `stest1 section in HTML`)
}

func TestGenerator_TextInput_RawWireShape(t *testing.T) {
	card := loadFixtureCard(t, "text_input", "text_input", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_text"
	out := generateAndAssert(t, "text_input", []*cardfetch.AgentCard{card}, vars)

	// text class → raw text:, no JSON.stringify, no JSON.parse validation.
	mustContain(t, out.AppJS, `text: ta_0.value`, `text-class wire shape: raw textarea value`)
	mustNotContain(t, out.AppJS, `JSON.parse(ta_0.value)`, `text-class must NOT validate JSON`)
}

func TestGenerator_BinaryInput_FilePart(t *testing.T) {
	card := loadFixtureCard(t, "binary_input", "binary_input", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_binary"
	out := generateAndAssert(t, "binary_input", []*cardfetch.AgentCard{card}, vars)

	mustContain(t, out.IndexHTML, `<input type="file"`, `file-class input → <input type=file>`)
	mustContain(t, out.AppJS, `file: file_0`, `file-class wire shape uses file: field`)
	mustContain(t, out.AppJS, `partId: "blob"`, `partId derived from input.id`)
	// outputs: image/png → <img>, application/pdf → download link
	mustContain(t, out.AppJS, `document.createElement('img')`, `image/png output renders <img>`)
	mustContain(t, out.AppJS, `document.createElement('a')`, `pdf output renders download link`)
}

func TestGenerator_UnknownInput_DefensiveFallback(t *testing.T) {
	card := loadFixtureCard(t, "unknown_input", "unknown_input", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_unknown"
	out := generateAndAssert(t, "unknown_input", []*cardfetch.AgentCard{card}, vars)

	mustContain(t, out.AppJS, `unrecognized contentType "application/x-acme-blob"`, `unknown-input TODO comment`)
	mustContain(t, out.AppJS, `text: ta_0.value`, `unknown-class fallback uses JSON-encoded text default`)
}

func TestGenerator_InboundStream_TodoStub(t *testing.T) {
	card := loadFixtureCard(t, "inbound_stream", "inbound_stream", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_inbound"
	out := generateAndAssert(t, "inbound_stream", []*cardfetch.AgentCard{card}, vars)

	mustContain(t, out.AppJS, `inbound stream "uploads"`, `inbound stream → TODO stub`)
	mustNotContain(t, out.AppJS, `session.onStream(`, `no consumer code for inbound streams`)
}

func TestGenerator_AboveCeiling(t *testing.T) {
	card := loadFixtureCard(t, "complex_multi_input", "complex_multi_input", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_complex"
	out := generateAndAssert(t, "complex_multi_input", []*cardfetch.AgentCard{card}, vars)

	// First 5 inputs wired, in6 listed as TODO.
	mustContain(t, out.AppJS, `partId: "in1"`, `first input wired`)
	mustContain(t, out.AppJS, `partId: "in5"`, `fifth input wired`)
	mustNotContain(t, out.AppJS, `partId: "in6"`, `sixth input is over the ceiling`)
	mustContain(t, out.AppJS, `- in6 (application/json)`, `over-ceiling input listed as TODO`)
	// Outputs: out4 over the ceiling. Multi-output → switch dispatch.
	mustContain(t, out.AppJS, `case "out1":`, `first output wired`)
	mustNotContain(t, out.AppJS, `case "out4":`, `fourth output is over the ceiling`)
	// Streams: third over the ceiling.
	mustContain(t, out.AppJS, `descriptor.declaredStream !== "events_a"`, `first stream wired`)
	mustContain(t, out.AppJS, `descriptor.declaredStream !== "events_b"`, `second stream wired`)
	mustNotContain(t, out.AppJS, `descriptor.declaredStream !== "events_c"`, `third stream is over the ceiling`)
	// Shared affinity note for events_b.
	mustContain(t, out.AppJS, `shared-affinity stream`, `shared-affinity comment`)
}

func TestGenerator_PipeOnly(t *testing.T) {
	card := loadFixtureCard(t, "pipe_only", "pipe_only", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_pipe"
	out := generateAndAssert(t, "pipe_only", []*cardfetch.AgentCard{card}, vars)

	// Pipe-only → taskKind 'pipe' with a duration read from the page input.
	mustContain(t, out.AppJS, `taskKind: "pipe"`, `pipe-only agent → taskKind='pipe'`)
	mustContain(t, out.AppJS, `readPipeDuration(document.getElementById("duration-pipe_only"))`, `pipe-only reads duration from the input`)
	mustContain(t, out.AppJS, `duration,`, `pipe-only sends the duration field`)
	mustNotContain(t, out.AppJS, `duration: 1`, `hardcoded duration stub is gone`)
	// HTML carries the duration input but no pipe toggle (pipe-only has no choice).
	mustContain(t, out.IndexHTML, `id="duration-pipe_only"`, `duration input rendered`)
	mustContain(t, out.IndexHTML, `max="43200"`, `duration input bounds the SDK range`)
	mustNotContain(t, out.IndexHTML, `id="pipe-toggle-pipe_only"`, `pipe-only has no request/pipe toggle`)
}

func TestGenerator_RequestPipe_Mixed(t *testing.T) {
	card := loadFixtureCard(t, "request_pipe", "request_pipe", 200)
	vars := defaultVars()
	vars.ProjectName = "demo_mixed"
	out := generateAndAssert(t, "request_pipe", []*cardfetch.AgentCard{card}, vars)

	// Mixed → a checkbox selects the mode; duration is sent only in pipe mode.
	mustContain(t, out.IndexHTML, `id="pipe-toggle-request_pipe"`, `mixed agent renders the pipe toggle`)
	mustContain(t, out.IndexHTML, `id="duration-request_pipe"`, `mixed agent renders the duration input`)
	mustContain(t, out.AppJS, `document.getElementById("pipe-toggle-request_pipe").checked`, `mode is read from the checkbox`)
	mustContain(t, out.AppJS, `taskKind: asPipe ? "pipe" : "request"`, `taskKind switches on the checkbox`)
	mustContain(t, out.AppJS, `if (asPipe) message.duration = readPipeDuration(`, `duration sent only in pipe mode`)
}

// ----------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------

func mustContain(t *testing.T, haystack, needle, why string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected %q in output (%s)", needle, why)
	}
}

func mustNotContain(t *testing.T, haystack, needle, why string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected %q NOT in output (%s)", needle, why)
	}
}

// ----------------------------------------------------------------------
// Empty-cards guard
// ----------------------------------------------------------------------

func TestGenerator_EmptyCards(t *testing.T) {
	if _, err := GenerateApp(nil, defaultVars()); err == nil {
		t.Errorf("expected error for nil cards")
	}
	if _, err := GenerateApp([]*cardfetch.AgentCard{}, defaultVars()); err == nil {
		t.Errorf("expected error for empty cards")
	}
	if _, err := GenerateApp([]*cardfetch.AgentCard{nil}, defaultVars()); err == nil {
		t.Errorf("expected error for nil entry")
	}
}

// ----------------------------------------------------------------------
// Behavioural assertions on cross-cutting concerns
// ----------------------------------------------------------------------

// TestGenerator_BlocksErrorMessage_Helper verifies the centralized error-
// mapping helper exists and is referenced by both the sign-in and per-send
// catches. The InsufficientBalance branch lives in one place so paid-agent
// errors surface from SendMessage failures (the common case), not just
// sign-in. Bypasses generateAndAssert (no golden diff needed).
func TestGenerator_BlocksErrorMessage_Helper(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	mustContain(t, out.AppJS, `function blocksErrorMessage(err)`, `helper definition`)
	mustContain(t, out.AppJS, `'InsufficientBalance'`, `paid-agent branch in helper`)
	mustContain(t, out.AppJS, `authError.textContent = blocksErrorMessage(err)`, `sign-in catch uses helper`)
	mustContain(t, out.AppJS, `outputEl.textContent += '\n[' + blocksErrorMessage(err) + ']'`, `per-send catch uses helper`)
	// The InsufficientBalance branch must live in the helper only — exactly
	// one occurrence in the file. Multiple occurrences would mean the catch
	// sites still inline the branch instead of delegating.
	if got := strings.Count(out.AppJS, `'InsufficientBalance'`); got != 1 {
		t.Errorf("expected exactly 1 occurrence of 'InsufficientBalance' (helper only), got %d", got)
	}
}

// TestEmbed_NoDocumentWrite asserts the scaffold never injects the widget via
// document.write (Chrome blocks parser-blocking cross-site document.write
// scripts, which left window.BlocksAuth undefined on deployed static hosts).
func TestEmbed_NoDocumentWrite(t *testing.T) {
	card := loadFixtureCard(t, "stest1", "stest1", 200)
	files, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}
	if strings.Contains(files.IndexHTML, "document.write") {
		t.Error("index.html must not use document.write to inject the widget")
	}
	if !strings.Contains(files.IndexHTML, "document.createElement('script')") {
		t.Error("index.html should inject the widget via a created <script> element")
	}
	if !strings.Contains(files.AppJS, "typeof BlocksAuth === 'undefined'") {
		t.Error("blocksErrorMessage should guard against a missing BlocksAuth widget")
	}
}

// TestGenerator_AppJS_WaitsForWidgetReady verifies app.js does not assume the
// widget is already loaded. The deferred app.js can run before the dynamically
// injected cross-origin widget defines window.BlocksAuth, so both sign-in and
// auto-resume must wait for readiness (otherwise auto-resume silently never
// fires and an early click shows a false "widget failed to load").
func TestGenerator_AppJS_WaitsForWidgetReady(t *testing.T) {
	card := loadFixtureCard(t, "stest1", "stest1", 200)
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}
	mustContain(t, out.AppJS, `function whenBlocksAuthReady(`, `readiness helper defined`)
	// attemptSignIn awaits readiness before deciding the widget failed.
	mustContain(t, out.AppJS, `await whenBlocksAuthReady(`, `attemptSignIn awaits widget readiness`)
	// Auto-resume runs only after the widget is ready, so it is not a one-shot
	// false-negative when app.js wins the race.
	mustContain(t, out.AppJS, `whenBlocksAuthReady(WIDGET_READY_TIMEOUT_MS).then(`, `auto-resume gated on readiness`)
	// The timeout path still surfaces a user-visible message.
	if strings.Count(out.AppJS, `The Blocks auth widget failed to load`) < 1 {
		t.Error("expected a 'widget failed to load' message on the timeout path")
	}
}

// TestGenerator_RenderedRefs_Tracking verifies the post-terminal sweep
// uses a Set to track rendered refs (skipping live-rendered ones) rather
// than the brittle artifactsRendered === 0 gate. This catches the
// preloaded-history-artifact race where a preload + live mix would
// previously skip the preload.
func TestGenerator_RenderedRefs_Tracking(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	mustContain(t, out.AppJS, `const renderedRefs = new Set()`, `Set declaration`)
	mustContain(t, out.AppJS, `if (renderedRefs.has(event.artifactRef)) return`, `onArtifact idempotency`)
	mustContain(t, out.AppJS, `renderedRefs.add(event.artifactRef)`, `onArtifact populates set`)
	mustContain(t, out.AppJS, `if (renderedRefs.has(ref)) continue`, `post-terminal sweep skips already-rendered`)
	mustContain(t, out.AppJS, `renderedRefs.add(ref)`, `post-terminal sweep records refs`)
	// Old gate must be gone — we always sweep, just skip rendered refs.
	mustNotContain(t, out.AppJS, `artifactsRendered`, `stale counter must not survive`)
}

// TestGenerator_ClaimRefBeforeAwait verifies the double-render race fix:
// renderedRefs.add(...) must execute BEFORE any await on downloadArtifact,
// in both the onArtifact callback and the post-terminal sweep. The SDK
// invokes artifact callbacks synchronously and ignores the returned
// promise (task-session.ts), so a terminal event mid-await would let the
// sweep re-enter the same ref if we hadn't claimed it yet.
func TestGenerator_ClaimRefBeforeAwait(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	// Helper: assert that the first occurrence of `add` precedes the
	// first occurrence of `await downloadArtifact` in `region`.
	assertClaimBeforeAwait := func(region, addNeedle, awaitNeedle, label string) {
		t.Helper()
		addAt := strings.Index(region, addNeedle)
		awaitAt := strings.Index(region, awaitNeedle)
		if addAt < 0 || awaitAt < 0 {
			t.Errorf("%s: missing needles (add=%d, await=%d)", label, addAt, awaitAt)
			return
		}
		if addAt > awaitAt {
			t.Errorf("%s: renderedRefs.add must precede await downloadArtifact (add@%d, await@%d)", label, addAt, awaitAt)
		}
	}

	// onArtifact preamble
	onArtIdx := strings.Index(out.AppJS, "session.onArtifact(")
	if onArtIdx < 0 {
		t.Fatal("expected session.onArtifact in output")
	}
	// Slice from onArtifact through the end of the callback (rough; the
	// add/await are both very close to each other).
	onArtRegion := out.AppJS[onArtIdx:]
	assertClaimBeforeAwait(onArtRegion,
		`renderedRefs.add(event.artifactRef)`,
		`await session.downloadArtifact(event.artifactRef)`,
		"onArtifact preamble")

	// Post-terminal sweep
	sweepIdx := strings.Index(out.AppJS, "for (const ref of session.listArtifacts())")
	if sweepIdx < 0 {
		t.Fatal("expected listArtifacts sweep in output")
	}
	sweepRegion := out.AppJS[sweepIdx:]
	assertClaimBeforeAwait(sweepRegion,
		`renderedRefs.add(ref)`,
		`await session.downloadArtifact(ref)`,
		"post-terminal sweep")
}

// TestGenerator_JSCommentInjectionResistance verifies that newlines and
// JS line-terminator characters in card-derived ids/keys are sanitized
// before being interpolated into // line comments. Without this, a
// malicious or buggy card id like "foo\nalert(1);//" would terminate
// the comment and inject executable JS into the generated app.js.
func TestGenerator_JSCommentInjectionResistance(t *testing.T) {
	// Synthesize a card whose ids and keys carry every flavor of JS
	// line terminator. The card schema only requires non-empty strings
	// for these fields, so this is a reachable input.
	card := &cardfetch.AgentCard{
		AgentName: "evil",
		TaskKinds: []string{"request"},
		Inputs: []cardfetch.InputDecl{
			{ID: "in_lf\nalert('lf');//", ContentType: "text/plain", Required: true},
		},
		Outputs: []cardfetch.OutputDecl{
			{ID: "out_cr\ralert('cr');//", ContentType: "text/plain", Guaranteed: true},
		},
		Streams: map[string]cardfetch.StreamDecl{
			"stream_ls alert('ls');//": {Direction: "outbound", Format: "events"},
		},
	}
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, defaultVars())
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	// The tail of any injection — the alert() — must not appear at the
	// start of a line in app.js. We scan all lines for ones that begin
	// with `alert(`, which would mean a comment was terminated and the
	// payload became live code.
	for i, line := range strings.Split(out.AppJS, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "alert(") {
			t.Errorf("line %d looks like injected code: %q", i, line)
		}
	}

	// And the sanitizer should have replaced the terminators with
	// spaces, not stripped them entirely — so the readable comment
	// retains the surrounding text.
	mustContain(t, out.AppJS, `in_lf alert('lf');//`, `LF replaced with space`)
	mustContain(t, out.AppJS, `out_cr alert('cr');//`, `CR replaced with space`)
	mustContain(t, out.AppJS, `stream_ls alert('ls');//`, `U+2028 replaced with space`)
}

// TestGenerator_IndexHTML_AutoEscape verifies that html/template's
// contextual escaping defends against quote-bearing --blocks-base-url
// values. text/template would have allowed these to break out of the
// <title>, the <script> string, or the document.write payload.
// Bypasses generateAndAssert because non-default vars would diverge from
// the goldens.
//
// The page <title> is now derived solely from agent names (single → name,
// 2–3 → "a + b", 4+ → "<n> agents"); since agent names are regex-
// constrained at the registry to ^[a-zA-Z0-9_]+$, ProjectName no longer
// reaches HTML output and the <title>-injection surface from earlier is
// gone. We still assert that ProjectName is NOT interpolated into the
// <title> on the multi-agent path, as a guard against regression.
func TestGenerator_IndexHTML_AutoEscape(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)

	// Single-agent: PageTitle == cards[0].AgentName (regex-safe), so the
	// surface here is BlocksAssetBaseUrl. We feed a value that would
	// trivially break a text/template rendering.
	vars := defaultVars()
	vars.BlocksAssetBaseUrl = `https://x.com/" onload="alert(1)"`
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, vars)
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	// The raw payload must not appear verbatim — html/template should
	// have JS-string-escaped it (the value lives inside a <script>
	// single-quoted string).
	if strings.Contains(out.IndexHTML, `https://x.com/" onload="alert(1)"`) {
		t.Errorf("expected --blocks-base-url payload to be escaped, but found raw substring in index.html")
	}

	// Multi-agent: ProjectName MUST NOT appear in the rendered HTML
	// title — the title is built from agent names.
	c2 := loadFixtureCard(t, "stest1", "stest1", 200)
	vars2 := defaultVars()
	vars2.ProjectName = `<script>alert("title")</script>`
	out2, err := GenerateApp([]*cardfetch.AgentCard{card, c2}, vars2)
	if err != nil {
		t.Fatalf("GenerateApp multi: %v", err)
	}
	if strings.Contains(out2.IndexHTML, `alert("title")`) {
		t.Errorf("expected ProjectName to be excluded from <title>, found ProjectName payload in index.html")
	}
	// html/template numerically escapes `+` inside <title> (renders as
	// `+` in the browser, but appears as &#43; in HTML source).
	mustContain(t, out2.IndexHTML, `<title>echo2 &#43; stest1</title>`, `multi-agent title is agent-name-joined`)
}

// TestGenerator_AppJS_AutoResumeUsesBlocksBaseUrl verifies the auto-
// resume partition-key check in app.js uses `vars.BlocksAssetBaseUrl`
// (the value passed via `--blocks-base-url`) as the default
// `backendBaseUrl`, NOT a hardcoded `https://blocks.ai`. The widget
// bundle bakes the same flag in as `__BACKEND_BASE_URL_DEFAULT__`, so
// the two MUST agree for the silent-refresh path to ever match — a
// scaffold built against staging would otherwise compute the expected
// partition under prod and never resume.
func TestGenerator_AppJS_AutoResumeUsesBlocksBaseUrl(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)

	vars := defaultVars()
	vars.BlocksAssetBaseUrl = "https://staging.blocks.ai"
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, vars)
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	// The auto-resume default MUST be the staging override (jsString-
	// quoted so the emitted JS is `'https://staging.blocks.ai'`).
	mustContain(
		t,
		out.AppJS,
		`: "https://staging.blocks.ai";`,
		`auto-resume backendBaseUrl default must be the --blocks-base-url override`,
	)
	// And the hardcoded prod fallback MUST NOT appear in that line.
	for _, line := range strings.Split(out.AppJS, "\n") {
		if strings.Contains(line, "const backendBaseUrl =") &&
			strings.Contains(line, `'https://blocks.ai'`) {
			t.Errorf("auto-resume line still hardcodes 'https://blocks.ai': %q", line)
		}
	}
}

// TestGenerator_AppJS_AutoResumeBlocksBaseUrlEscaped verifies the
// emitted default backendBaseUrl is JS-string-escaped via jsString, so
// a `--blocks-base-url` containing the literal's own delimiter (the
// JSON double quote) cannot break out of the string into the
// surrounding script.
func TestGenerator_AppJS_AutoResumeBlocksBaseUrlEscaped(t *testing.T) {
	card := loadFixtureCard(t, "echo2", "echo2", 200)

	vars := defaultVars()
	vars.BlocksAssetBaseUrl = `https://x.com/"; alert(1); //`
	out, err := GenerateApp([]*cardfetch.AgentCard{card}, vars)
	if err != nil {
		t.Fatalf("GenerateApp: %v", err)
	}

	// jsString -> json.Marshal escapes the embedded `"` as `\"`. The
	// emitted literal must contain the escaped form, AND must not
	// contain an unescaped `";` that would close the string and let the
	// `alert(1)` payload run as live code.
	mustContain(
		t,
		out.AppJS,
		`https://x.com/\"; alert(1); //`,
		`embedded " must be backslash-escaped inside the emitted JS string`,
	)
	if strings.Contains(out.AppJS, `/"; alert(1); //`) {
		t.Errorf("emitted JS contains an unescaped `\"` from --blocks-base-url, breaking out of the string literal")
	}
}

// TestGenerator_BuildPageTitle exercises the title selection rules end-
// to-end through GenerateApp: single → agent name; 2–3 → " + " joined;
// 4+ → "<n> agents".
func TestGenerator_BuildPageTitle(t *testing.T) {
	mk := func(name string) *cardfetch.AgentCard {
		// echo2's fixture is the simplest valid card; agent name is the
		// only field that affects <title>.
		c := loadFixtureCard(t, "echo2", "echo2", 200)
		c.AgentName = name
		return c
	}

	// html/template numerically escapes `+` inside <title> (renders as
	// `+` in the browser, but appears as &#43; in HTML source).
	cases := []struct {
		label string
		names []string
		want  string
	}{
		{"single", []string{"alpha"}, "<title>alpha</title>"},
		{"two", []string{"alpha", "beta"}, "<title>alpha &#43; beta</title>"},
		{"three", []string{"a", "b", "c"}, "<title>a &#43; b &#43; c</title>"},
		{"four_collapses", []string{"a", "b", "c", "d"}, "<title>4 agents</title>"},
		{"five_collapses", []string{"a", "b", "c", "d", "e"}, "<title>5 agents</title>"},
	}

	vars := defaultVars()
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cards := make([]*cardfetch.AgentCard, len(tc.names))
			for i, n := range tc.names {
				cards[i] = mk(n)
			}
			out, err := GenerateApp(cards, vars)
			if err != nil {
				t.Fatalf("GenerateApp: %v", err)
			}
			mustContain(t, out.IndexHTML, tc.want, "page title")
		})
	}
}
