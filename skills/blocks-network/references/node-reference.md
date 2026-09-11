# Blocks Network Node Reference

Advanced reference for handler interfaces, streaming, TaskClient, environment variables, and deployment.

---

## Handler Interfaces

### StartTaskMessage

Key fields: `taskId`, `ownerId`, `requestParts` (input array), `taskKind` (`'request'` | `'pipe'`), `hasStream`, `writeToken`, `controlToken`. Also includes `agentName`, `orgId`, `duration`, `durationExpiresAtMs`, `callerClaims`, `requestSummary`.

**Accessing input** -- always check for undefined. Parts use `{ partId: '...', text: '...' }` format:

```typescript
const input = task.requestParts?.[0];
const text = typeof input === 'string' ? input : (input as Record<string, unknown>)?.text as string ?? 'default';
```

### TaskContext

Provides `reportStatus(message)`, `createStream(options?)`, `taskClient`, `cancelSignal`, `isCancelled`, `isExpired`, `hasStream`, `taskId`, `requestParts` (readonly), `consumerPublicKey` (readonly). Also includes `downloadInputArtifact(part)` to fetch file-type input artifacts and `publishArtifact(data, options?)` to publish artifacts mid-handler. Always use optional chaining (`ctx?.reportStatus()`) since ctx may be undefined in testing.

### CreateStreamOptions

Options: `direction` (`'outbound'`|`'inbound'`|`'bidirectional'`), `onActivate`, `metadata`, `external`, `format` (`'bytes'`|`'events'`), `bundleSizeBytes`, `maxLatencyMs`, `declaredStream` (key from card's streams block; omit when the card declares a single stream -- the SDK resolves it automatically; **required** when the card declares multiple streams), `subscribeGraceMs` (grace period in ms after `stream_started` before returning the StreamClient; gives the consumer time to subscribe; default 1000, set to 0 to skip).

### StreamObject

The handler-side stream wrapper. Mirrors the consumer-side `StreamClient` read/error/uuid surface so handler code uses the same iterators and callbacks consumers do (see the SDK contract for the normative definition).

Properties: `streamId`, `channel`, `isActive`, `external`, `uuid` (read; same as `StreamClient.uuid`, useful for log correlation), `token` (external only).

Write / lifecycle: `write(data)` (throws on inbound-only and external streams), `end()`, `activate(opts?)` (external only), `onEnd(cb)`.

Read iterators (inbound and bidirectional streams):
- **Recommended:** `bytes()` for `format: 'bytes'` streams (yields `Uint8Array`, decodes base64/utf8 transparently). `events<T>()` for `format: 'events'` streams (yields one event per `for-await` iteration; flattens producer-side batches). `readable()` returns `Promise<node:stream.Readable>` for piping into Node file/process APIs.
- **Low-level:** `inbound` is an `AsyncIterable<InboundMessage>` over raw wire envelopes (`{ data, seq, ts, format, encoding }`). Reach for it only when you need raw envelope metadata; `bytes()` / `events()` are the everyday paths.

Errors: `onError(cb: (err: StreamError) => void)` subscribes to per-stream PubNub status errors. **Append-only** — register before the read path activates; past errors do not replay. On `fatal: true` the underlying client auto-tears down so the iterator exits cleanly.

`onInboundDone` is intentionally NOT part of `StreamObject` — it's an internal callback owned by `TaskSession`. For a "stream drained" signal, `await` the for-await-of loop on `bytes()` / `events()` / `inbound`.

### HandlerResult

```typescript
{ artifacts?: ArtifactEntry[] }
```

Where `ArtifactEntry` is:

```typescript
{ data: Buffer | string; mimeType: string; fileName?: string; outputId?: string }
```

---

## Handler Patterns

All examples use the same import:

```typescript
import type { StartTaskMessage, TaskContext, HandlerResult } from '@blocks-network/sdk';
```

### Simple Text Processor

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const text = typeof input === 'string' ? input : JSON.stringify(input ?? '');
  ctx?.reportStatus('Processing...');
  return { artifacts: [{ data: text.toUpperCase(), mimeType: 'text/plain' }] };
}
```

### Streaming Handler

**Requires** `streams._default` in `agent-card.json` (see Streaming Capabilities below).
`createStream()` takes options only; the SDK derives the channel from the card-declared key plus the card's affinity.

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const input = task.requestParts?.[0];
  const text = typeof input === 'string' ? input : 'Hello from streaming agent!';

  // Guard on hasStream — request-task streaming is consumer opt-in, so
  // createStream() throws when the consumer didn't opt in.
  if (ctx?.hasStream) {
    ctx.reportStatus('Streaming...');
    const stream = await ctx.createStream({
      declaredStream: 'main-stream',
      bundleSizeBytes: 2048,
      maxLatencyMs: 50,
    });
    for (const word of text.split(' ')) stream.write(word + ' ');
    await stream.end();
  }

  return { artifacts: [{ data: text, mimeType: 'text/plain' }] };
}
```

