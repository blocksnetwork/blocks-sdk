// Package scaffold's embed generator turns one or more agent-card snapshots
// into the four files that make up an embed-auth project's web/ directory:
// app.js, index.html, styles.css, README.md.
//
// The generator is pure: no network, no auth, no environment lookups.
// Inputs are *cardfetch.AgentCard values (already fetched by cmd/init.go)
// and an EmbedVars struct (widget version, asset base URL, project name).
// Outputs are the four file contents as strings, returned in an AppFiles
// struct that the caller writes to disk.
//
// Per-agent JS function bodies and HTML <section>s are constructed
// programmatically rather than via Go template loops; the conditional
// logic over inputs / outputs / streams is too branchy for text/template
// to read or maintain (per IMPL §7.3).
package scaffold

import (
	"encoding/json"
	"fmt"
	htmltemplate "html/template"
	"sort"
	"strings"
	"text/template"

	"github.com/pubnub/blocks-sdk/cli/internal/cardfetch"
	"github.com/pubnub/blocks-sdk/cli/internal/schema"
)

// EmbedVars carries scaffold-time variables that don't come from the card.
// The fields here are the only generator inputs that aren't card-derived.
type EmbedVars struct {
	// ProjectName is the directory name passed to `blocks init`.
	ProjectName string
	// WidgetVersion is the @blocks-network/embed-auth version pinned at
	// scaffold time (from widget_version.txt).
	WidgetVersion string
	// BlocksAssetBaseUrl is the Blocks base URL (derived from
	// `--blocks-base-url`). Drives both the widget bundle host emitted
	// into index.html AND the default `backendBaseUrl` baked into
	// app.js's auto-resume partition-key check. The two MUST agree so
	// the scaffold's stored-partition lookup matches the partition the
	// widget itself wrote (the widget's compile-time
	// `__BACKEND_BASE_URL_DEFAULT__` is set from the same flag).
	BlocksAssetBaseUrl string
	// BackendBaseUrl is the backend API origin the deployed bundle calls at
	// runtime (sign-in / refresh / task RPC). Resolved profile-aware at
	// `blocks init` time (see cmd.resolveWebappBackendURL). Baked into app.js
	// as the explicit `backendBaseUrl` argument to signInAndGetClient(s) and
	// used for the auto-resume partition-key check. Distinct from
	// BlocksAssetBaseUrl (the widget-bundle host); the two are equal for stock
	// Blocks but may differ for on-prem / split asset+API deployments.
	BackendBaseUrl string
	// CardSnapshotDate is stamped into the per-agent header comment.
	CardSnapshotDate string
}

// AppFiles is the generator's return value: four file contents to write
// into <project>/web/ (and README.md to <project>/README.md).
type AppFiles struct {
	AppJS     string
	IndexHTML string
	StylesCSS string
	ReadmeMD  string
}

// Generator complexity ceilings (IMPL §R3.5). Cards exceeding any of
// these have the surplus elements listed as TODO comments rather than
// emitted as live code.
const (
	maxInputs  = 5
	maxOutputs = 3
	maxStreams = 2
)

// GenerateApp renders the four embed-scaffold files for the given cards.
//
// cards must have at least one entry. Multi-agent scaffolds use
// signInAndGetClients and emit one labeled <section>+function block per
// agent in input order.
func GenerateApp(cards []*cardfetch.AgentCard, vars EmbedVars) (AppFiles, error) {
	if len(cards) == 0 {
		return AppFiles{}, fmt.Errorf("GenerateApp: at least one card required")
	}
	for i, c := range cards {
		if c == nil {
			return AppFiles{}, fmt.Errorf("GenerateApp: cards[%d] is nil", i)
		}
	}

	multi := len(cards) > 1

	appJS, err := renderAppJS(cards, vars, multi)
	if err != nil {
		return AppFiles{}, err
	}
	indexHTML, err := renderIndexHTML(cards, vars)
	if err != nil {
		return AppFiles{}, err
	}
	stylesCSS, err := readEmbedTemplate("templates/embed/styles.css")
	if err != nil {
		return AppFiles{}, err
	}
	readme, err := renderReadme(cards, vars)
	if err != nil {
		return AppFiles{}, err
	}

	return AppFiles{
		AppJS:     appJS,
		IndexHTML: indexHTML,
		StylesCSS: stylesCSS,
		ReadmeMD:  readme,
	}, nil
}

func readEmbedTemplate(path string) (string, error) {
	raw, err := templateFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read embedded %s: %w", path, err)
	}
	return string(raw), nil
}

// ----------------------------------------------------------------------
// app.js rendering
// ----------------------------------------------------------------------

func renderAppJS(cards []*cardfetch.AgentCard, vars EmbedVars, multi bool) (string, error) {
	tmplSrc, err := readEmbedTemplate("templates/embed/app.js.base.tmpl")
	if err != nil {
		return "", err
	}

	var headerLines []string
	for _, c := range cards {
		headerLines = append(headerLines, agentHeaderComment(c, vars.CardSnapshotDate))
	}
	headerComments := strings.Join(headerLines, "\n")

	signInComment, signInWiring := renderSignIn(cards, vars, multi)
	agentBlocks := renderAgentBlocks(cards, multi)

	tmpl, err := template.New("app.js").Parse(tmplSrc)
	if err != nil {
		return "", fmt.Errorf("parse app.js.base.tmpl: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{
		"HeaderComments": headerComments,
		"SignInComment":  signInComment,
		"SignInWiring":   signInWiring,
		"AgentBlocks":    agentBlocks,
	}); err != nil {
		return "", fmt.Errorf("execute app.js.base.tmpl: %w", err)
	}
	return b.String(), nil
}

