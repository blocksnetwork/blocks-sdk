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

The handler-side stream wrapper. Mirrors the consumer-side `StreamClient` read/error/uuid surface so handler code uses the same iterators and callbacks consumers do (see `dev_docs/SDK_CONTRACT.md` §8.2.10 for the normative definition).

Properties: `streamId`, `channel`, `isActive`, `external`, `uuid` (read; same as `StreamClient.uuid`, useful for log correlation), `token` (external only).

Write / lifecycle: `write(data)` (throws on inbound-only and external streams), `end()`, `activate(opts?)` (external only), `onEnd(cb)`.

Read iterators (inbound and bidirectional streams):
- **Recommended:** `bytes()` for `format: 'bytes'` streams (yields `Uint8Array`, decodes base64/utf8 transparently). `events<T>()` for `format: 'events'` streams (yields one event per `for-await` iteration; flattens producer-side batches). `readable()` returns `Promise<node:stream.Readable>` for piping into Node file/process APIs.
- **Low-level:** `inbound` is an `AsyncIterable<InboundMessage>` over raw wire envelopes (`{ data, seq, ts, format, encoding }`). Reach for it only when you need raw envelope metadata; `bytes()` / `events()` are the everyday paths.

Errors: `onError(cb: (err: StreamError) => void)` subscribes to per-stream PubNub status errors. **Append-only** — register before the read path activates; past errors do not replay. On `fatal: true` the underlying client auto-tears down so the iterator exits cleanly.