### Agent-to-Agent (Orchestrator)

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  const text = typeof task.requestParts?.[0] === 'string' ? task.requestParts[0] : '';
  ctx?.reportStatus('Delegating to sub-agent...');

  const session = await ctx!.taskClient.sendMessage({
    agentName: 'summarizer',
    ownerId: task.ownerId,
    requestParts: [{ partId: 'request', text }],
  });

  return new Promise((resolve) => {
    session.onArtifact((event) =>
      resolve({ artifacts: [{ data: JSON.stringify(event, null, 2), mimeType: 'application/json' }] }),
    );
    session.onTerminal(() =>
      resolve({ artifacts: [{ data: 'Sub-agent completed', mimeType: 'text/plain' }] }),
    );
  });
}
```

---

## Streaming Capabilities

<!-- sync: streaming rules duplicated in SKILL.md, agent-card-reference.md, python-reference.md -->
**IMPORTANT:** Streaming is configured via a **top-level `streams`** property in
`agent-card.json` -- NOT inside `capabilities`. The `capabilities` object only
accepts `taskKinds` and rejects all other fields (`additionalProperties: false`).

`ctx.createStream()` throws `"Streaming was not negotiated for this task."`
whenever `ctx.hasStream` is false — either the card lacks the `streams`
declaration, or (for request tasks) the consumer didn't opt in via
`extensions.blocks.stream`. Guard on `ctx.hasStream` before
calling it.

### agent-card.json (streams section)

For request-only agents (`taskKinds: ["request"]`), streams must contain only `_default` (schema-enforced).
Both `direction` and `format` are **required**.

```json
{
  "capabilities": {
    "taskKinds": ["request"]
  },
  "streams": {
    "_default": {
      "direction": "outbound",
      "format": "bytes",
      "description": "Main output stream"
    }
  }
}
```

Stream property options:
- `direction`: `"outbound"` | `"inbound"` | `"bidirectional"`
- `format`: `"bytes"` | `"events"`
- `description`: human-readable description (optional)
- `affinity`: `"shared"` | `"dedicated"` (optional; default `"dedicated"`). Shared-affinity streams are cross-task broadcasts with a single writer per agent; they are **pipe-only** (the SDK throws on request tasks) and do not publish a `stream_end` marker on per-task cleanup. See the SDK contract for the full stream lifecycle.
- `schema`, `outboundSchema`, `inboundSchema`: JSON Schema (for `events` format only)
- `contentType`: string (for `bytes` format only)

**Format constraints** (enforced by the schema):
- **Event streams**: `contentType` is NOT allowed. Unidirectional (`outbound`/`inbound`) use `schema`; bidirectional MUST use `outboundSchema` + `inboundSchema` instead.
- **Byte streams**: `schema`, `outboundSchema`, `inboundSchema` are NOT allowed.

### Handler: creating a stream

`createStream()` takes options only. Use `declaredStream` to pick which card-declared stream to open (omit when the card declares a single stream):

```typescript
const stream = await ctx.createStream({
  declaredStream: 'my-stream',
  bundleSizeBytes: 4096,
  maxLatencyMs: 100,
});

stream.write(JSON.stringify(data) + '\n');
await stream.end();
```

### Trigger: consuming a stream

Use `waitForStream()` to discover streams and the high-level consumer APIs
to decode data:

```typescript
const ref = await session.waitForStream();
const stream = ref.open();

// For bytes format streams (yields Uint8Array, browser-safe):
for await (const chunk of stream.bytes()) {
  process.stdout.write(chunk); // Node
  // In a browser, decode with: new TextDecoder().decode(chunk)
}

// For events format streams (browser-safe):
for await (const event of stream.events<MyType>()) {
  console.log(event);
}

// Or pipe to a file (Node-only — `readable()` returns a Node.js Readable):
const readable = await stream.readable();
readable.pipe(createWriteStream('./output.bin'));
```

The low-level `stream.inbound` iterator is still available for advanced use. Its `InboundMessage` is a `format`-discriminated union: `.data` is `string[]` for `bytes`, `unknown[]` for `events`, and `Record<string, unknown>` for `raw`.
---

## Consumer Auth Patterns

Three browser/runtime patterns are supported (see the SDK contract for
the full taxonomy):

1. **API key** (`TaskClient.create({ apiKey })`) — for Node trigger
   scripts, CI, server-side jobs. **Never** embed an API key in
   browser code.
2. **`tokenEndpoint`** (Mode 2 / `browser_sdk`) — the developer
   operates their own backend that mints short-lived JWTs from their
   API key. The browser calls `tokenEndpoint` to refresh. Used by
   the in-tree dashboard and any partner with their own server.
3. **Embedded auth** (Mode 3 / partner-hosted static page) — drop-in
   `BlocksAuth` widget served at `app.blocks.ai/embed/auth.<version>.min.js`
   (or `npm i @blocks-network/embed-auth`). Call
   `BlocksAuth.signInAndGetClient({ agent })` (one agent) or
   `BlocksAuth.signInAndGetClients({ agents })` (several); the widget runs a
   popup handshake against `/api/v1/auth/embed/popup`, gets short-lived
   per-agent JWTs back via `postMessage`, and returns ready-to-use
   `TaskClient` instances. No backend required on the partner side.
   Authoring flow: `blocks init my-ui --mode webapp --agent <name>` (writes
   `blocks.config.json` with a required `backendBaseUrl` — the runtime backend
   API origin, resolved from `--backend-url`/`BLOCKS_BACKEND_URL`/active profile
   and distinct from the `--blocks-base-url` asset host — plus a generated
   `web/`; pipe-capable agents get a duration control, minutes 1..43200), then
   `blocks dev` /
   `blocks deploy <partner>` (positional target — cloudflare, vercel,
   netlify, or a user-defined plugin; for non-interactive deploys set
   `CLOUDFLARE_API_TOKEN` / `VERCEL_TOKEN` / `NETLIFY_AUTH_TOKEN`). See
   `blocks-sdk/embed-auth/README.md` for the widget API,
   `docs/embed-getting-started.md` for the wire-level pattern.

## Trigger Scripts (Consumer Task Submission)

Trigger scripts submit tasks to agents using `TaskClient.create()`
with an API key. The SDK handles JWT acquisition, token refresh, and
owner identity automatically.

```typescript
import 'dotenv/config';
import { TaskClient, textPart } from '@blocks-network/sdk';

const client = await TaskClient.create({
  billingMode: 'free',
  apiKey: process.env.BLOCKS_API_KEY!,
});

const session = await client.sendMessage({
  agentName: 'my-agent',
  requestParts: [textPart('Hello from trigger!')],
});

console.log('Task created:', session.taskId);

const terminal = await session.waitForTerminal(60_000);
console.log('Task finished:', terminal.state);

const artifacts = session.listArtifacts();
for (const ref of artifacts) {
  const downloaded = await session.downloadArtifact(ref);
  console.log('Artifact:', downloaded.fileName, downloaded.data.length, 'bytes');
}