func agentHeaderComment(c *cardfetch.AgentCard, snapshotDate string) string {
	var b strings.Builder
	if snapshotDate == "" {
		fmt.Fprintf(&b, "// Agent: %s — card snapshot taken at init time. Re-run `blocks init` to refresh.\n", c.AgentName)
	} else {
		fmt.Fprintf(&b, "// Agent: %s — card snapshot taken %s. Re-run `blocks init` to refresh.\n", c.AgentName, snapshotDate)
	}
	if len(c.Inputs) > 0 {
		b.WriteString("// Inputs:")
		for _, in := range c.Inputs {
			fmt.Fprintf(&b, "  %s (%s)", jsCommentText(in.ID), jsCommentText(displayContentType(in.ContentType)))
		}
		b.WriteString("\n")
	}
	if len(c.Outputs) > 0 {
		b.WriteString("// Outputs:")
		for _, op := range c.Outputs {
			fmt.Fprintf(&b, " %s (%s)", jsCommentText(op.ID), jsCommentText(displayContentType(op.ContentType)))
		}
		b.WriteString("\n")
	}
	if len(c.Streams) > 0 {
		streamKeys := sortedStreamKeys(c.Streams)
		b.WriteString("// Streams:")
		for _, k := range streamKeys {
			s := c.Streams[k]
			fmt.Fprintf(&b, " %s (%s, %s)", jsCommentText(k), jsCommentText(s.Format), jsCommentText(s.Direction))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func displayContentType(ct string) string {
	if ct == "" {
		return "unknown"
	}
	return ct
}

func sortedStreamKeys(m map[string]cardfetch.StreamDecl) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func renderSignIn(cards []*cardfetch.AgentCard, vars EmbedVars, multi bool) (string, string) {
	var comment string
	var b strings.Builder
	b.WriteString("  let clients = null;\n\n")

	// Shared backend resolver: honor the `blocks dev` override
	// (window.__BLOCKS_EMBED_DEV__.backendBaseUrl) locally, else the
	// scaffold-time baked backend URL. Sign-in and the auto-resume
	// partition-key check MUST use the same value or silent refresh breaks.
	b.WriteString("  function embedBackendBaseUrl() {\n")
	b.WriteString("    const dev = (typeof window !== 'undefined' && window.__BLOCKS_EMBED_DEV__) || null;\n")
	b.WriteString("    return (dev && dev.backendBaseUrl) ? dev.backendBaseUrl : ")
	b.WriteString(jsString(vars.BackendBaseUrl))
	b.WriteString(";\n")
	b.WriteString("  }\n\n")

	// Emit the sign-in flow as a named function so the click handler and
	// the page-load auto-resume both share the same code path. The
	// auto-resume call below uses the widget's built-in silent-refresh
	// path (signInAndGetClients takes the resume branch when a stored
	// session exists), so reload-after-sign-in keeps the user signed in
	// without a popup.
	b.WriteString("  async function attemptSignIn() {\n")
	b.WriteString("    authError.textContent = '';\n")
	b.WriteString("    if (!(await whenBlocksAuthReady(WIDGET_READY_TIMEOUT_MS))) {\n")
	b.WriteString("      authError.textContent = 'The Blocks auth widget failed to load. Check your network connection and reload.';\n")
	b.WriteString("      return false;\n")
	b.WriteString("    }\n")
	b.WriteString("    try {\n")
	if multi {
		comment = "Multi-agent scaffold: signInAndGetClients returns one TaskClient per agent."
		b.WriteString("      clients = await BlocksAuth.signInAndGetClients({ agents: [")
		var names []string
		for _, c := range cards {
			names = append(names, jsString(c.AgentName))
		}
		b.WriteString(strings.Join(names, ", "))
		b.WriteString("], backendBaseUrl: embedBackendBaseUrl() });\n")
		b.WriteString("      window.__blocksClients = clients; // exposed for debugging\n")
	} else {
		comment = "Single-agent scaffold: signInAndGetClient returns one TaskClient."
		fmt.Fprintf(&b, "      const client = await BlocksAuth.signInAndGetClient({ agent: %s, backendBaseUrl: embedBackendBaseUrl() });\n", jsString(cards[0].AgentName))
		fmt.Fprintf(&b, "      clients = { [%s]: client };\n", jsString(cards[0].AgentName))
	}
	b.WriteString("      signInBtn.hidden = true;\n")
	b.WriteString("      signOutBtn.hidden = false;\n")
	// Reveal only the sections for agents the backend actually returned a
	// client for. The popup intersects requested agents with agents the
	// user can reach, so a private agent the user has no grant on is
	// filtered out (impl_06 §4.1). Revealing its section would let the
	// user click Send and hit "no signed-in client" mid-flow; better to
	// hide it until the user requests access (BLOCKS-162 invitation
	// flow). Customize this block if you want to show a tailored
	// "request access" notice for filtered-out agents.
	for _, c := range cards {
		fmt.Fprintf(&b, "      if (clients[%s]) document.getElementById('section-%s').hidden = false;\n", jsString(c.AgentName), c.AgentName)
	}
	b.WriteString("      return true;\n")
	b.WriteString("    } catch (err) {\n")
	// blocksErrorMessage centralizes the BlocksAuthError + InsufficientBalance
	// branches so sign-in and per-send catches share a single source of truth.
	b.WriteString("      authError.textContent = blocksErrorMessage(err);\n")
	b.WriteString("      return false;\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n\n")

	b.WriteString("  signInBtn.addEventListener('click', attemptSignIn);\n\n")

	// Auto-resume on page load. We can't blindly call attemptSignIn() —
	// signInAndGetClients only takes the silent-refresh path when the
	// stored partition matches the page's EXACT `(backendBaseUrl,
	// pageOrigin, agents[])` tuple. If only a stale or unrelated
	// partition exists on the same origin (e.g. an older agent set),
	// signInAndGetClients falls through to the popup, which browsers
	// block outside a user gesture and surfaces POPUP_BLOCKED to the
	// user before they've done anything. Recompute the page's expected
	// partition key (using the widget's own exported helper) and only
	// auto-resume when an exact match exists.
	b.WriteString("  // Auto-resume on page load: silent refresh ONLY when the stored partition exactly matches this page's (backend, origin, agents); otherwise wait for click.\n")
	b.WriteString("  async function hasExactStoredEmbedSession() {\n")
	b.WriteString("    try {\n")
	b.WriteString("      if (typeof BlocksAuth === 'undefined' || typeof BlocksAuth.computePartitionKey !== 'function') return false;\n")
	b.WriteString("      const raw = localStorage.getItem('blocks-auth-active-sessions-v1');\n")
	b.WriteString("      if (!raw) return false;\n")
	b.WriteString("      const arr = JSON.parse(raw);\n")
	b.WriteString("      if (!Array.isArray(arr) || arr.length === 0) return false;\n")
	// backendBaseUrl comes from embedBackendBaseUrl() — the same resolver the
	// sign-in call uses — so the recomputed partition key matches the one the
	// widget wrote. agentNames is the canonical list from blocks.config.json
	// (this scaffold is one-shot, so it's the exact set we'll pass to
	// signInAndGetClient*).
	b.WriteString("      const backendBaseUrl = embedBackendBaseUrl();\n")
	b.WriteString("      const pageOrigin = window.location.origin;\n")
	b.WriteString("      const agentNames = [")
	for i, c := range cards {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(jsString(c.AgentName))
	}
	b.WriteString("];\n")
	b.WriteString("      const expected = await BlocksAuth.computePartitionKey({ backendBaseUrl, pageOrigin, agentNames });\n")
	b.WriteString("      return arr.some(function (entry) { return entry && entry.partitionKey === expected; });\n")
	b.WriteString("    } catch (_) {\n")
	b.WriteString("      return false;\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	// Wait for the widget before probing for a stored session — otherwise a
	// race where app.js runs before the widget loads makes
	// hasExactStoredEmbedSession() return false and never retries, leaving a
	// returning user signed out despite a valid stored session.
	b.WriteString("  whenBlocksAuthReady(WIDGET_READY_TIMEOUT_MS).then(function () {\n")
	b.WriteString("    hasExactStoredEmbedSession().then(function (yes) {\n")
	b.WriteString("      if (yes) attemptSignIn();\n")
	b.WriteString("    });\n")
	b.WriteString("  });")

	return comment, b.String()
}

func renderAgentBlocks(cards []*cardfetch.AgentCard, multi bool) string {
	var b strings.Builder
	for _, c := range cards {
		b.WriteString("\n")
		b.WriteString(renderAgentBlock(c, multi))
	}
	return b.String()
}

func renderAgentBlock(c *cardfetch.AgentCard, multi bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  // === Agent: %s ===\n", c.AgentName)
	fmt.Fprintf(&b, "  document.getElementById('send-%s').addEventListener('click', async () => {\n", c.AgentName)
	fmt.Fprintf(&b, "    const sendBtn = document.getElementById('send-%s');\n", c.AgentName)
	fmt.Fprintf(&b, "    const outputEl = document.getElementById('output-%s');\n", c.AgentName)
	if hasOutboundStream(c) {
		fmt.Fprintf(&b, "    const streamEl = document.getElementById('stream-%s');\n", c.AgentName)
		b.WriteString("    streamEl.textContent = '';\n")
	}
	b.WriteString("    outputEl.textContent = '';\n")
	b.WriteString("    sendBtn.disabled = true;\n")
	// Track which artifact refs we've rendered so the post-terminal sweep
	// can render any that came in via history-preload (those don't fire
	// onArtifact) without double-rendering live ones. Set keys are object
	// identity; ArtifactRef instances are stable across listArtifacts and
	// onArtifact (see task-session.ts accumulateArtifact).
	b.WriteString("    const renderedRefs = new Set();\n")
	b.WriteString("    try {\n")
	fmt.Fprintf(&b, "      const client = clients[%s];\n", jsString(c.AgentName))
	if multi {
		fmt.Fprintf(&b, "      if (!client) throw new Error('no signed-in client for ' + %s);\n", jsString(c.AgentName))
	}

	// Inputs → requestParts construction.
	b.WriteString("      const requestParts = [];\n")
	limitedInputs := c.Inputs
	if len(limitedInputs) > maxInputs {
		limitedInputs = limitedInputs[:maxInputs]
	}
	for i, in := range limitedInputs {
		b.WriteString(renderInputBlock(c.AgentName, i, in))
	}
	if len(c.Inputs) > maxInputs {
		b.WriteString("      // TODO: card declares additional inputs beyond the generator's\n")
		b.WriteString("      //       complexity ceiling. Wire these manually:\n")
		for _, in := range c.Inputs[maxInputs:] {
			fmt.Fprintf(&b, "      //   - %s (%s)\n", jsCommentText(in.ID), jsCommentText(displayContentType(in.ContentType)))
		}
	}

	// taskKind selection and (for pipe-capable agents) duration wiring.
	b.WriteString(renderSendMessage(c))

	// Outputs → onArtifact dispatch.
	b.WriteString(renderOutputDispatch(c.Outputs))

	// Streams → onStream consumer.
	b.WriteString(renderStreamConsumers(c.Streams))

	// Wait for terminal.
	b.WriteString("      const terminal = await session.waitForTerminal();\n")
	b.WriteString("      if (terminal.state !== 'completed') {\n")
	b.WriteString("        outputEl.textContent += '\\n[' + (terminal.error || terminal.reason || 'task ' + terminal.state) + ']';\n")
	b.WriteString("      }\n")
	// Post-terminal sweep. sendMessage() may preload artifact refs from
	// history before returning (task-client.ts), and those preloads do
	// NOT fire onArtifact. Always walk listArtifacts() and render any
	// ref we haven't already handled. listArtifacts() returns
	// ArtifactRef[] without outputId, so per-output dispatch isn't
	// possible here — we render by the artifact's own mimeType.
	b.WriteString("      for (const ref of session.listArtifacts()) {\n")
	b.WriteString("        if (renderedRefs.has(ref)) continue;\n")
	b.WriteString("        renderedRefs.add(ref);\n")
	b.WriteString("        try {\n")
	b.WriteString("          const downloaded = await session.downloadArtifact(ref);\n")
	b.WriteString("          renderArtifactByMime(downloaded.data, ref.mimeType, outputEl);\n")
	b.WriteString("        } catch (err) {\n")
	b.WriteString("          outputEl.textContent += '\\n[download failed: ' + err.message + ']';\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")

	b.WriteString("    } catch (err) {\n")
	// Same blocksErrorMessage helper as the sign-in catch — keeps
	// InsufficientBalance / BlocksAuthError handling consistent across
	// the two error paths. Send-time InsufficientBalance is the common
	// real-world case for paid agents (sign-in itself doesn't charge).
	b.WriteString("      outputEl.textContent += '\\n[' + blocksErrorMessage(err) + ']';\n")
	b.WriteString("    } finally {\n")
	b.WriteString("      sendBtn.disabled = false;\n")
	b.WriteString("    }\n")
	b.WriteString("  });\n")
	return b.String()
}

func hasOutboundStream(c *cardfetch.AgentCard) bool {
	for _, s := range c.Streams {
		if s.Direction == "outbound" {
			return true
		}
	}
	return false
}

// classifyTaskKinds reports whether the card's declared taskKinds include the
// request and pipe kinds. An empty list is treated as request-only (the
// registry default), matching the prior pickTaskKind behavior.
func classifyTaskKinds(kinds []string) (hasRequest, hasPipe bool) {
	for _, k := range kinds {
		switch k {
		case "request":
			hasRequest = true
		case "pipe":
			hasPipe = true
		}
	}
	return hasRequest, hasPipe
}

// supportsPipe reports whether the agent declares the pipe task kind (pipe-only
// or mixed request+pipe). Drives the duration control in the HTML.
func supportsPipe(c *cardfetch.AgentCard) bool {
	_, hasPipe := classifyTaskKinds(c.TaskKinds)
	return hasPipe
}

// renderSendMessage emits the taskKind- and duration-aware sendMessage call.
// Pipe tasks require a duration in MINUTES (1–43200) per the pipe-task
// contract; the value is read from the page's duration input.
//
//   - pipe-only  → always taskKind 'pipe' + duration from the input.
//   - mixed      → a checkbox selects request vs pipe; duration is sent only
//     when pipe is selected.
//   - request-only / empty → taskKind 'request', no duration (byte-identical
//     to the prior generator output).
func renderSendMessage(c *cardfetch.AgentCard) string {
	hasRequest, hasPipe := classifyTaskKinds(c.TaskKinds)
	nameLit := jsString(c.AgentName)
	durID := jsString("duration-" + c.AgentName)
	var b strings.Builder

	switch {
	case hasPipe && !hasRequest:
		b.WriteString("      // Pipe-only agent: taskKind 'pipe' requires a duration in MINUTES (1–43200).\n")
		fmt.Fprintf(&b, "      const duration = readPipeDuration(document.getElementById(%s));\n", durID)
		b.WriteString("      const session = await client.sendMessage({\n")
		fmt.Fprintf(&b, "        agentName: %s,\n", nameLit)
		b.WriteString("        taskKind: \"pipe\",\n")
		b.WriteString("        duration,\n")
		b.WriteString("        requestParts,\n")
		b.WriteString("      });\n")
	case hasPipe && hasRequest:
		toggleID := jsString("pipe-toggle-" + c.AgentName)
		b.WriteString("      // Agent supports both 'request' and 'pipe'. The checkbox picks the mode;\n")
		b.WriteString("      // 'pipe' requires a duration in MINUTES (1–43200).\n")
		fmt.Fprintf(&b, "      const asPipe = document.getElementById(%s).checked;\n", toggleID)
		b.WriteString("      const message = {\n")
		fmt.Fprintf(&b, "        agentName: %s,\n", nameLit)
		b.WriteString("        taskKind: asPipe ? \"pipe\" : \"request\",\n")
		b.WriteString("        requestParts,\n")
		b.WriteString("      };\n")
		fmt.Fprintf(&b, "      if (asPipe) message.duration = readPipeDuration(document.getElementById(%s));\n", durID)
		b.WriteString("      const session = await client.sendMessage(message);\n")
	default:
		b.WriteString("      const session = await client.sendMessage({\n")
		fmt.Fprintf(&b, "        agentName: %s,\n", nameLit)
		b.WriteString("        taskKind: \"request\",\n")
		b.WriteString("        requestParts,\n")
		b.WriteString("      });\n")
	}
	return b.String()
}

func renderInputBlock(agentName string, idx int, in cardfetch.InputDecl) string {
	var b strings.Builder
	cls := schema.ClassifyContentType(in.ContentType)
	if in.ID == "" {
		fmt.Fprintf(&b, "      // TODO: input declared without an id; skipping.\n")
		return b.String()
	}

	// Index-based variable names avoid collisions when card ids differ only
	// by punctuation (e.g. "a-b" vs "a_b"). Each agent's send function is
	// its own scope so per-input indexes are stable.
	domID := jsString("input-" + agentName + "-" + in.ID)
	partLit := jsString(in.ID)
	idLit := jsString(in.ID)

	switch cls {
	case schema.TransportForm:
		fmt.Fprintf(&b, "      // input %q is form-class (%s) — wire shape is\n", in.ID, in.ContentType)
		b.WriteString("      // { partId, text: <JSON-encoded value> } per SDK_CONTRACT §8.6.2g.\n")
		fmt.Fprintf(&b, "      const ta_%d = document.getElementById(%s);\n", idx, domID)
		fmt.Fprintf(&b, "      try { JSON.parse(ta_%d.value); } catch (e) { throw new Error('input ' + %s + ': invalid JSON: ' + e.message); }\n", idx, idLit)
		fmt.Fprintf(&b, "      requestParts.push({ partId: %s, text: ta_%d.value });\n", partLit, idx)
	case schema.TransportText:
		fmt.Fprintf(&b, "      // input %q is text-class (%s) — wire shape is\n", in.ID, in.ContentType)
		b.WriteString("      // { partId, text: <raw string> } per SDK_CONTRACT §8.6.2g.\n")
		fmt.Fprintf(&b, "      const ta_%d = document.getElementById(%s);\n", idx, domID)
		fmt.Fprintf(&b, "      requestParts.push({ partId: %s, text: ta_%d.value });\n", partLit, idx)
	case schema.TransportFile:
		fmt.Fprintf(&b, "      // input %q is file-class (%s) — use filePart() so files <= 16 KB\n", in.ID, in.ContentType)
		b.WriteString("      // inline and larger files use the pre-signed URL upload flow automatically.\n")
		fmt.Fprintf(&b, "      const fileEl_%d = document.getElementById(%s);\n", idx, domID)
		fmt.Fprintf(&b, "      const file_%d = fileEl_%d.files[0];\n", idx, idx)
		if in.Required {
			fmt.Fprintf(&b, "      if (!file_%d) throw new Error('input ' + %s + ': no file selected');\n", idx, idLit)
		}
		fmt.Fprintf(&b, "      if (file_%d) {\n", idx)
		fmt.Fprintf(&b, "        // SDK filePart() helper picks inline vs upload-URL automatically.\n")
		fmt.Fprintf(&b, "        requestParts.push({\n")
		fmt.Fprintf(&b, "          partId: %s,\n", partLit)
		fmt.Fprintf(&b, "          file: file_%d,\n", idx)
		fmt.Fprintf(&b, "          fileName: file_%d.name,\n", idx)
		fmt.Fprintf(&b, "          contentType: file_%d.type || %s,\n", idx, jsString(in.ContentType))
		fmt.Fprintf(&b, "        });\n")
		fmt.Fprintf(&b, "      }\n")
	default:
		// Unknown class.
		fmt.Fprintf(&b, "      // TODO: input %q has unrecognized contentType %q.\n", in.ID, in.ContentType)
		b.WriteString("      // Sending JSON-encoded text as a defensive default. Confirm with\n")
		b.WriteString("      // the agent's owner what the handler reads.\n")
		fmt.Fprintf(&b, "      const ta_%d = document.getElementById(%s);\n", idx, domID)
		fmt.Fprintf(&b, "      try { JSON.parse(ta_%d.value); } catch (e) { throw new Error('input ' + %s + ': invalid JSON: ' + e.message); }\n", idx, idLit)
		fmt.Fprintf(&b, "      requestParts.push({ partId: %s, text: ta_%d.value });\n", partLit, idx)
	}
	if len(in.Schema) == 0 && cls != schema.TransportFile {
		fmt.Fprintf(&b, "      // TODO: agent did not declare a schema for input %q. Replace the\n", in.ID)
		b.WriteString("      //       textarea default with whatever your handler accepts.\n")
	}
	return b.String()
}

func renderOutputDispatch(outputs []cardfetch.OutputDecl) string {
	var b strings.Builder

	// Common preamble. The SDK invokes onArtifact callbacks
	// synchronously and ignores their returned promise (see
	// task-session.ts). If a terminal event fires while an artifact
	// download is awaiting, the post-terminal sweep can re-enter
	// the same ref. Claim the ref BEFORE the await so the sweep
	// (and re-entrant onArtifact for the same ref) skip it. If the
	// download fails we stay claimed — the sweep would have hit
	// the same error.
	preamble := func() {
		b.WriteString("      session.onArtifact(async (event) => {\n")
		b.WriteString("        if (renderedRefs.has(event.artifactRef)) return;\n")
		b.WriteString("        renderedRefs.add(event.artifactRef);\n")
		b.WriteString("        let bytes;\n")
		b.WriteString("        try {\n")
		b.WriteString("          const downloaded = await session.downloadArtifact(event.artifactRef);\n")
		b.WriteString("          bytes = downloaded.data;\n")
		b.WriteString("        } catch (err) {\n")
		b.WriteString("          outputEl.textContent += '\\n[download failed: ' + err.message + ']';\n")
		b.WriteString("          return;\n")
		b.WriteString("        }\n")
	}

	if len(outputs) == 0 {
		// Card declares no outputs. Render any incoming artifact by its
		// own mimeType — the agent may emit untyped artifacts and we
		// don't want to silently drop them.
		b.WriteString("      // Card declares no outputs. Any artifact that arrives is rendered\n")
		b.WriteString("      // via the artifact's own mimeType (no per-output declaration to key off).\n")
		preamble()
		b.WriteString("        renderArtifactByMime(bytes, event.artifactRef.mimeType, outputEl);\n")
		b.WriteString("      });\n")
		return b.String()
	}

	limited := outputs
	overflow := []cardfetch.OutputDecl{}
	if len(outputs) > maxOutputs {
		limited = outputs[:maxOutputs]
		overflow = outputs[maxOutputs:]
	}

	if len(limited) == 1 {
		// Single declared output — route every artifact here regardless
		// of event.outputId presence. outputId is OPTIONAL per
		// SDK_CONTRACT §9.1 and many real agents (echo2, stest1) emit
		// artifacts without setting it.
		op := limited[0]
		b.WriteString("      // Card declares exactly 1 output — route every artifact here\n")
		b.WriteString("      // regardless of event.outputId. outputId is OPTIONAL per\n")
		b.WriteString("      // SDK_CONTRACT §9.1; we don't gate on its presence.\n")
		preamble()
		fmt.Fprintf(&b, "        // === Output: %q (%s, guaranteed=%t) ===\n", op.ID, displayContentType(op.ContentType), op.Guaranteed)
		b.WriteString(indent(renderOutputCase(op), "        "))
		b.WriteString("      });\n")
		return b.String()
	}

	// >=2 declared outputs: switch on event.outputId, with a mimeType-
	// based default for untagged or unknown ids.
	b.WriteString("      // Output rendering is dispatched via onArtifact, switching on\n")
	b.WriteString("      // event.outputId. outputId is OPTIONAL per SDK_CONTRACT §9.1, so\n")
	b.WriteString("      // untagged or unknown artifacts fall to a mimeType-based renderer.\n")
	preamble()
	b.WriteString("        switch (event.outputId) {\n")
	for _, op := range limited {
		fmt.Fprintf(&b, "          // === Output: %q (%s, guaranteed=%t) ===\n", op.ID, displayContentType(op.ContentType), op.Guaranteed)
		fmt.Fprintf(&b, "          case %s: {\n", jsString(op.ID))
		b.WriteString(indent(renderOutputCase(op), "            "))
		b.WriteString("            break;\n")
		b.WriteString("          }\n")
	}
	b.WriteString("          default: {\n")
	b.WriteString("            // Untagged or unknown outputId. Render based on the artifact's\n")
	b.WriteString("            // own mimeType rather than guessing which declared output it\n")
	b.WriteString("            // was meant for.\n")
	b.WriteString("            renderArtifactByMime(bytes, event.artifactRef.mimeType, outputEl);\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("      });\n")

	if len(overflow) > 0 {
		b.WriteString("      // TODO: card declares additional outputs beyond the generator's\n")
		b.WriteString("      //       complexity ceiling. Add cases manually:\n")
		for _, op := range overflow {
			fmt.Fprintf(&b, "      //   - %s (%s)\n", jsCommentText(op.ID), jsCommentText(displayContentType(op.ContentType)))
		}
	}
	return b.String()
}

func renderOutputCase(op cardfetch.OutputDecl) string {
	cls := schema.ClassifyContentType(op.ContentType)
	var b strings.Builder
	idLit := jsString(op.ID)
	ctLit := jsString(op.ContentType)
	switch {
	case op.ContentType == "application/json":
		b.WriteString("const parsed = JSON.parse(new TextDecoder().decode(bytes));\n")
		b.WriteString("outputEl.textContent += JSON.stringify(parsed, null, 2);\n")
	case cls == schema.TransportText:
		b.WriteString("outputEl.textContent += new TextDecoder().decode(bytes);\n")
	case cls == schema.TransportFile && strings.HasPrefix(op.ContentType, "image/"):
		fmt.Fprintf(&b, "const blob = new Blob([bytes], { type: %s });\n", ctLit)
		b.WriteString("const img = document.createElement('img');\n")
		b.WriteString("img.className = 'output';\n")
		b.WriteString("img.src = URL.createObjectURL(blob);\n")
		b.WriteString("outputEl.appendChild(img);\n")
	case cls == schema.TransportFile:
		fmt.Fprintf(&b, "// TODO: output %q is %s. Rendered as a download link;\n", op.ID, op.ContentType)
		b.WriteString("//       replace with whatever your UI needs.\n")
		fmt.Fprintf(&b, "const blob = new Blob([bytes], { type: %s });\n", ctLit)
		b.WriteString("const link = document.createElement('a');\n")
		b.WriteString("link.href = URL.createObjectURL(blob);\n")
		fmt.Fprintf(&b, "link.download = %s;\n", idLit)
		fmt.Fprintf(&b, "link.textContent = 'Download ' + %s;\n", idLit)
		b.WriteString("outputEl.appendChild(link);\n")
	case cls == schema.TransportForm:
		b.WriteString("const parsed = JSON.parse(new TextDecoder().decode(bytes));\n")
		b.WriteString("outputEl.textContent += JSON.stringify(parsed, null, 2);\n")
	default:
		fmt.Fprintf(&b, "// TODO: output %q has unrecognized contentType %q. Rendered as download.\n", op.ID, op.ContentType)
		b.WriteString("const blob = new Blob([bytes]);\n")
		b.WriteString("const link = document.createElement('a');\n")
		b.WriteString("link.href = URL.createObjectURL(blob);\n")
		fmt.Fprintf(&b, "link.download = %s;\n", idLit)
		fmt.Fprintf(&b, "link.textContent = 'Download ' + %s;\n", idLit)
		b.WriteString("outputEl.appendChild(link);\n")
	}
	return b.String()
}

func renderStreamConsumers(streams map[string]cardfetch.StreamDecl) string {
	if len(streams) == 0 {
		return ""
	}
	keys := sortedStreamKeys(streams)
	var b strings.Builder

	wired := 0
	for _, k := range keys {
		s := streams[k]
		switch s.Direction {
		case "outbound":
			if wired >= maxStreams {
				fmt.Fprintf(&b, "      // TODO: card declares additional outbound stream %q (%s); generator\n", k, s.Format)
				b.WriteString("      //       complexity ceiling reached. Wire it manually following the same\n")
				b.WriteString("      //       descriptor.declaredStream pattern below.\n")
				continue
			}
			b.WriteString(renderOutboundStream(k, s))
			wired++
		case "inbound", "bidirectional":
			fmt.Fprintf(&b, "      // TODO: agent declares an %s stream %q (%s).\n", s.Direction, k, s.Format)
			b.WriteString("      //       Consumer-side stream writing is not generated — see\n")
			b.WriteString("      //       SDK_CONTRACT §8.4 / §8.7 for the consumer-writer pattern.\n")
		default:
			fmt.Fprintf(&b, "      // TODO: stream %q has unrecognized direction %q.\n", k, s.Direction)
		}
	}
	return b.String()
}

func renderOutboundStream(key string, s cardfetch.StreamDecl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "      // === Stream: %q (%s, outbound) ===\n", key, s.Format)
	b.WriteString("      // Filter on declaredStream (the card key); streamId is a runtime-\n")
	b.WriteString("      // generated instance id and won't match the card key.\n")
	b.WriteString("      // If declaredStream is undefined the agent didn't tag this stream\n")
	b.WriteString("      // with a card key — the filter falls through and we ignore the stream.\n")
	if s.Affinity == "shared" {
		b.WriteString("      // Note: shared-affinity stream — see SDK_CONTRACT §8.4.1a; do not\n")
		b.WriteString("      //       call stream.end() on the inbound side.\n")
	}
	b.WriteString("      session.onStream(async (streamRef) => {\n")
	fmt.Fprintf(&b, "        if (streamRef.descriptor.declaredStream !== %s) return;\n", jsString(key))
	b.WriteString("        const stream = streamRef.open();\n")
	b.WriteString("        try {\n")
	if s.Format == "bytes" {
		b.WriteString("          for await (const chunk of stream.bytes()) {\n")
		b.WriteString("            streamEl.textContent += new TextDecoder().decode(chunk);\n")
		b.WriteString("          }\n")
	} else {
		b.WriteString("          for await (const ev of stream.events()) {\n")
		b.WriteString("            streamEl.textContent += JSON.stringify(ev) + '\\n';\n")
		b.WriteString("          }\n")
	}
	b.WriteString("        } catch (err) {\n")
	b.WriteString("          streamEl.textContent += '\\n[stream error: ' + err.message + ']';\n")
	b.WriteString("        }\n")
	b.WriteString("      });\n")
	return b.String()
}

// jsString JSON-encodes s into a JS string literal with surrounding quotes.
// Defensive against card-derived ids/keys/contentTypes containing quotes,
// backslashes, control characters, or other JS-string-breaking content.
// JSON strings are a subset of valid JS string literals.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// jsCommentText replaces JS line-terminator characters (CR, LF, U+2028,
// U+2029) so card-derived strings can be safely interpolated into a
// `//` line comment. Without this, an id containing "\nalert(1);//"
// would terminate the comment and inject executable JS into the
// generated app.js.
//
// `%q` callers are already safe (Go-quoting escapes newlines as the
// two-character sequence `\n`), so this is only needed where the value
// is interpolated raw via `%s`.
var jsCommentSanitizer = strings.NewReplacer(
	"\r", " ",
	"\n", " ",
	" ", " ",
	" ", " ",
)

func jsCommentText(s string) string {
	return jsCommentSanitizer.Replace(s)
}

func indent(s, pad string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

// ----------------------------------------------------------------------
// index.html rendering
// ----------------------------------------------------------------------

// buildPageTitle picks the <title> for the embed page from the agent
// cards. Single agent → the agent name. 2–3 agents → "a + b" joined.
// 4+ agents → "<n> agents". Agent names are regex-constrained
// (^[a-zA-Z0-9_]+$) at the registry, so the title is HTML-safe by
// construction; html/template still contextually escapes it.
func buildPageTitle(cards []*cardfetch.AgentCard) string {
	switch len(cards) {
	case 0:
		return "Blocks embed page"
	case 1:
		return cards[0].AgentName
	case 2, 3:
		names := make([]string, len(cards))
		for i, c := range cards {
			names[i] = c.AgentName
		}
		return strings.Join(names, " + ")
	default:
		return fmt.Sprintf("%d agents", len(cards))
	}
}

func renderIndexHTML(cards []*cardfetch.AgentCard, vars EmbedVars) (string, error) {
	tmplSrc, err := readEmbedTemplate("templates/embed/index.html.base.tmpl")
	if err != nil {
		return "", err
	}
	// html/template applies contextual escaping: HTML-escape inside <title>,
	// JS-string-escape inside <script>, URL-encode inside href, etc. This
	// defends against single-quote / </script> / <title>-breaking content
	// in --blocks-base-url and the project directory name. AgentSections
	// is HTML we built ourselves with explicit htmlEscape calls, so we mark
	// it template.HTML to opt out of double-escaping.
	tmpl, err := htmltemplate.New("index.html").Parse(tmplSrc)
	if err != nil {
		return "", fmt.Errorf("parse index.html.base.tmpl: %w", err)
	}

	pageTitle := buildPageTitle(cards)

	var sections strings.Builder
	for _, c := range cards {
		sections.WriteString(renderAgentSection(c))
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]any{
		"PageTitle":          pageTitle,
		"BlocksAssetBaseUrl": vars.BlocksAssetBaseUrl,
		"WidgetVersion":      vars.WidgetVersion,
		"AgentSections":      htmltemplate.HTML(strings.TrimRight(sections.String(), "\n")),
	}); err != nil {
		return "", fmt.Errorf("execute index.html.base.tmpl: %w", err)
	}
	return b.String(), nil
}

func renderAgentSection(c *cardfetch.AgentCard) string {
	var b strings.Builder
	// agentName is regex-constrained to ^[a-zA-Z0-9_]+$ by the registry,
	// so it is safe to interpolate into HTML attributes and CSS-style ids
	// without escaping. Card-derived input/output ids are not constrained
	// beyond minLength: 1, so they are htmlEscape'd below.
	fmt.Fprintf(&b, "    <section class=\"agent-section\" id=\"section-%s\" hidden>\n", c.AgentName)
	fmt.Fprintf(&b, "      <h2>%s</h2>\n", htmlEscape(c.AgentName))

	limitedInputs := c.Inputs
	if len(limitedInputs) > maxInputs {
		limitedInputs = limitedInputs[:maxInputs]
	}
	for _, in := range limitedInputs {
		b.WriteString(renderInputControl(c.AgentName, in))
	}
	b.WriteString(renderPipeControls(c))
	fmt.Fprintf(&b, "      <button id=\"send-%s\" type=\"button\">Send</button>\n", c.AgentName)
	if len(c.Outputs) > 0 {
		b.WriteString("      <label>Output</label>\n")
		fmt.Fprintf(&b, "      <pre class=\"output\" id=\"output-%s\"></pre>\n", c.AgentName)
	} else {
		// Always emit an output element — the JS unconditionally appends
		// terminal-state messages here.
		fmt.Fprintf(&b, "      <pre class=\"output\" id=\"output-%s\"></pre>\n", c.AgentName)
	}
	if hasOutboundStream(c) {
		b.WriteString("      <label>Stream</label>\n")
		fmt.Fprintf(&b, "      <pre class=\"stream\" id=\"stream-%s\"></pre>\n", c.AgentName)
	}
	b.WriteString("    </section>\n")
	return b.String()
}

// renderPipeControls emits the duration input (minutes) for any pipe-capable
// agent, plus a "run as a pipe session" checkbox for mixed request+pipe agents.
// agentName is registry-constrained to ^[a-zA-Z0-9_]+$, so it is safe in ids
// and attributes without escaping. Returns "" for request-only agents.
func renderPipeControls(c *cardfetch.AgentCard) string {
	hasRequest, hasPipe := classifyTaskKinds(c.TaskKinds)
	if !hasPipe {
		return ""
	}
	var b strings.Builder
	if hasRequest {
		fmt.Fprintf(&b, "      <label class=\"pipe-toggle\"><input type=\"checkbox\" id=\"pipe-toggle-%s\" /> Run as a pipe session</label>\n", c.AgentName)
	}
	fmt.Fprintf(&b, "      <label for=\"duration-%s\">Duration (minutes, 1–43200)</label>\n", c.AgentName)
	fmt.Fprintf(&b, "      <input type=\"number\" class=\"duration\" id=\"duration-%s\" min=\"1\" max=\"43200\" value=\"30\" />\n", c.AgentName)
	return b.String()
}

func renderInputControl(agentName string, in cardfetch.InputDecl) string {
	cls := schema.ClassifyContentType(in.ContentType)
	var b strings.Builder
	// in.ID is htmlEscape'd into every attribute it appears in. agentName
	// is regex-constrained ^[a-zA-Z0-9_]+$ so it does not need escaping.
	idAttr := htmlEscape(in.ID)
	fmt.Fprintf(&b, "      <label for=\"input-%s-%s\">%s</label>\n", agentName, idAttr, htmlEscape(inputLabel(in)))
	switch cls {
	case schema.TransportFile:
		fmt.Fprintf(&b, "      <input type=\"file\" id=\"input-%s-%s\" accept=\"%s\" />\n", agentName, idAttr, htmlEscape(in.ContentType))
	case schema.TransportForm, schema.TransportUnknown:
		def := defaultInputValue(in, true)
		fmt.Fprintf(&b, "      <textarea id=\"input-%s-%s\" rows=\"4\">%s</textarea>\n", agentName, idAttr, htmlEscape(def))
	case schema.TransportText:
		def := defaultInputValue(in, false)
		fmt.Fprintf(&b, "      <textarea id=\"input-%s-%s\" rows=\"4\">%s</textarea>\n", agentName, idAttr, htmlEscape(def))
	}
	return b.String()
}

func inputLabel(in cardfetch.InputDecl) string {
	if in.Description != "" {
		return fmt.Sprintf("%s — %s", in.ID, in.Description)
	}
	return in.ID
}

// defaultInputValue returns the textarea pre-fill for an input. encodeJSON
// controls whether values are JSON-encoded (form / unknown classes) or
// emitted raw (text class).
func defaultInputValue(in cardfetch.InputDecl, encodeJSON bool) string {
	// 1. example
	if len(in.Example) > 0 {
		if encodeJSON {
			return prettyJSON(in.Example)
		}
		// text class: unwrap a JSON string if present, otherwise use raw.
		var s string
		if err := json.Unmarshal(in.Example, &s); err == nil {
			return s
		}
		return string(in.Example)
	}
	// 2. schema.default
	if d := schemaDefault(in.Schema); d != nil {
		if encodeJSON {
			return prettyJSON(d)
		}
		var s string
		if err := json.Unmarshal(d, &s); err == nil {
			return s
		}
		return string(d)
	}
	// 3. last-resort fallback
	if encodeJSON {
		return "{}"
	}
	return ""
}

func prettyJSON(raw json.RawMessage) string {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func schemaDefault(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var s struct {
		Default json.RawMessage `json:"default"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	if len(s.Default) == 0 {
		return nil
	}
	return s.Default
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// ----------------------------------------------------------------------
// README rendering
// ----------------------------------------------------------------------

func renderReadme(cards []*cardfetch.AgentCard, vars EmbedVars) (string, error) {
	tmplSrc, err := readEmbedTemplate("templates/embed/README.md.tmpl")
	if err != nil {
		return "", err
	}
	tmpl, err := template.New("README.md").Parse(tmplSrc)
	if err != nil {
		return "", fmt.Errorf("parse README.md.tmpl: %w", err)
	}

	var names []string
	for _, c := range cards {
		names = append(names, c.AgentName)
	}
	var flags strings.Builder
	for _, n := range names {
		fmt.Fprintf(&flags, " --agent %s", n)
	}

	projectName := vars.ProjectName
	if projectName == "" {
		projectName = strings.Join(names, "+")
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{
		"ProjectName": projectName,
		"AgentFlags":  flags.String(),
		"AgentList":   humanList(names),
	}); err != nil {
		return "", fmt.Errorf("execute README.md.tmpl: %w", err)
	}
	return b.String(), nil
}

func humanList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return "`" + items[0] + "`"
	case 2:
		return "`" + items[0] + "` and `" + items[1] + "`"
	}
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = "`" + it + "`"
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
}