`onInboundDone` is intentionally NOT part of `StreamObject` — it's an internal callback owned by `TaskSession` (SDK_CONTRACT §8.3.8). For a "stream drained" signal, `await` the for-await-of loop on `bytes()` / `events()` / `inbound`.

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

  if (ctx) {
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

Without the `streams` declaration, `ctx.createStream()` will throw:
`"Streaming was not negotiated for this task."`

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
- `affinity`: `"shared"` | `"dedicated"` (optional; default `"dedicated"`). Shared-affinity streams are cross-task broadcasts with a single writer per agent; they are **pipe-only** (the SDK throws on request tasks) and do not publish a `stream_end` marker on per-task cleanup. See `dev_docs/SDK_CONTRACT.md` §4.4.2, §8.4.1a, §8.7.3a, §8.7.4 for the lifecycle contract.
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

**sendMessage(params)** -- required params: `agentName`, `requestParts`. Optional: `ownerId` (auto-populated from auth), `idempotencyKey`, `taskKind` (`'request'`|`'pipe'`), `duration`, `consumerPublicKey`, `pushNotificationConfig`, `retryPolicy`, `autoDrain`, `drainWindowMs` (default 30_000; overrides the per-session auto-drain window for already-open streams).

**TaskClient.connect({ taskId, autoDrain?, drainWindowMs?, role? })** -- returns a `TaskSession`. `drainWindowMs` mirrors the `sendMessage` option so reconnecting consumers can tune the drain window for streams they open via `openAllStreams()` / `onStream`. `role` defaults to `'consumer'` (task submitter — server checks `userId === task.ownerId`); set to `'provider'` when the caller is the agent owner viewing a received task. Provider-role access is scoped by the caller's active org: the agent's org must match the active org resolved from the session `X-Active-Org` header or the credential's org claim, AND the caller must be a current member of that org. Admins with an admin-typed active org get the cross-org bypass; when no active org is resolved at all (legacy callers), the server falls back to admin-bypass / non-admin membership check on the agent's org.

**TaskSession** -- returned by `sendMessage()`. Properties: `taskId`, `ownerId`, `orgId`, `readToken`, `statusChannel`, `state`, `isClosed` (getter; `true` after `close()` / `asyncClose()` runs). Event listeners: `onProgress(cb: (e: ProgressEvent) => void)`, `onArtifact(cb: (e: ArtifactEvent) => void)`, `onTerminal(cb: (e: TerminalEvent) => void)`, `onEvent(cb)`, `onError(cb)`, `onStream(cb)`. Blocking wait: `waitForTerminal(timeoutMs?)` -- returns `Promise<TerminalEvent>`, resolves immediately for already-terminal sessions. History helpers: `listEvents()` (all valid task events parsed by `connect()` history), `listArtifacts()`, `downloadArtifact(ref)`, `saveArtifacts(dir)`. Stream helpers: `listStreams()`, `waitForStream(id?)`, `waitForStreamWhere(predicate)`, `openAllStreams(opts?)` (active-session eager-open — returns `StreamClient[]` for every readable ref, skipping outbound-only and already-ended refs). Card lookup: `client.getAgentCard(agentName)`. Control: `cancel()`, `terminate()`, `close()`, `asyncClose()`. Resource management: `Symbol.dispose` (TaskClient), `Symbol.asyncDispose` (TaskSession).

`onArtifact(cb)` replays pre-populated artifacts synchronously at registration time, in the same spirit as `onStream()` and sticky `onTerminal()`. Replay events are minimal synthetic artifact events with `type`, `taskId`, and `artifactRef`; original history-only wire fields such as `outputId` and `protocolVersion` are not retained.

**Part helpers:** `textPart(text, partId?)`, `filePart(data, opts?)` (sync, universal — accepts `Uint8Array | ArrayBuffer | Blob | File`), `filePartFromPath(path, opts?)` (async, Node-only — reads via lazy `node:fs`) -- all exported from `@blocks-network/sdk`.

**Artifact helpers:** `buildArtifactRef(data, mimeType, fileName?)` constructs an `ArtifactRef` from raw bytes (used internally by handler return paths and exposed for advanced consumers). `shouldInlineArtifact(sizeBytes)` returns `true` when the size is below the 16 KB inline threshold — matches the SDK's auto-routing decision. `decodeInlineArtifact(ref)` synchronously decodes an inline `ArtifactRef` to `Uint8Array` (no network). `downloadArtifact(ref)` is the standalone async fetcher for file-class refs (the same logic `session.downloadArtifact()` calls). All four are exported from `@blocks-network/sdk`.

**Auth — `tokenEndpoint`:** accepts either a string URL or a `TokenEndpointConfig` object (`{ url, credentials?: 'include' | 'same-origin' | 'omit', headers?, body? }`). The config form supports cookie-based auth (`credentials: 'include'`) and custom CSRF headers.

**Auth — surfacing refresh failures:** If proactive refresh fails 3 times in a row, the SDK records an `AuthRefreshFailedError` on the underlying `ConsumerAuth`. The next authenticated `TaskClient` call (`sendMessage`, `connect`, `getTask`, `listTasks`, `cancelTask`, file-upload helpers, etc.) runs through a shared preflight that first attempts one reactive-recovery refresh; if that recovery succeeds, the recorded error clears and the call proceeds, and if it fails, the typed `AuthRefreshFailedError` is thrown (the error is exported from `@blocks-network/sdk`). Register `onAuthError` on `TaskClient.create()` if you want a proactive hook for re-auth UX; the preflight is the safety net for callers who don't and the recovery path for transient outages that resolve before the next call.

**Stream consumer APIs:** `stream.bytes()` (decoded byte iterator, yields `Uint8Array`, browser-safe), `stream.events<T>()` (flattened event iterator, browser-safe), `stream.readable()` (Node Readable adapter, returns `Promise<Readable>` — **Node-only**, not for browser bundles). All iterators deliver messages in sequence order via the SDK's reorder buffer. `ref.open({ reorderTimeoutMs })` configures the gap timeout (default 750ms; 0 disables reordering).

**Stream error surfaces:** `StreamClient.onError(cb: (err: StreamError) => void)` subscribes to per-stream PubNub status errors (PAM revocation, network failures, malformed payloads). `StreamError` is `{ category, error, channel, timestamp, fatal }`; `category` is one of the neutral values `"connected"`, `"reconnected"`, `"network_down"`, `"network_issues"`, `"timeout"`, `"malformed_response"`, `"access_denied"`, `"bad_request"`, `"other"`. Fatal categories that force-terminate the stream are `"access_denied"` and `"bad_request"`; on `fatal: true` the client auto-tears down and signals iterator completion, so handlers typically only react to non-fatal errors. Distinct from `TaskSession.onError` (which surfaces consumer-callback exceptions, not stream-level failures). `StreamUnavailableError` (exported from `@blocks-network/sdk`) is thrown synchronously by `StreamRef.open()` when the owning session is already terminal; it carries `.terminalState` and `.streamId` so callers can branch on `instanceof StreamUnavailableError`.

**Exported error types:** all are `instanceof Error` and exported from `@blocks-network/sdk`.

| Class | Thrown by | Meaning |
|---|---|---|
| `RpcError` | any SDK call that contacts the backend | A non-2xx HTTP response from the Blocks REST API. Carries `status`, `code`, `details`. |
| `BillingModeMismatchError` | `TaskClient.sendMessage`, `TaskClient.connect` | The `billingMode` passed to `TaskClient.create()` does not match the target agent's registered mode. Carries `expected` / `got`. |
| `AnonTaskAccessDeniedError` | `TaskClient.connect` (anon role) | A 403 from `/api/v1/auth/anon-task-read-token` — the anon-readable channel rejected the fingerprint. |
| `StreamUnavailableError` | `StreamRef.open()` | The owning session is already terminal; live stream data is gone (artifacts persist). Carries `.terminalState` and `.streamId`. |
| `AgentAuthFatalError` | `AgentAuth` token-exchange path | The API key has been revoked / the org has been disabled. Non-retryable; the agent should exit. |

---

## Environment Variables (.env)

Auto-populated by `blocks init` or by `blocks login --write-env` (opt-in). Use `--dir <path>` to target a specific directory. Contains `BLOCKS_API_KEY`.

Additional env vars read by the SDK:

- `BLOCKS_CDM_URL` -- CDM config endpoint (defaults to production S3-hosted endpoint)
- `LOG_LEVEL` -- error/warn/info/debug (default info)
- `BLOCKS_DEBUG_INTERNAL` -- comma-separated debug subsystems (Node SDK only). Values: `diagnostics` (transport-status listener — connectivity transitions and alive snapshots), `forward_transport` (forward the underlying transport's own log output through the Blocks logger under `[Transport]`). Neither implied by `LOG_LEVEL=debug`. See `dev_docs/SDK_CONTRACT.md` §11.2 for the canonical contract.
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
  // tokenEndpoint: 'https://my-backend/token',  // auth mode 2: session-authenticated backend endpoint (customer proxy OR dashboard embedder — see SDK_CONTRACT §8.6.4b/g)
  // tokenProvider: async () => ({ token, expiresAt }),  // auth mode 3: custom
});
```

- `billingMode` is required ('free' → playground keyset, 'paid' → network keyset) and must match the target agent's server-derived billingMode (exception: authenticated same-org callers are exempt from this check). Read it from the registry: `(await getAgent(name)).billingMode`.
- Exactly one auth mode: `apiKey`, `tokenEndpoint`, or `tokenProvider`
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
  the agent that received the task. Provider-role access is scoped by
  the caller's active org: the agent's org must match the
  active org from the session `X-Active-Org` header or the credential's
  org claim, AND the caller must be a current member of that org.
  Admins with an admin-typed active org get the cross-org bypass.
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

### Scaffold

```bash
blocks init <name> --yes --language node                  # Provider scaffold (handler + agent-card.json)
blocks init <name> --yes --language node --type consumer  # Consumer scaffold (index.ts using TaskClient)
```

`blocks init` defaults `--type provider`. Consumer projects produce
`index.ts` plus a `package.json` with a `start` script -- no
`agent-card.json`, no handler, no publish step. The non-interactive
mode requires the name argument; without it, the CLI errors out.

### Authenticate

```bash
blocks login --write-env            # Authenticate and write BLOCKS_API_KEY to .env
blocks login --write-env --dir ./x  # Write .env to a specific directory
blocks login --no-write-env         # Authenticate without touching .env (skips prompt)
blocks login --api-key "$KEY" --write-env       # Skip browser flow with a pre-issued key
echo "$KEY" | blocks login --api-key-stdin --write-env  # Read key from stdin
blocks whoami                       # Print org, key id, expiry
blocks whoami --json                # Structured: org_name, org_id, key_id, expires_at, days_remaining, expired
blocks logout                       # Delete ~/.config/blocks/credentials.json + remove BLOCKS_API_KEY from .env
```

In a non-TTY shell (CI / coding agents), `blocks login` auto-skips
the `Write BLOCKS_API_KEY to project .env? (Y/n):` prompt without
writing -- it does not hang. Always pass `--write-env` or
`--no-write-env` for deterministic behavior.

### Publish

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
in a non-TTY shell hangs on the registry prompts -- always include
the relevant flags.

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

`blocks dashboard` resolves the dashboard URL from the CDM config or
from `BLOCKS_APP_BASE_URL` / `BLOCKS_DASHBOARD_URL` if either is set.
Export the env var to target staging / a worktree / a self-hosted
deployment. `blocks check` validates JSON schema **and** the file
referenced by `runtime.handler`.

### Manage private-agent grants (`blocks invite`)

When an agent is published with `--listing private`, access is gated
by per-user / per-org grants. The `blocks invite` family manages
those grants:

```bash
blocks invite send <agentName> --email user@example.com   # invite a specific user
blocks invite send <agentName> --org consumer-org-slug    # invite an entire consumer org
blocks invite list <agentName>                            # list pending invitations
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