await session.asyncClose();
client.destroy();
```

---

## TaskClient & TaskSession

**sendMessage(params)** -- required params: `agentName`, `requestParts`. Optional: `ownerId` (auto-populated from auth), `idempotencyKey`, `taskKind` (`'request'`|`'pipe'`), `duration`, `consumerPublicKey`, `stream` (request-task streaming opt-in — `true` requests streaming, `false` suppresses it; omitted uses the server default, now no-streaming, so pass `true` to stream; ignored for pipe; resolved `hasStream` still requires agent capability), `pushNotificationConfig`, `retryPolicy`, `autoDrain`, `drainWindowMs` (default 30_000; overrides the per-session auto-drain window for already-open streams).

**TaskClient.connect({ taskId, autoDrain?, drainWindowMs?, role? })** -- returns a `TaskSession`. `drainWindowMs` mirrors the `sendMessage` option so reconnecting consumers can tune the drain window for streams they open via `openAllStreams()` / `onStream`. `role` defaults to `'consumer'` (task submitter — server checks `userId === task.ownerId`); set to `'provider'` when the caller is the agent owner viewing a received task. Provider-role access follows the same rule as the Dashboard's received-tasks view: the agent's owner is always admitted. Otherwise the agent's org must match the active org resolved from the session `X-Active-Org` header or the credential's org claim, AND the caller must be a current member of that org; admins with an admin-typed active org get the cross-org bypass, and when no active org is resolved at all (legacy callers) the server falls back to admin-bypass / membership on the agent's org. For a private agent, membership alone is not enough: a non-admin must also hold an agent-management permission in the owning org and have been invited to the agent.

**TaskSession** -- returned by `sendMessage()`. Properties: `taskId`, `ownerId`, `orgId`, `readToken`, `statusChannel`, `state`, `isClosed` (getter; `true` after `close()` / `asyncClose()` runs). Event listeners: `onProgress(cb: (e: ProgressEvent) => void)`, `onArtifact(cb: (e: ArtifactEvent) => void)`, `onTerminal(cb: (e: TerminalEvent) => void)`, `onCancelRequested(cb: (e: CancelRequestedEvent) => void)`, `onEvent(cb)`, `onError(cb)`, `onStream(cb)`. Blocking wait: `waitForTerminal(timeoutMs?)` -- returns `Promise<TerminalEvent>`, resolves immediately for already-terminal sessions. History helpers: `listEvents()` (all valid task events parsed by `connect()` history), `listArtifacts()`, `downloadArtifact(ref)`, `saveArtifacts(dir)`. Stream helpers: `listStreams()`, `waitForStream(id?)`, `waitForStreamWhere(predicate)`, `openAllStreams(opts?)` (active-session eager-open — returns `StreamClient[]` for every readable ref, skipping outbound-only and already-ended refs). Card lookup: `client.getAgentCard(agentName)` (forwards the client's credential, which a Blocks Enterprise deployment requires to return a card at all; raises `AuthRefreshFailedError` if a configured credential cannot be produced, so `null` only ever means "no such agent"). Control: `cancel()`, `terminate()`, `close()`, `asyncClose()`. Resource management: `Symbol.dispose` (TaskClient), `Symbol.asyncDispose` (TaskSession).

**At-most-once `onTerminal`.** `session.onTerminal`, `session.waitForTerminal()`, `TaskClient.subscribeToTask`'s `onTerminal`, and the synthetic re-emit on registration against an already-terminal session each fire at most once per task. First-terminal-wins; subsequent wire-level terminals are silently dropped (e.g. scanner force-cancel + agent's delayed terminal).

**`onCancelRequested`** — backend-published acknowledgment of a cooperative cancel on `u.{orgId}.{taskId}`. Fires zero or once per session; suppressed once a terminal has been delivered. Event shape surfaced to the callback: `{ type: 'cancel_requested', taskId, ts }`. (The wire payload also carries `protocolVersion` per the SDK task-event schema, but it is not surfaced to the callback.) Use to render an in-flight "cancel requested" UI signal before any terminal arrives. **Late registration:** callbacks registered after the wire event arrived still receive a synthetic replay of the first event, mirroring `onTerminal`'s sticky behavior — but only while no terminal has been delivered (a post-terminal registration gets nothing, preserving causality).

`onArtifact(cb)` replays pre-populated artifacts synchronously at registration time, in the same spirit as `onStream()` and sticky `onTerminal()`. Replay events are minimal synthetic artifact events with `type`, `taskId`, and `artifactRef`; original history-only wire fields such as `outputId` and `protocolVersion` are not retained.

**Part helpers:** `textPart(text, partId?)`, `filePart(data, opts?)` (sync, universal — accepts `Uint8Array | ArrayBuffer | Blob | File`), `filePartFromPath(path, opts?)` (async, Node-only — reads via lazy `node:fs`) -- all exported from `@blocks-network/sdk`.

**Artifact helpers:** `buildArtifactRef(data, mimeType, fileName?)` constructs an `ArtifactRef` from raw bytes (used internally by handler return paths and exposed for advanced consumers). `shouldInlineArtifact(sizeBytes)` returns `true` when the size is below the 16 KB inline threshold — matches the SDK's auto-routing decision. `decodeInlineArtifact(ref)` synchronously decodes an inline `ArtifactRef` to `Uint8Array` (no network). `downloadArtifact(ref)` is the standalone async fetcher for file-class refs (the same logic `session.downloadArtifact()` calls). All four are exported from `@blocks-network/sdk`.

**Auth — `tokenEndpoint`:** accepts either a string URL or a `TokenEndpointConfig` object (`{ url, credentials?: 'include' | 'same-origin' | 'omit', headers?, body? }`). The config form supports cookie-based auth (`credentials: 'include'`) and custom CSRF headers.

**Auth — surfacing refresh failures:** If proactive refresh fails 3 times in a row, or a reactive (on-401) refresh fails, the SDK records an `AuthRefreshFailedError` on the underlying `ConsumerAuth`. When the trigger was a reactive refresh, the call that hit the 401 -- or the auth-revoke RPC code that made it refresh-retryable -- fails immediately with that original error, untyped. The typed error is conditional: a later call's preflight retries the refresh once and raises only if that also fails, clearing the recorded error if it succeeds. The next authenticated `TaskClient` call (`sendMessage`, `connect`, `getTask`, `listTasks`, `cancelTask`, file-upload helpers, etc.) runs through a shared preflight that first attempts one reactive-recovery refresh; if that recovery succeeds, the recorded error clears and the call proceeds, and if it fails, the typed `AuthRefreshFailedError` is thrown (the error is exported from `@blocks-network/sdk`). Register `onAuthError` on `TaskClient.create()` if you want a proactive-path hook (it does not fire for reactive failures) for re-auth UX; the preflight is the safety net for callers who don't and the recovery path for transient outages that resolve before the next call.

**Stream consumer APIs:** `stream.bytes()` (decoded byte iterator, yields `Uint8Array`, browser-safe), `stream.events<T>()` (flattened event iterator, browser-safe), `stream.readable()` (Node Readable adapter, returns `Promise<Readable>` — **Node-only**, not for browser bundles). All iterators deliver messages in sequence order via the SDK's reorder buffer. `ref.open({ reorderTimeoutMs })` configures the gap timeout (default 750ms; 0 disables reordering).

**Stream error surfaces:** `StreamClient.onError(cb: (err: StreamError) => void)` subscribes to per-stream PubNub status errors (PAM revocation, network failures, malformed payloads). `StreamError` is `{ category, error, channel, timestamp, fatal }`; `category` is one of the neutral values `"connected"`, `"reconnected"`, `"network_down"`, `"network_issues"`, `"timeout"`, `"malformed_response"`, `"access_denied"`, `"bad_request"`, `"other"`. Fatal categories that force-terminate the stream are `"access_denied"` and `"bad_request"`; on `fatal: true` the client auto-tears down and signals iterator completion, so handlers typically only react to non-fatal errors. Distinct from `TaskSession.onError` (which surfaces consumer-callback exceptions, not stream-level failures). `StreamUnavailableError` (exported from `@blocks-network/sdk`) is thrown synchronously by `StreamRef.open()` when the owning session is already terminal; it carries `.terminalState` and `.streamId` so callers can branch on `instanceof StreamUnavailableError`.

**Exported error types:** all are `instanceof Error` and exported from `@blocks-network/sdk`.

| Class | Thrown by | Meaning |
|---|---|---|
| `RpcError` | any SDK call that contacts the backend | A non-2xx HTTP response from the Blocks REST API. Carries `status`, `code`, `details`. |
| `BillingModeMismatchError` | `TaskClient.sendMessage`, `TaskClient.connect` | The `billingMode` passed to `TaskClient.create()` does not match the target agent's registered mode. Carries `expected` / `got`. |
| `AnonTaskAccessDeniedError` | `TaskClient.connect` (anon role) | A 403 from `/api/v1/auth/anon-task-read-token` — the anon-readable channel rejected the fingerprint. |
| `StreamUnavailableError` | `StreamRef.open()` | The owning session is already terminal; live stream data is gone (artifacts persist). Carries `.terminalState` and `.streamId`. |
| `AgentAuthFatalError` | `AgentAuth` connect/refresh path | Fatal, non-retryable — the API key was revoked/disabled (`API_KEY_INVALID`) or an administrator forced the agent offline (`AGENT_FORCED_OFFLINE`). The runtime terminates the process (`process.exit(1)`). Transient connect failures (network, 5xx, `404 not-published`) are NOT fatal and do not block startup. |

---

## Environment Variables (.env)

Auto-populated by `blocks init` or by `blocks login --write-env` (opt-in).
Use `--dir <path>` to target a specific directory. Contains
`BLOCKS_API_KEY`, plus `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` when the
login targeted a specific deployment rather than stock Blocks Network (a
`--network` login writes neither and removes stale values). All three go in
as one rewrite. That rewrite leaves one uncommented assignment of each in
the file — under any spelling of the name, since for a variable whose name
uppercases to `BLOCKS_*` the CLI treats `blocks_api_key` and
`BLOCKS_API_KEY` as one variable, and `blocks logout` removes both. Two
limits belong with that: outside `BLOCKS_*` each spelling is its own
variable, so "assigned once" is a per-spelling statement for an application
variable; and the fold applies on every platform, so on macOS and Linux a
lowercase `blocks_...` line added by hand is one `blocks login` will
overwrite and `blocks logout` will delete. A value containing a line break
or a NUL byte — which would be read back as a second assignment — is refused
by name with nothing written. Note what a project `.env` does and does not
configure: the CLI imports only its own seven `BLOCKS_*` settings from that
file, and everything else in it is passed on to the agent process `blocks
run` starts rather than being applied to the CLI. Proxy, certificate-trust,
home/config-location and hosting-partner-token variables therefore have to
be exported in your shell to affect the CLI; the file prints a note on
stderr when it supplies one.

Additional env vars read by the SDK:

- `BLOCKS_CDM_URL` -- CDM config endpoint, which is what selects the
  real-time keysets. Unset, the SDK uses a hardcoded default serving
  **Blocks Network** keysets; `BLOCKS_BACKEND_URL` overrides only the REST
  API origin and does not change this. `blocks run` sets both for the
  delegated process whenever the invocation targets a named deployment
  (stock Blocks Network needs no injection — the SDK default already
  resolves it), so an agent started that way needs neither. It sets them as
  defaults: a non-empty value the child already carries for either variable
  wins. For a process
  you launch directly (`npx tsx trigger.ts`) both come from `.env`, where
  `blocks login --write-env` writes them. Every deployment serves the
  endpoint at `<origin>/api/v1/cdm`, so a hand-written value is just that.
  Note that a `BLOCKS_BACKEND_URL` or `BLOCKS_CDM_URL` a project `.env`
  supplied -- as opposed to one exported in your shell, which these two
  gates leave alone -- is declined by the CLI unless it agrees with a
  deployment you chose: for the backend URL, any deployment some saved
  profile describes (not necessarily the active profile's); for the CDM URL,
  the CDM endpoint of the deployment this command is actually targeting, so
  on stock Blocks Network there is no such origin to compare against and a
  file-supplied value is declined there. The export precedence belongs to
  those two gates: naming a target on the `blocks login` command line drops
  an ambient `BLOCKS_CDM_URL` answering for a different deployment however
  you set it. So a `.env` from a cloned repository does not redirect a
  command at a deployment you never named
- `LOG_LEVEL` -- error/warn/info/debug (default info)
- `BLOCKS_DEBUG_INTERNAL` -- comma-separated debug subsystems. Values: `diagnostics` (transport-status listener — connectivity transitions and alive snapshots; **Node SDK only**), `forward_transport` (surface the underlying transport's own log output; **both SDKs** — Node forwards it through the Blocks logger under `[Transport]`, Python stops filtering the `httpx`/`httpcore` request lines; see `python-reference.md`). Neither implied by `LOG_LEVEL=debug`. See the SDK contract for the canonical definition.
- `BLOCKS_PROFILE` -- comma-separated opt-in local profilers. `timing` makes the runtime log one `dispatch timing` line per task with `received_to_running_ms` / `running_to_handler_ms` / `received_to_handler_ms` (single process clock, skew-free). **Local-only, no egress**; off by default; for bench use. See the SDK contract
- `STREAM_BUNDLE_SIZE` -- stream flush byte threshold (default 4096)
- `STREAM_MAX_LATENCY_MS` -- stream flush time threshold in ms (default 250)
- `STREAM_MAX_MESSAGE_SIZE` -- max message size before multipart splitting (default 16384)
- `STREAM_GATING` -- presence gating for streams (default true)

---

## Transport-Layer Resilience

The agent's long-lived PubNub control client retries subscribe failures with an unbounded budget by default (~30 days at a 60s cap). Brief network outages — VPN reconnects, gateway flaps, transient ISP failures — no longer silently park the agent after the underlying transport's vanilla ~4-6 minute retry window exhausts. The control client emits two **always-on** structured log events at the prevailing `LOG_LEVEL`: `transport_degraded` (warn-level) when the mapped neutral category enters `network_down` / `network_issues` / `timeout` / `malformed_response`, and `transport_restored` (info-level) on `reconnected`. These let an operator distinguish "agent retrying" from "agent dead" without opting into anything. Deeper signal — connectivity transitions and per-client alive snapshots — is surfaced as structured `transport_status_transition` and `transport_alive_snapshot` events when `BLOCKS_DEBUG_INTERNAL=diagnostics` is set (off at any `LOG_LEVEL` by default). To additionally see the underlying transport's own log lines, set `BLOCKS_DEBUG_INTERNAL=forward_transport` — those lines route through the Blocks logger under the `[Transport]` tag, filtered by `LOG_LEVEL`. (The Python SDK uses a different mechanism — see `python-reference.md`.)

Per-task and per-stream PubNub clients keep the SDK's default short retry budget — a stuck task should fail fast rather than retry indefinitely. This split is enforced internally and not configurable from the handler.

This behavior is automatic; no agent-card or env-var changes are required.

---

## Pipe Tasks (Provider Handler)

Pipe tasks are long-running streaming tasks with an explicit duration. The
handler loops until cancelled or expired, streaming data to consumers.

**Key differences from request tasks:**
- Handler voluntary return does NOT auto-publish a terminal event
- Streams continue running after handler returns
- `duration` (minutes) is set by the consumer; available as `task.duration`
- The runtime only publishes terminal on cancel, expire, or terminate

```typescript
export default async function handler(
  task: StartTaskMessage, ctx?: TaskContext,
): Promise<HandlerResult> {
  if (!ctx) throw new Error('TaskContext required for pipe tasks');

  ctx.reportStatus('Starting stream...');
  const stream = await ctx.createStream({
    declaredStream: 'data-stream',
    format: 'events',
    bundleSizeBytes: 4096,
    maxLatencyMs: 100,
  });

  // Long-running loop -- exits on cancel or duration expiry
  try {
    while (!ctx.cancelSignal.aborted) {
      stream.write({ ts: Date.now(), value: Math.random() });
      await sleepMs(1000, ctx.cancelSignal);
    }
  } catch (err) {
    if (!(err instanceof Error && err.message === 'aborted')) throw err;
  }

  await stream.end();

  const reason = ctx.isExpired ? 'duration_expired'
    : ctx.isCancelled ? 'canceled' : 'stopped';
  ctx.reportStatus(`Stream ended: ${reason}`);

  return {
    artifacts: [{
      data: JSON.stringify({ reason }),
      mimeType: 'application/json',
    }],
  };
}

function sleepMs(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', onAbort);
      reject(new Error('aborted'));
    };
    signal.addEventListener('abort', onAbort, { once: true });
  });
}
```

---

## Sending Pipe Tasks (Consumer)

Consumers create pipe tasks by setting `taskKind: 'pipe'` and providing a
`duration` (required, 1-43200 minutes). They then discover and consume
the provider's stream.

```typescript
const session = await client.sendMessage({
  agentName: 'my-pipe-agent',
  taskKind: 'pipe',
  duration: 5,  // minutes (required for pipe tasks)
  requestParts: [textPart('start streaming')],
});

// Discover the stream
const streamRef = await session.waitForStream();
const stream = streamRef.open();

// Consume inbound data using high-level API
for await (const event of stream.events()) {
  console.log('Received:', event);
}

// Wait for terminal
const terminal = await session.waitForTerminal();
console.log('Task ended:', terminal.state);
session.close();
client.destroy();
```

**InboundMessage** shape: discriminated union by `format`. Fields: `data` (`string[]` for `bytes`, `unknown[]` for `events`, `Record<string, unknown>` for `raw`), `seq`, `ts`, `format`, `encoding`. Exported from `@blocks-network/sdk` and from `@blocks-network/sdk/stream`.

**Auto-drain**: When the terminal event arrives, the session waits up to
the configured drain window (default **30 seconds**; override per session
via `drainWindowMs` on `TaskClient.sendMessage()` and `TaskClient.connect()`)
for open streams to deliver remaining data before closing.

---

## TaskClient.create() Static Factory

Create a `TaskClient` with automatic CDM config resolution and auth:

```typescript
const client = await TaskClient.create({
  billingMode: 'free',  // required: 'free' | 'paid'
  apiKey: 'blocks-api-key',  // auth mode 1: API key → JWT
  // tokenEndpoint: 'https://my-backend/token',  // auth mode 2: session-authenticated backend endpoint (customer proxy OR dashboard embedder — see the SDK contract)
  // tokenProvider: async () => ({ token, expiresAt }),  // auth mode 3: custom
});
```

- `billingMode` is required ('free' → playground keyset, 'paid' → network keyset) and must match the target agent's server-derived billingMode (exception: authenticated same-org callers are exempt from this check). Read it from the registry. `getAgent()` has no default backend URL and throws `baseUrl is required` without one, so take the Blocks Network backend URL from the CDM; pass `apiKey` as well when the agent is private, otherwise the lookup returns `null`:

  ```ts
  import { fetchCdmConfig, getAgent } from '@blocks-network/sdk';

  const { api } = await fetchCdmConfig();
  const entry = await getAgent(name, {
    baseUrl: api.baseUrl,
    apiKey: process.env.BLOCKS_API_KEY,
  });
  const billingMode = entry?.billingMode;  // undefined when the agent is unknown or not visible
  ```

  The list helpers (`fetchAgentRegistry`, `fetchAgentsByTag`, `fetchAgentsByListing`) take the same `baseUrl` / `apiKey` options.
- Exactly one auth mode: `apiKey`, `tokenEndpoint`, or `tokenProvider`
- `rpcHeaders?: Record<string, string>` — optional extra request headers merged onto every RPC call (not the token mint). Merged UNDER SDK-owned headers, so a caller cannot override `Authorization`, `Content-Type`, `Blocks-Protocol-Version`, or `X-Write-Affinity` (case-insensitive). Request metadata, not auth; the canonical dashboard use is `X-Active-Org` to scope a multi-org user's submission to the agent's owning org.
- Returns `Promise<TaskClient>`

---

## TaskClient.connect() -- Reconnecting to Existing Tasks

Reconnect to an active or completed task by ID:

```typescript
const session = await client.connect({ taskId: 'task-abc-123' });

// For terminal tasks: history is preloaded, no live events
const events = session.listEvents();
const artifacts = session.listArtifacts();
const streams = session.listStreams();

// For active tasks: history preloaded + live subscription
session.onArtifact((event) => console.log('Artifact:', event));
session.onStream((streamRef) => {
  const stream = streamRef.open();
  // consume stream...
});

// Provider (agent owner) viewing a received task:
const providerSession = await client.connect({
  taskId: 'task-abc-123',
  role: 'provider',
});
```

- Requires JWT-based auth (`apiKey`, `tokenEndpoint`, or `tokenProvider`
  via `TaskClient.create()`). `AgentAuth` is not supported for `connect()`.
- `role` defaults to `'consumer'` (task submitter — server checks
  `userId === task.ownerId`). Set to `'provider'` when the caller owns
  the agent that received the task. Provider-role access follows the same rule as the Dashboard's
  received-tasks view: the agent's owner is always admitted. Otherwise
  the agent's org must match the active org resolved from the session
  `X-Active-Org` header or the credential's org claim, AND the caller
  must be a current member of that org; admins with an admin-typed
  active org get the cross-org bypass, and when no active org is
  resolved at all (legacy callers) the server falls back to admin-
  bypass / membership on the agent's org. For a private agent,
  membership alone is not enough: a non-admin must also hold an agent-
  management permission in the owning org and have been invited to the
  agent.
- Terminal tasks: preloads events/artifacts/streams from history, no live events
- Active tasks: preloads history, then subscribes from cursor (no gap)

---

## Consumer Stream Discovery

Multiple ways to discover and consume streams on a `TaskSession`:

```typescript
// Wait for any stream (resolves on first stream_started event)
const streamRef = await session.waitForStream();

// Wait for a specific stream by declared key or runtime ID
const streamRef = await session.waitForStream('data-stream');

// Wait for a stream matching a predicate
const streamRef = await session.waitForStreamWhere(
  (ref) => ref.descriptor.format === 'events',
);

// High-level consumption
const stream = streamRef.open();
for await (const chunk of stream.bytes()) {      // Uint8Array, browser-safe
  process.stdout.write(chunk);
}
for await (const event of stream.events()) {     // flattened events, browser-safe
  console.log(event);
}
const readable = await stream.readable();        // Node-only Readable adapter
readable.pipe(createWriteStream('./output.bin'));

// List already-discovered streams
const allStreams = session.listStreams();
```

- `streamRef.open()` returns a `StreamClient`; idempotent while active
- `stream.inbound` is still available as the low-level `AsyncIterable<InboundMessage>` (`InboundMessage` is exported from `@blocks-network/sdk` and `@blocks-network/sdk/stream`)
- `stream.descriptor.declaredStream` carries the card-declared stream key

---

## Task Lifecycle Methods

### On TaskClient

```typescript
await client.getTask(taskId);          // returns TaskInfo
await client.listTasks({               // returns ListTasksResult
  ownerId, agentName, state, limit, cursor,
});
await client.cancelTask(taskId);
await client.pauseTask(taskId);
await client.resumeTask(taskId);
await client.retryTask(taskId);
await client.terminateTask(taskId);
```

### On TaskSession

```typescript
await session.cancel();     // request cancellation
await session.terminate();  // force terminate
session.close();            // unsubscribe and clean up
```

---

## CLI Commands

Always use `--language node` when scaffolding. Do NOT mkdir before `blocks init` -- it creates the directory.

### Global flags

Accepted by every command, before or after the subcommand:

- `--profile <name>` -- the deployment profile to use; also names the
  profile `blocks login` creates. Outranks `BLOCKS_PROFILE` and the saved
  active profile.
- `--no-input` -- ask the CLI not to read stdin. Where it is honoured, a
  prompt that would still be required fails with an actionable error naming
  the flag that answers it, whether or not stdin is a terminal. That last
  clause is the behaviour to plan for on `blocks login`: its deployment
  question and its `.env` question used to fall back to a silent default off
  a terminal and are now errors under this flag, so pass `--network` (or an
  instance argument) and `--write-env` / `--no-write-env`. A key passed as
  `--api-key` / `--api-key-stdin` answers both instead; `BLOCKS_API_KEY` in
  the environment does not. Without the flag, a non-TTY run still defaults
  silently. A refusal names a flag, or — for a hosting partner's API token and
  for a deploy plugin's declared `credentialEnvVar` — an environment variable.
  Coverage is **not** universal, so it is not a guarantee that a command cannot
  block: the flag's own help text (`blocks --help`), which tracks the code, is
  the current list. The only gaps are the organization picker the browser login
  shows for a multi-organization account (skip it with `--api-key` /
  `--api-key-stdin`; nothing else answers it) and the token prompt for
  `blocks login --provider cloudflare|vercel|netlify`. `blocks deploy` reads no
  stdin under the flag, though not every read there becomes an error: a missing
  deploy target falls back to the positional argument or `deployTarget` in
  `blocks.config.json`, and the post-deploy agent-card question is skipped with a
  note on stderr while the deploy still exits `0` — that question comes after the
  upload, so it cannot decide whether the command failed (`--no-card-update`
  states the intent and silences the note; a malformed `--card-path` *is* refused,
  before anything is uploaded). One refusal there has no answer at all: a `web/`
  bundle baked
  for a different backend than the one now targeted is a hard failure under
  `--no-input`, with no override flag, since only re-scaffolding or retargeting
  can make the two agree.

The CLI also loads a `.env` from the current directory before any command
runs, without overriding variables the environment already carries and
skipping empty assignments -- which is how the `BLOCKS_API_KEY`,
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` that `blocks login --write-env`
leaves behind take effect on later commands. Such a file configures the
**agent**, and can also retarget the CLI through a fixed list: it imports the seven settings it consumes
itself (`BLOCKS_API_KEY`, `BLOCKS_BACKEND_URL`, `BLOCKS_CDM_URL`,
`BLOCKS_PROFILE`, `BLOCKS_APP_BASE_URL`, `BLOCKS_DASHBOARD_URL`,
`BLOCKS_CLI_CLIENT_ID`), and everything else stays out of its process while
still being merged into the environment of the agent process `blocks run`
starts. So an agent's own configuration keeps arriving from `.env`, and nothing
a cloned repository ships can change the transport the CLI uses, which
certificates it trusts, where it keeps credentials, or what it executes. Being
a `BLOCKS_*` name is not the test -- `BLOCKS_INSTALL_DIR` is not on the list.
Four of the seven do retarget the CLI, so a cloned `.env` is not inert; a pin
naming a deployment no saved profile describes is dropped rather than obeyed,
with a note naming the file, the variable and the deployment used instead.
Names commonly seen in a `.env` get an explanatory note on stderr when the file
supplies them -- the proxy and certificate settings (`HTTP_PROXY`,
`HTTPS_PROXY`, `ALL_PROXY`, `NO_PROXY`, `SSL_CERT_FILE`, `SSL_CERT_DIR`,
`GODEBUG`), the state-location ones (`XDG_CONFIG_HOME`, `HOME`, `USERPROFILE`),
the hosting-provider tokens (`CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`,
`VERCEL_TOKEN`, `VERCEL_TEAM_ID`, `NETLIFY_AUTH_TOKEN`) and
`BLOCKS_INSTALL_DIR` -- naming the variable and the file, with export as the
remedy. Matching is case-insensitive. The consequence that matters in practice:
`blocks deploy` reads a partner token from the environment, so one in a project
`.env` is not used -- export it, as the deploy section above already says.

The two targeting variables carry one exception each when the value came
from that file rather than from your shell (an exported value wins at both
of these gates, whichever command is running; the one input above even an
export is a target named on the `blocks login` command line, described after
this list):

- `BLOCKS_BACKEND_URL` is honoured when **any** saved profile describes the
  deployment it names -- i.e. you have logged in there at some point. It does
  not have to match the *active* profile.
- `BLOCKS_CDM_URL` is honoured only when it is the CDM endpoint of the
  deployment the command is actually targeting. Stock Blocks Network has no
  such origin to compare against, so a file-supplied value is declined there.

The CLI drops anything else and prints what it dropped, so a `.env` carried
in a cloned repository does not point a command -- or a credential -- at a
deployment you never named. Export the value in your shell, or log in to
that deployment (or pin the deployment that serves that CDM endpoint), to
make it authoritative. A login that names its target explicitly additionally
unsets an ambient `BLOCKS_CDM_URL` for that login, because the payload
behind it names both an API origin and an OAuth client id and would
otherwise redefine the deployment just named: `--network` (or picking Blocks
Network at the prompt) drops any such value, and an instance argument drops
any value that is not that instance's own CDM endpoint. Those drops are the
one place an exported value does not win: they do not weigh provenance,
because a choice made on this invocation outranks ambient configuration.
They affect that login process only.

### Scaffold

```bash
blocks init <name> --yes --language node                  # Provider scaffold (handler + agent-card.json)
blocks init <name> --yes --language node --mode consumer  # Consumer scaffold (index.ts using TaskClient)
```

`blocks init` defaults `--mode provider`. Consumer projects produce
`index.ts` plus a `package.json` with a `start` script -- no
`agent-card.json`, no handler, no publish step. The non-interactive
mode requires the name argument; without it, the CLI errors out.

### Authenticate

```bash
blocks login --network --write-env  # Blocks Network; write BLOCKS_API_KEY to .env
blocks login acme --write-env       # Enterprise short name -> https://acme.blocks.ai
blocks login https://blocks.acme.com --write-env       # Enterprise custom domain
blocks login https://blocks.acme.com --profile acme    # Store it under a custom profile name
blocks login --write-env --dir ./x  # Write .env to a specific directory
blocks login --no-write-env         # Authenticate without touching .env (skips prompt)
blocks login --api-key "$KEY" --write-env       # Skip browser flow with a pre-issued key
echo "$KEY" | blocks login --api-key-stdin --write-env  # Read key from stdin
blocks whoami                       # Print org, key id, expiry
blocks whoami --json                # Structured: org_name, org_id, key_id, expires_at, days_remaining, expired
blocks logout                       # Clear the profile's cached keys + remove BLOCKS_API_KEY from .env
```

`blocks login` prompts in a TTY for: which deployment to target (skipped once the
question is settled -- by any completed login, including one to Blocks Network,
which stores no deployment URL of its own; or by a deployment this invocation
resolves -- the active profile, `BLOCKS_BACKEND_URL`, or a project `.env` pinning a
deployment you have a profile for, so a bare `blocks login` there returns to it
without asking. Only a genuine first run is asked), the instance URL or short name
if you pick Enterprise, and `Write credentials to project .env? (Y/n):`. In a
non-TTY shell (CI / coding agents) those are auto-skipped without writing --
it does not hang, but the deployment is then whatever the profile resolves
to; under `--no-input` they are errors instead. Answer them
with flags: `--network` (or an instance URL / short name argument, which
`--network` is mutually exclusive with) and `--write-env` /
`--no-write-env`. An instance argument that spells out an address is validated
before anything is sent:
`https://` is required except for `localhost` / `127.0.0.1` / `[::1]`, the
authority must be a host with an optional port in 1–65535 (not `user@host`),
a path prefix is kept but a query string or a fragment is refused, and a
short name must be a single DNS label expanding to a hostname within the
253-character DNS limit. An argument matching an existing profile name is not
an address and is not re-checked: it resolves to that profile's saved
deployment and is used as stored. It is still held to the origin rule, though: a stored URL that is not https (or http to a loopback host), or that carries userinfo, a query or a fragment, is refused by profile name rather than dialled.

`--write-env` writes `BLOCKS_API_KEY`, and the deployment's own
`BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` alongside it when the login
targeted a specific deployment rather than stock Blocks Network, so a
directly-launched script (`npx tsx trigger.ts`) sends its REST calls to the
deployment the key was minted at *and* subscribes on that deployment's
keysets. A `--network` login writes neither and removes stale values. All
three are applied in one edit, so an interrupted login does not leave a new
key beside another deployment's URLs — see [Environment Variables
(.env)](#environment-variables-env).

The key itself is stored in the deployment's profile in
`~/.config/blocks/contexts.json`, regardless of `--write-env`.
`blocks logout` clears that profile's cached org keys and strips
`BLOCKS_API_KEY` from the project `.env`, but **keeps the deployment
target** — it says so, and names `blocks profile remove <name>` for
forgetting the deployment entirely. It does not revoke the key on the
server. `blocks profile remove <name>` is the deliberate opposite: it drops each
of the project `.env`'s `BLOCKS_BACKEND_URL` and `BLOCKS_CDM_URL` that names the
deployment being removed — the two are judged independently — and the
`BLOCKS_API_KEY` beside them when the backend URL was one of them (the backend pin
is what records where that key is spent) or when the key is one only the removed
profile had cached. A `.env` whose only match is the CDM endpoint therefore loses
that line and keeps the key. It touches the file only when the deployment is
genuinely being forgotten: if another profile still describes it — an alias
alongside its host — the removal forgets a name, not a deployment, and the `.env`
is left as it is. Values naming any other deployment are left alone; the cleanup
runs before the profile is deleted, so a `.env` that exists but cannot be read or
rewritten fails the command with the profile still in place, rather than being
skipped silently. No `.env` at all is a silent success.

### Register (recommended first step)

```bash
blocks register                                 # Private + free; no listing/billing/terms prompts
blocks register --api-key "$KEY"                # Inline auth (skip blocks login)
echo "$KEY" | blocks register --api-key-stdin   # Inline auth via stdin
```

`blocks register` registers the agent as **private and free** (usable by
the owner and invited organizations only). It exposes only `--api-key`,
`--api-key-stdin`, and `--org-name` (the last inert on an Enterprise deployment, whose
organizations are pre-seeded by its admin) — no visibility or billing flags — and
has no visibility / billing / terms prompts, so non-interactive and CI
invocations succeed with no required flags. (Interactive runs may still prompt for an organization name on the first agent an org
registers or publishes — the same prompt `blocks publish` shows, and Blocks Network
only: an Enterprise deployment pre-seeds organizations, so it skips both that prompt
and `--org-name`.) Run `blocks publish` later to go
public or set pricing; it can also promote an already-registered agent.

### Unregister (remove an agent)

```bash
blocks unregister                               # Name read from ./agent-card.json
blocks unregister <agentName>                   # Remove an agent from anywhere
blocks unregister <agentName> --yes             # Skip the confirmation (required in CI)
blocks unregister <agentName> --api-key "$KEY"  # Inline auth (also --api-key-stdin)
```

The inverse of `blocks register`: removes the agent from the deployment
you are currently targeting. With no argument the name comes from
`identity.agentName` in `agent-card.json`. The deployment is printed before
the removal, which **cannot be undone**; no organization is named, because
the removal is authorized by your rights over this agent rather than by the
organization the key belongs to, and the confirmation prompt names the agent
itself. A non-interactive session refuses to remove anything without `--yes`.

### Publish (go public / set pricing)

```bash
blocks publish                                  # Interactive (prompts for billing, listing, terms)
blocks publish --billing-mode free \
  --listing public --accept-terms               # Free public agent (non-interactive)
blocks publish --billing-mode paid --listing private \
  --price-per-task 0.05 --accept-terms          # Paid private agent
blocks publish --api-key "$KEY"                 # Inline auth (skip blocks login)
echo "$KEY" | blocks publish --api-key-stdin    # Inline auth via stdin
```

Full publish flag set: `--billing-mode {free|paid}`, `--listing
{public|private}`, `--price`, `--price-per-task`, `--price-per-minute`,
`--free-units`, `--free-tasks`, `--free-minutes`, `--accept-terms`,
`--org-name`, `--api-key`, `--api-key-stdin`. Bare `blocks publish`
in a non-TTY shell does not prompt: a missing required value is an
immediate error naming the flag that supplies it -- always include
the relevant flags. On a deployment with no marketplace, billing is off
and `--billing-mode paid` is rejected before anything is sent, with an
error naming the value that works: publish free there (omit the flag,
or pass `--billing-mode free`).

`blocks publish` re-runs the same schema validation as `blocks check`
before contacting the registry, so `check` is a fast pre-flight, not
a hard gate.

### Validate, run, dashboard

```bash
blocks check                        # Validate agent-card.json + handler file existence
blocks run                          # Start agent from ./agent-card.json (delegates to language runner)
blocks dashboard                    # Open the agent's dashboard page in a browser
blocks dashboard <agent-name>       # Override the agent name (default: read from ./agent-card.json)
```

`blocks dashboard` resolves the dashboard URL from `BLOCKS_APP_BASE_URL`
/ `BLOCKS_DASHBOARD_URL` (or the active profile's dashboard origin) if
set, otherwise from the active deployment — `BLOCKS_BACKEND_URL`, the
active profile's backend, or the CDM config — so it targets the
deployment you're on rather than always stock Blocks Network. When
`BLOCKS_BACKEND_URL` targets a different backend than the active profile
was logged into, the profile's cached dashboard origin is skipped so the
link follows `BLOCKS_BACKEND_URL`. Export the env var to target
staging / a worktree / a self-hosted deployment. `blocks check`
validates JSON schema **and** the file referenced by `runtime.handler`.

### Manage private-agent grants (`blocks invite`)

When an agent is published with `--listing private`, its owner and the
other members of the organization that owns it reach it already; for
everyone else access is gated by per-user / per-org grants. The
`blocks invite` family manages those grants:

```bash
blocks invite send <agentName> --email user@example.com   # invite a specific user
blocks invite send <agentName> --org consumer-org-slug    # invite an entire consumer org
blocks invite list <agentName>                            # list unaccepted invitations, including expired
blocks invite grants <agentName>                          # list active grants (users + orgs)
blocks invite revoke <agentName> --email user@example.com # revoke a user grant
blocks invite revoke <agentName> --org consumer-org-slug  # revoke an org grant
blocks invite accept <token>                              # consumer-side: accept an invitation token
```

`--email` and `--org` are mutually exclusive on `send` / `revoke`. All
commands require prior `blocks login`.

### Maintenance

```bash
blocks upgrade                      # Self-update the CLI binary in place (POSIX installer flow)
blocks version                      # Print the installed CLI version
```

---

## Deployment

```bash
cd my-agent && npm install && npm start   # Local: run agent
npx tsx trigger.ts                         # Send test task
```

### Multi-instance

Each process auto-generates a unique instance ID (`AG-{agentName}-{uuid}`). Set `expectedInstances` in `agent-card.json` to match the number of instances.

```bash
npm start  # Terminal 1 (set expectedInstances in agent-card.json)
npm start  # Terminal 2
```
